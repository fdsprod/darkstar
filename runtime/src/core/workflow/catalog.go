package workflow

import (
	"bytes"
	"context"
	"crypto/sha256"
	"darkstar/src/ports/valueschema"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"darkstar/src/core/config"
	registryport "darkstar/src/ports/capabilityregistry"
	"darkstar/src/ports/workflowstore"
)

// LoadedDefinition is one validated canonical workflow selected from configured scopes.
type LoadedDefinition struct {
	Document      Document
	CanonicalJSON json.RawMessage
	Digest        string
	SourceScope   workflowstore.Scope
	SourceRef     string
}

type InstallDisposition string

const (
	InstallCreated          InstallDisposition = "created"
	InstallAlreadyInstalled InstallDisposition = "already_installed"
)

// InstallResult reports the closed outcome of immutable installation.
type InstallResult struct {
	Version     VersionSummary     `json:"version"`
	Disposition InstallDisposition `json:"disposition"`
}

// ValidationReport is the stable result of validating one authored workflow.
// Validity is derived from Issues so the payload cannot contradict itself.
type ValidationReport struct {
	Metadata *Metadata        `json:"metadata,omitempty"`
	Digest   string           `json:"digest,omitempty"`
	Issues   ValidationErrors `json:"issues"`
}

// Definition combines immutable installation metadata with its typed document.
type Definition struct {
	Version  VersionSummary `json:"version"`
	Document Document       `json:"document"`
}

// VersionSummary is the finite list projection; the canonical document remains
// available only from Definition.
type VersionSummary struct {
	Name        string              `json:"name"`
	Version     string              `json:"version"`
	Digest      string              `json:"digest"`
	SourceScope workflowstore.Scope `json:"sourceScope"`
	SourceRef   string              `json:"sourceReference"`
	InstalledAt time.Time           `json:"installedAt"`
}

// Library is the complete authoring projection. Installed versions are
// immutable evidence; drafts are the only editable records.
type Library struct {
	Versions []VersionSummary        `json:"versions"`
	Drafts   []workflowstore.Draft   `json:"drafts"`
	Archives []workflowstore.Archive `json:"archives"`
}

// AuthoringCatalog is a server-derived set of closed schema options and
// references observed in immutable installed definitions. Empty reference
// groups are truthful: the editor must not invent unavailable profiles.
type AuthoringCatalog struct {
	SchemaVersion   int                      `json:"schemaVersion"`
	NodeTypes       []NodeType               `json:"nodeTypes"`
	ValueTypes      []ValueType              `json:"valueTypes"`
	CheckpointModes []CheckpointMode         `json:"checkpointModes"`
	PredicateOps    []string                 `json:"predicateOps"`
	Agents          StringReferenceGroup     `json:"agents"`
	Policies        StringReferenceGroup     `json:"policies"`
	Schemas         StringReferenceGroup     `json:"schemas"`
	Skills          CapabilityReferenceGroup `json:"skills"`
	Tools           CapabilityReferenceGroup `json:"tools"`
	Workflows       WorkflowReferenceGroup   `json:"workflows"`
}

type ReferenceGroupStatus string
type ReferenceUnavailableReason string

const (
	ReferenceKnown                ReferenceGroupStatus       = "known"
	ReferenceUnavailable          ReferenceGroupStatus       = "unavailable"
	ReferenceNotConfigured        ReferenceUnavailableReason = "not_configured"
	ReferenceDiscoveryUnsupported ReferenceUnavailableReason = "discovery_unsupported"
	ReferenceReadFailed           ReferenceUnavailableReason = "read_failed"
)

type StringReferenceGroup struct {
	Status ReferenceGroupStatus       `json:"status"`
	Items  []string                   `json:"items,omitempty"`
	Reason ReferenceUnavailableReason `json:"reason,omitempty"`
}
type CapabilityCatalogItem struct {
	Name         string                    `json:"name"`
	Kind         registryport.Kind         `json:"kind"`
	Class        registryport.Class        `json:"class"`
	Version      string                    `json:"version,omitempty"`
	Fingerprint  string                    `json:"fingerprint"`
	Availability registryport.Availability `json:"availability"`
}
type CapabilityReferenceGroup struct {
	Status ReferenceGroupStatus       `json:"status"`
	Items  []CapabilityCatalogItem    `json:"items,omitempty"`
	Reason ReferenceUnavailableReason `json:"reason,omitempty"`
}
type WorkflowReferenceGroup struct {
	Status ReferenceGroupStatus       `json:"status"`
	Items  []VersionSummary           `json:"items,omitempty"`
	Reason ReferenceUnavailableReason `json:"reason,omitempty"`
}

func (group StringReferenceGroup) MarshalJSON() ([]byte, error) {
	if group.Status == ReferenceKnown {
		items := group.Items
		if items == nil {
			items = []string{}
		}
		return json.Marshal(struct {
			Status ReferenceGroupStatus `json:"status"`
			Items  []string             `json:"items"`
		}{group.Status, items})
	}
	return json.Marshal(struct {
		Status ReferenceGroupStatus       `json:"status"`
		Reason ReferenceUnavailableReason `json:"reason"`
	}{group.Status, group.Reason})
}
func (group CapabilityReferenceGroup) MarshalJSON() ([]byte, error) {
	if group.Status == ReferenceKnown {
		items := group.Items
		if items == nil {
			items = []CapabilityCatalogItem{}
		}
		return json.Marshal(struct {
			Status ReferenceGroupStatus    `json:"status"`
			Items  []CapabilityCatalogItem `json:"items"`
		}{group.Status, items})
	}
	return json.Marshal(struct {
		Status ReferenceGroupStatus       `json:"status"`
		Reason ReferenceUnavailableReason `json:"reason"`
	}{group.Status, group.Reason})
}
func (group WorkflowReferenceGroup) MarshalJSON() ([]byte, error) {
	if group.Status == ReferenceKnown {
		items := group.Items
		if items == nil {
			items = []VersionSummary{}
		}
		return json.Marshal(struct {
			Status ReferenceGroupStatus `json:"status"`
			Items  []VersionSummary     `json:"items"`
		}{group.Status, items})
	}
	return json.Marshal(struct {
		Status ReferenceGroupStatus       `json:"status"`
		Reason ReferenceUnavailableReason `json:"reason"`
	}{group.Status, group.Reason})
}

type DraftCreateRequest struct {
	Name, ScopeReference, IdempotencyKey string
	Scope                                workflowstore.DraftScope
	Document, Layout                     json.RawMessage
}

type DraftUpdateRequest struct {
	ID               string
	ExpectedRevision uint64
	Document, Layout json.RawMessage
}

type DraftPublishRequest struct {
	ID, Version      string
	ExpectedRevision uint64
}

type ValidationSeverity string

const ValidationSeverityError ValidationSeverity = "error"

// AuthoringFinding maps a schema location back to editor concepts while
// retaining the canonical JSON pointer as the source of truth.
type AuthoringFinding struct {
	Code       ValidationCode     `json:"code"`
	Severity   ValidationSeverity `json:"severity"`
	Message    string             `json:"message"`
	Location   string             `json:"location,omitempty"`
	NodeID     Identifier         `json:"nodeId,omitempty"`
	EdgeID     Identifier         `json:"edgeId,omitempty"`
	Field      string             `json:"field,omitempty"`
	Suggestion string             `json:"suggestion,omitempty"`
}

type DraftValidationReport struct {
	DraftID        string             `json:"draftId"`
	Revision       uint64             `json:"revision"`
	DocumentDigest string             `json:"documentDigest"`
	Digest         string             `json:"digest,omitempty"`
	Findings       []AuthoringFinding `json:"findings"`
}

type DraftPublishResult struct {
	DraftID                string             `json:"draftId"`
	DraftRevision          uint64             `json:"draftRevision"`
	SourceValidationDigest string             `json:"sourceValidationDigest"`
	Published              VersionSummary     `json:"published"`
	Disposition            InstallDisposition `json:"disposition"`
}

type DraftPreview struct {
	DraftID        string `json:"draftId"`
	Revision       uint64 `json:"revision"`
	DocumentDigest string `json:"documentDigest"`
	Digest         string `json:"digest"`
	Route          Route  `json:"route"`
}

type NodeDefinitionCreateRequest struct {
	Scope                NodeDefinitionScope              `json:"scope"`
	Owner                string                           `json:"owner"`
	Name                 string                           `json:"name"`
	Version              string                           `json:"version"`
	DisplayName          string                           `json:"displayName"`
	Description          string                           `json:"description"`
	Inputs               map[Identifier]ValueDeclaration  `json:"inputs"`
	Outputs              map[Identifier]OutputDeclaration `json:"outputs"`
	ConfigurationSchema  json.RawMessage                  `json:"configurationSchema"`
	Implementation       NodeImplementationKind           `json:"implementation"`
	RequiredCapabilities []CapabilityReference            `json:"requiredCapabilities"`
}

// Graph is a deterministic, presentation-neutral projection of a definition.
type Graph struct {
	Workflow WorkflowIdentity  `json:"workflow"`
	Nodes    []GraphNode       `json:"nodes"`
	Edges    []RouteTransition `json:"edges"`
}

type GraphNode struct {
	ID       Identifier `json:"id"`
	Type     NodeType   `json:"type"`
	Entry    bool       `json:"entry,omitempty"`
	Terminal bool       `json:"terminal,omitempty"`
}

// RoutePreview binds one installed workflow identity to its candidate frozen route.
type RoutePreview struct {
	Workflow WorkflowIdentity `json:"workflow"`
	Route    Route            `json:"route"`
}

// Catalog coordinates scope-aware loading, version installation, and run snapshots.
type Catalog struct {
	valueSchemas valueschema.Validator
	publishMu    sync.Mutex
	source       workflowstore.Source
	store        workflowstore.Store
	capabilities registryport.Registry
	definitions  *NodeDefinitionLibrary
	now          func() time.Time
}

func (c *Catalog) WithCapabilityRegistry(registry registryport.Registry) *Catalog {
	c.capabilities = registry
	return c
}

func (c *Catalog) WithNodeDefinitionLibrary(library *NodeDefinitionLibrary) *Catalog {
	c.definitions = library
	return c
}

func NewCatalog(source workflowstore.Source, store workflowstore.Store) (*Catalog, error) {
	if source == nil || store == nil {
		return nil, errors.New("workflow catalog requires a source and store")
	}
	definitions, _ := NewNodeDefinitionLibrary()
	return &Catalog{source: source, store: store, definitions: definitions, now: time.Now}, nil
}

// Canonicalize validates an authored JSON workflow and returns its canonical
// representation and lowercase SHA-256 identity.
func Canonicalize(content []byte) (Document, json.RawMessage, string, error) {
	if err := rejectDuplicateJSONKeys(content); err != nil {
		return Document{}, nil, "", fmt.Errorf("decode workflow: %w", err)
	}
	document, err := Decode(content)
	if err != nil {
		return Document{}, nil, "", err
	}
	if validationErrors := Validate(document); len(validationErrors) != 0 {
		return Document{}, nil, "", validationErrors
	}
	typedJSON, err := json.Marshal(document)
	if err != nil {
		return Document{}, nil, "", fmt.Errorf("canonicalize workflow: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(typedJSON))
	decoder.UseNumber()
	var canonicalValue any
	if err := decoder.Decode(&canonicalValue); err != nil {
		return Document{}, nil, "", fmt.Errorf("canonicalize workflow: %w", err)
	}
	canonical, err := json.Marshal(canonicalValue)
	if err != nil {
		return Document{}, nil, "", fmt.Errorf("canonicalize workflow: %w", err)
	}
	if _, err := Decode(canonical); err != nil {
		return Document{}, nil, "", fmt.Errorf("validate canonical workflow: %w", err)
	}
	digest := sha256.Sum256(canonical)
	return document, canonical, hex.EncodeToString(digest[:]), nil
}

func rejectDuplicateJSONKeys(content []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.UseNumber()
	var visit func() error
	visit = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delimiter {
		case '{':
			keys := make(map[string]struct{})
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return errors.New("JSON object key is not a string")
				}
				if _, exists := keys[key]; exists {
					return fmt.Errorf("duplicate JSON object key %q", key)
				}
				keys[key] = struct{}{}
				if err := visit(); err != nil {
					return err
				}
			}
		case '[':
			for decoder.More() {
				if err := visit(); err != nil {
					return err
				}
			}
		default:
			return fmt.Errorf("unexpected JSON delimiter %q", delimiter)
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if closing != json.Delim(map[json.Delim]rune{'{': '}', '[': ']'}[delimiter]) {
			return errors.New("mismatched JSON delimiter")
		}
		return nil
	}
	if err := visit(); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("workflow contains more than one JSON value")
		}
		return err
	}
	return nil
}

// Load selects the highest-precedence source for every workflow name/version.
// Duplicate definitions within one scope fail instead of depending on filename order.
func (c *Catalog) Load(ctx context.Context) ([]LoadedDefinition, error) {
	candidates, err := c.source.Load(ctx)
	if err != nil {
		return nil, err
	}
	selected := make(map[string]LoadedDefinition, len(candidates))
	for _, candidate := range candidates {
		loaded, err := loadCandidate(candidate, c.valueSchemas)
		if err != nil {
			return nil, err
		}
		key := loaded.Document.Metadata.Name + "\x00" + loaded.Document.Metadata.Version
		if current, exists := selected[key]; exists {
			if current.SourceScope == loaded.SourceScope {
				return nil, fmt.Errorf("workflow %s %s is defined more than once in %s scope (%q and %q)",
					loaded.Document.Metadata.Name, loaded.Document.Metadata.Version, loaded.SourceScope, current.SourceRef, loaded.SourceRef)
			}
			if scopePrecedence(current.SourceScope) > scopePrecedence(loaded.SourceScope) {
				continue
			}
		}
		selected[key] = loaded
	}
	loaded := make([]LoadedDefinition, 0, len(selected))
	for _, definition := range selected {
		loaded = append(loaded, definition)
	}
	sort.Slice(loaded, func(i, j int) bool {
		if loaded[i].Document.Metadata.Name != loaded[j].Document.Metadata.Name {
			return loaded[i].Document.Metadata.Name < loaded[j].Document.Metadata.Name
		}
		return loaded[i].Document.Metadata.Version < loaded[j].Document.Metadata.Version
	})
	return loaded, nil
}

// Install validates and installs one explicitly selected authored candidate.
func (c *Catalog) Install(ctx context.Context, candidate workflowstore.Candidate) (InstallResult, error) {
	definition, issues, err := c.resolveCandidateSubworkflows(ctx, candidate)
	if err != nil {
		return InstallResult{}, err
	}
	if len(issues) != 0 {
		return InstallResult{}, issues
	}
	return c.install(ctx, definition)
}

// ValidateCandidate reports structural and semantic findings without installing.
func (c *Catalog) ValidateCandidate(ctx context.Context, candidate workflowstore.Candidate) ValidationReport {
	return c.validateResolvedCandidate(ctx, candidate)
}

// List returns finite metadata without duplicating canonical document bytes.
func (c *Catalog) List(ctx context.Context, name string) ([]VersionSummary, error) {
	versions, err := c.store.InstalledVersions(ctx, name)
	if err != nil {
		return nil, err
	}
	result := make([]VersionSummary, len(versions))
	for index, version := range versions {
		result[index] = versionSummary(version)
	}
	return result, nil
}

func (c *Catalog) Library(ctx context.Context) (Library, error) {
	versions, err := c.List(ctx, "")
	if err != nil {
		return Library{}, err
	}
	drafts, err := c.store.Drafts(ctx)
	if err != nil {
		return Library{}, err
	}
	archives, err := c.store.Archives(ctx)
	if err != nil {
		return Library{}, err
	}
	return Library{Versions: versions, Drafts: drafts, Archives: archives}, nil
}

func (c *Catalog) AuthoringCatalog(ctx context.Context) (AuthoringCatalog, error) {
	unavailableStrings := StringReferenceGroup{Status: ReferenceUnavailable, Reason: ReferenceNotConfigured}
	unavailableCapabilities := CapabilityReferenceGroup{Status: ReferenceUnavailable, Reason: ReferenceNotConfigured}
	workflows := WorkflowReferenceGroup{Status: ReferenceKnown, Items: []VersionSummary{}}
	versions, versionErr := c.List(ctx, "")
	archives, archiveErr := c.store.Archives(ctx)
	if versionErr != nil || archiveErr != nil {
		workflows = WorkflowReferenceGroup{Status: ReferenceUnavailable, Reason: ReferenceReadFailed}
	} else {
		archived := make(map[string]bool, len(archives))
		for _, value := range archives {
			archived[value.Name+"\x00"+value.Version] = true
		}
		for _, version := range versions {
			if !archived[version.Name+"\x00"+version.Version] {
				workflows.Items = append(workflows.Items, version)
			}
		}
	}
	skills, tools := unavailableCapabilities, unavailableCapabilities
	if c.capabilities != nil {
		records, err := c.capabilities.Snapshot(ctx)
		if err != nil {
			skills, tools = CapabilityReferenceGroup{Status: ReferenceUnavailable, Reason: ReferenceReadFailed}, CapabilityReferenceGroup{Status: ReferenceUnavailable, Reason: ReferenceReadFailed}
		} else {
			skills, tools = CapabilityReferenceGroup{Status: ReferenceKnown, Items: []CapabilityCatalogItem{}}, CapabilityReferenceGroup{Status: ReferenceKnown, Items: []CapabilityCatalogItem{}}
			for _, record := range records {
				item := CapabilityCatalogItem{Name: record.Name, Kind: record.Kind, Class: record.Class, Version: record.DeclaredVersion, Fingerprint: record.Fingerprint, Availability: record.Availability}
				switch record.Kind {
				case registryport.KindSkill:
					skills.Items = append(skills.Items, item)
				case registryport.KindTool:
					tools.Items = append(tools.Items, item)
				}
			}
			sort.Slice(skills.Items, func(i, j int) bool {
				return skills.Items[i].Name < skills.Items[j].Name || skills.Items[i].Name == skills.Items[j].Name && skills.Items[i].Fingerprint < skills.Items[j].Fingerprint
			})
			sort.Slice(tools.Items, func(i, j int) bool {
				return tools.Items[i].Name < tools.Items[j].Name || tools.Items[i].Name == tools.Items[j].Name && tools.Items[i].Fingerprint < tools.Items[j].Fingerprint
			})
		}
	}
	return AuthoringCatalog{SchemaVersion: 1,
		NodeTypes:       []NodeType{NodeReasoning, NodeGate, NodeCommand, NodeApproval, NodeSubworkflow, NodePointExecution},
		ValueTypes:      []ValueType{ValueNull, ValueBoolean, ValueInteger, ValueNumber, ValueString, ValueArray, ValueObject},
		CheckpointModes: []CheckpointMode{CheckpointNone, CheckpointAcknowledge, CheckpointApprove, CheckpointApproveOnChange, CheckpointExternal},
		PredicateOps:    []string{"const", "eq", "ne", "lt", "lte", "gt", "gte", "present", "all", "any", "not"},
		Agents:          unavailableStrings, Policies: unavailableStrings, Schemas: unavailableStrings, Skills: skills, Tools: tools, Workflows: workflows}, nil
}

func (c *Catalog) ArchiveVersion(ctx context.Context, name, version string) (workflowstore.Archive, error) {
	value, _, err := c.store.ArchiveVersion(ctx, name, version, c.now().UTC().Round(0))
	return value, err
}

func (c *Catalog) NodeDefinitions(ctx context.Context, filter NodeDefinitionFilter) ([]NodeDefinition, error) {
	library, err := c.nodeDefinitionLibrary(ctx)
	if err != nil {
		return nil, err
	}
	values := library.Search(filter)
	installed, err := c.store.InstalledVersions(ctx, "")
	if err != nil {
		return nil, err
	}
	definitions := make([]Definition, 0, len(installed))
	for _, item := range installed {
		definition, _, _, err := Canonicalize(item.Document)
		if err == nil {
			definitions = append(definitions, Definition{Version: versionSummary(item), Document: definition})
		}
	}
	for index := range values {
		values[index].Usage = DefinitionUsage(values[index].Ref, definitions)
	}
	return values, nil
}

func (c *Catalog) PublishNodeDefinition(ctx context.Context, value NodeDefinition) (NodeDefinition, error) {
	store, ok := c.store.(workflowstore.NodeDefinitionStore)
	if !ok {
		return NodeDefinition{}, errors.New("node definition persistence is unavailable")
	}
	sealed, err := sealNodeDefinition(value)
	if err != nil {
		return NodeDefinition{}, err
	}
	scope, owner, err := definitionOwner(sealed.Ref.Ref)
	if err != nil {
		return NodeDefinition{}, err
	}
	document, err := json.Marshal(sealed)
	if err != nil {
		return NodeDefinition{}, err
	}
	record, _, err := store.InstallNodeDefinition(ctx, workflowstore.NodeDefinitionRecord{Scope: scope, Owner: owner, Name: sealed.Ref.Ref.DefinitionName(), Version: sealed.Ref.Ref.DefinitionVersion(), Digest: sealed.Ref.Digest, Document: document, CreatedAt: sealed.CreatedAt})
	if err != nil {
		return NodeDefinition{}, err
	}
	return decodeStoredNodeDefinition(record)
}

func (c *Catalog) CreateNodeDefinition(ctx context.Context, input NodeDefinitionCreateRequest) (NodeDefinition, error) {
	var ref NodeDefinitionRef
	switch input.Scope {
	case NodeDefinitionProject:
		ref = ProjectNodeDefinitionRef{ProjectID: input.Owner, Name: input.Name, Version: input.Version}
	case NodeDefinitionUser:
		ref = UserNodeDefinitionRef{UserID: input.Owner, Name: input.Name, Version: input.Version}
	default:
		return NodeDefinition{}, ErrNodeDefinitionImmutable
	}
	return c.PublishNodeDefinition(ctx, NodeDefinition{
		Ref: ResolvedNodeDefinitionRef{Ref: ref}, DisplayName: input.DisplayName, Description: input.Description,
		Compatibility: APIVersionV1Alpha3, Inputs: input.Inputs, Outputs: input.Outputs,
		ConfigurationSchema: input.ConfigurationSchema, Implementation: input.Implementation,
		RequiredCapabilities: input.RequiredCapabilities, Lifecycle: NodeDefinitionActive, CreatedAt: c.now().UTC().Round(0),
	})
}

func (c *Catalog) DuplicateNodeDefinition(ctx context.Context, source ResolvedNodeDefinitionRef, scope NodeDefinitionScope, owner, name, version string) (NodeDefinition, error) {
	library, err := c.nodeDefinitionLibrary(ctx)
	if err != nil {
		return NodeDefinition{}, err
	}
	var target NodeDefinitionRef
	switch scope {
	case NodeDefinitionProject:
		target = ProjectNodeDefinitionRef{ProjectID: owner, Name: name, Version: version}
	case NodeDefinitionUser:
		target = UserNodeDefinitionRef{UserID: owner, Name: name, Version: version}
	default:
		return NodeDefinition{}, ErrNodeDefinitionImmutable
	}
	value, err := library.Duplicate(source, target, c.now().UTC().Round(0))
	if err != nil {
		return NodeDefinition{}, err
	}
	return c.PublishNodeDefinition(ctx, value)
}

func (c *Catalog) VersionNodeDefinition(ctx context.Context, source ResolvedNodeDefinitionRef, version string) (NodeDefinition, error) {
	library, err := c.nodeDefinitionLibrary(ctx)
	if err != nil {
		return NodeDefinition{}, err
	}
	value, err := library.Version(source, version, c.now().UTC().Round(0))
	if err != nil {
		return NodeDefinition{}, err
	}
	return c.PublishNodeDefinition(ctx, value)
}

func (c *Catalog) ArchiveNodeDefinition(ctx context.Context, ref ResolvedNodeDefinitionRef) (NodeDefinition, error) {
	scope, owner, err := definitionOwner(ref.Ref)
	if err != nil {
		return NodeDefinition{}, err
	}
	store, ok := c.store.(workflowstore.NodeDefinitionStore)
	if !ok {
		return NodeDefinition{}, errors.New("node definition persistence is unavailable")
	}
	record, _, err := store.ArchiveNodeDefinition(ctx, scope, owner, ref.Ref.DefinitionName(), ref.Ref.DefinitionVersion(), c.now().UTC().Round(0))
	if err != nil {
		return NodeDefinition{}, err
	}
	if record.Digest != ref.Digest {
		return NodeDefinition{}, ErrNodeDefinitionNotFound
	}
	return decodeStoredNodeDefinition(record)
}

func (c *Catalog) nodeDefinitionLibrary(ctx context.Context) (*NodeDefinitionLibrary, error) {
	current := c.definitions.Search(NodeDefinitionFilter{})
	builtins := []NodeDefinition{}
	for _, value := range current {
		if value.Ref.Ref.definitionScope() == NodeDefinitionBuiltIn {
			builtins = append(builtins, value)
		}
	}
	library, err := NewNodeDefinitionLibrary(builtins...)
	if err != nil {
		return nil, err
	}
	for _, value := range current {
		if value.Ref.Ref.definitionScope() != NodeDefinitionBuiltIn {
			if _, err := library.Publish(value); err != nil {
				return nil, err
			}
		}
	}
	if store, ok := c.store.(workflowstore.NodeDefinitionStore); ok {
		records, err := store.NodeDefinitions(ctx)
		if err != nil {
			return nil, err
		}
		for _, record := range records {
			value, err := decodeStoredNodeDefinition(record)
			if err != nil {
				return nil, err
			}
			if _, err := library.Publish(value); err != nil {
				return nil, err
			}
			if record.ArchivedAt != nil {
				_, _ = library.Archive(value.Ref)
			}
		}
	}
	return library, nil
}

func decodeStoredNodeDefinition(record workflowstore.NodeDefinitionRecord) (NodeDefinition, error) {
	var value NodeDefinition
	if err := json.Unmarshal(record.Document, &value); err != nil {
		return value, err
	}
	if value.Ref.Digest != record.Digest {
		return value, ErrNodeDefinitionNotFound
	}
	if record.ArchivedAt != nil {
		value.Lifecycle = NodeDefinitionArchived
	}
	return value, nil
}
func definitionOwner(ref NodeDefinitionRef) (workflowstore.DraftScope, string, error) {
	switch value := ref.(type) {
	case ProjectNodeDefinitionRef:
		return workflowstore.DraftScopeProject, value.ProjectID, nil
	case UserNodeDefinitionRef:
		return workflowstore.DraftScopeUser, value.UserID, nil
	case BuiltInNodeDefinitionRef:
		return "", "", ErrNodeDefinitionImmutable
	default:
		return "", "", fmt.Errorf("unsupported node-definition ref %T", ref)
	}
}

func (c *Catalog) CreateDraft(ctx context.Context, request DraftCreateRequest) (workflowstore.Draft, error) {
	if err := validateDraftIdentity(request.Name, request.Scope, request.ScopeReference, request.IdempotencyKey); err != nil {
		return workflowstore.Draft{}, err
	}
	if len(request.Document) == 0 {
		return workflowstore.Draft{}, errors.New("workflow draft document is required")
	}
	if len(request.Layout) == 0 {
		request.Layout = json.RawMessage(`{}`)
	}
	id := draftID(request.IdempotencyKey)
	value, _, err := c.store.CreateDraft(ctx, workflowstore.CreateDraftRequest{ID: id, Name: request.Name,
		Scope: request.Scope, ScopeReference: request.ScopeReference, IdempotencyKey: request.IdempotencyKey,
		Document: request.Document, Layout: request.Layout, CreatedAt: c.now().UTC().Round(0)})
	return value, err
}

func (c *Catalog) DuplicateDraft(ctx context.Context, name, version, newName string, scope workflowstore.DraftScope, scopeReference, idempotencyKey string) (workflowstore.Draft, error) {
	if err := validateDraftIdentity(newName, scope, scopeReference, idempotencyKey); err != nil {
		return workflowstore.Draft{}, err
	}
	definition, err := c.Definition(ctx, name, version)
	if err != nil {
		return workflowstore.Draft{}, err
	}
	document, err := rewriteWorkflowMetadata(definition.Document, newName, definition.Version.Version)
	if err != nil {
		return workflowstore.Draft{}, err
	}
	value, _, err := c.store.CreateDraft(ctx, workflowstore.CreateDraftRequest{ID: draftID(idempotencyKey), Name: newName,
		Scope: scope, ScopeReference: scopeReference, BaseVersion: definition.Version.Version, IdempotencyKey: idempotencyKey,
		Document: document, Layout: json.RawMessage(`{}`), CreatedAt: c.now().UTC().Round(0)})
	return value, err
}

func (c *Catalog) Draft(ctx context.Context, id string) (workflowstore.Draft, error) {
	return c.store.Draft(ctx, id)
}

func (c *Catalog) UpdateDraft(ctx context.Context, request DraftUpdateRequest) (workflowstore.Draft, error) {
	current, err := c.store.Draft(ctx, request.ID)
	if err != nil {
		return workflowstore.Draft{}, err
	}
	if request.ExpectedRevision == 0 {
		return workflowstore.Draft{}, errors.New("positive expected draft revision is required")
	}
	if len(request.Document) == 0 {
		request.Document = current.Document
	}
	if len(request.Layout) == 0 {
		request.Layout = current.Layout
	}
	name := draftDocumentName(request.Document)
	if name == "" {
		name = current.Name
	}
	return c.store.UpdateDraft(ctx, workflowstore.UpdateDraftRequest{ID: request.ID, ExpectedRevision: request.ExpectedRevision,
		Name: name, Document: request.Document, Layout: request.Layout, UpdatedAt: c.now().UTC().Round(0)})
}

func (c *Catalog) RenameDraft(ctx context.Context, id, name string, expectedRevision uint64) (workflowstore.Draft, error) {
	if strings.TrimSpace(name) == "" {
		return workflowstore.Draft{}, errors.New("workflow name is required")
	}
	current, err := c.store.Draft(ctx, id)
	if err != nil {
		return workflowstore.Draft{}, err
	}
	var document Document
	if err := json.Unmarshal(current.Document, &document); err != nil {
		return workflowstore.Draft{}, fmt.Errorf("rename requires a decodable workflow document: %w", err)
	}
	rewritten, err := rewriteWorkflowMetadata(document, name, document.Metadata.Version)
	if err != nil {
		return workflowstore.Draft{}, err
	}
	return c.store.UpdateDraft(ctx, workflowstore.UpdateDraftRequest{ID: id, ExpectedRevision: expectedRevision,
		Name: name, Document: rewritten, Layout: current.Layout, UpdatedAt: c.now().UTC().Round(0)})
}

func (c *Catalog) ValidateDraft(ctx context.Context, id string, expectedRevision uint64) (DraftValidationReport, error) {
	draft, err := c.store.Draft(ctx, id)
	if err != nil {
		return DraftValidationReport{}, err
	}
	if expectedRevision != 0 && draft.Revision != expectedRevision {
		return DraftValidationReport{}, fmt.Errorf("%w: draft %s is revision %d, expected %d", workflowstore.ErrDraftConflict, id, draft.Revision, expectedRevision)
	}
	report := c.validateResolvedCandidate(ctx, workflowstore.Candidate{Scope: draftScope(draft.Scope), Reference: draft.ScopeReference, Content: draft.Document})
	findings := make([]AuthoringFinding, len(report.Issues))
	for index, issue := range report.Issues {
		findings[index] = authoringFinding(issue, draft.Document)
	}
	return DraftValidationReport{DraftID: id, Revision: draft.Revision, DocumentDigest: draft.DocumentDigest, Digest: report.Digest, Findings: findings}, nil
}

func (c *Catalog) PreviewDraft(ctx context.Context, id string, expectedRevision uint64, request RouteRequest, routeContext RouteContext) (DraftPreview, ValidationErrors, error) {
	draft, err := c.store.Draft(ctx, id)
	if err != nil {
		return DraftPreview{}, nil, err
	}
	if expectedRevision == 0 || draft.Revision != expectedRevision {
		return DraftPreview{}, nil, fmt.Errorf("%w: draft %s is revision %d, expected %d", workflowstore.ErrDraftConflict, id, draft.Revision, expectedRevision)
	}
	definition, issues, err := c.resolveCandidateSubworkflows(ctx, workflowstore.Candidate{Scope: draftScope(draft.Scope), Reference: draft.ScopeReference, Content: draft.Document})
	if err != nil {
		return DraftPreview{}, nil, err
	}
	if len(issues) != 0 {
		return DraftPreview{}, issues, nil
	}
	route, routeIssues := CreateRoute(definition.Document, request, routeContext)
	if len(routeIssues) != 0 {
		return DraftPreview{}, routeIssues, nil
	}
	return DraftPreview{DraftID: id, Revision: draft.Revision, DocumentDigest: draft.DocumentDigest, Digest: definition.Digest, Route: route}, nil, nil
}

func (c *Catalog) validateResolvedCandidate(ctx context.Context, candidate workflowstore.Candidate) ValidationReport {
	definition, issues, err := c.resolveCandidateSubworkflows(ctx, candidate)
	if err != nil {
		var validationErrors ValidationErrors
		if errors.As(err, &validationErrors) {
			return ValidationReport{Issues: validationErrors}
		}
		return ValidationReport{Issues: ValidationErrors{{Code: ValidationSchemaInvalid, Message: err.Error(), Location: decoderErrorLocation(err)}}}
	}
	metadata := definition.Document.Metadata
	return ValidationReport{Metadata: &metadata, Digest: definition.Digest, Issues: issues}
}

func decoderErrorLocation(err error) string {
	var decodeError *DecodeError
	if errors.As(err, &decodeError) {
		return decodeError.Location
	}
	return ""
}

// resolveCandidateSubworkflows turns every exact child name/version reference into
// an installed digest and validates the authored route and data mappings against
// that immutable child before a draft may validate or publish successfully.
func (c *Catalog) resolveCandidateSubworkflows(ctx context.Context, candidate workflowstore.Candidate) (LoadedDefinition, ValidationErrors, error) {
	definition, err := loadCandidate(candidate, c.valueSchemas)
	if err != nil {
		return LoadedDefinition{}, nil, err
	}
	issues := make(ValidationErrors, 0)
	rootKey := definition.Document.Metadata.Name + "\x00" + definition.Document.Metadata.Version
	for _, nodeID := range sortedNodeIDs(definition.Document.Spec.Nodes) {
		fields := definition.Document.Spec.Nodes[nodeID].Fields()
		if fields.Definition != nil {
			base := fmt.Sprintf("/spec/nodes/%s/definition", nodeID)
			library, libraryErr := c.nodeDefinitionLibrary(ctx)
			if libraryErr != nil {
				issues = append(issues, ValidationError{Code: ValidationReferenceMissing, Message: libraryErr.Error(), Location: base})
				continue
			}
			resolved, resolveErr := library.Resolve(*fields.Definition)
			if resolveErr != nil {
				issues = append(issues, ValidationError{Code: ValidationReferenceMissing, Message: "exact reusable node definition is unavailable or its digest does not match", Location: base})
				continue
			}
			if resolved.Compatibility != definition.Document.APIVersion {
				issues = append(issues, ValidationError{Code: ValidationDefinitionInvalid, Message: fmt.Sprintf("node definition requires compatibility %s", resolved.Compatibility), Location: base + "/version"})
			}
			issues = append(issues, validateDefinitionPorts(fields, resolved, nodeID)...)
			if c.capabilities != nil {
				records, capabilityErr := c.capabilities.Snapshot(ctx)
				if capabilityErr != nil {
					issues = append(issues, ValidationError{Code: ValidationCapabilityMissing, Message: "required capability availability could not be verified", Location: base})
					continue
				}
				available := map[CapabilityReference]bool{}
				for _, record := range records {
					if record.Availability == registryport.AvailabilityAvailable {
						available[CapabilityReference{Kind: CapabilityKind(record.Kind), Name: record.Name}] = true
					}
				}
				for _, required := range resolved.RequiredCapabilities {
					if !available[required] {
						issues = append(issues, ValidationError{Code: ValidationCapabilityMissing, Message: fmt.Sprintf("node definition requires unavailable %s %q", required.Kind, required.Name), Location: base})
					}
				}
			}
		}
		node, ok := definition.Document.Spec.Nodes[nodeID].(SubworkflowNode)
		if !ok {
			continue
		}
		base := fmt.Sprintf("/spec/nodes/%s/call", nodeID)
		child, childErr := c.Definition(ctx, node.Call.Workflow.Name, node.Call.Workflow.Version)
		if childErr != nil {
			if errors.Is(childErr, workflowstore.ErrNotFound) {
				issues = append(issues, ValidationError{Code: ValidationReferenceMissing, Message: fmt.Sprintf("sub-workflow %s %s is not installed", node.Call.Workflow.Name, node.Call.Workflow.Version), Location: base + "/workflow"})
			} else {
				issues = append(issues, ValidationError{Code: ValidationSchemaInvalid, Message: fmt.Sprintf("could not inspect sub-workflow %s %s: %v", node.Call.Workflow.Name, node.Call.Workflow.Version, childErr), Location: base + "/workflow"})
			}
			continue
		}
		if node.Call.Workflow.Digest != "" && node.Call.Workflow.Digest != child.Version.Digest {
			issues = append(issues, ValidationError{Code: ValidationReferenceMissing,
				Message: fmt.Sprintf("sub-workflow digest %s does not match installed digest %s", node.Call.Workflow.Digest, child.Version.Digest), Location: base + "/workflow/digest"})
			continue
		}
		node.Call.Workflow.Digest = child.Version.Digest
		definition.Document.Spec.Nodes[nodeID] = node
		issues = append(issues, validateSubworkflowContract(node, child, base)...)
		childKey := child.Version.Name + "\x00" + child.Version.Version
		if childKey == rootKey {
			issues = append(issues, ValidationError{Code: ValidationUnboundedCycle, Message: "recursive sub-workflow call graph references the candidate itself", Location: base + "/workflow"})
		} else {
			issues = append(issues, c.validateInstalledCallGraph(ctx, child, map[string]bool{rootKey: true, childKey: true}, base+"/workflow")...)
		}
	}
	if len(issues) != 0 {
		sortValidationErrors(issues)
		return definition, issues, nil
	}
	resolved, marshalErr := json.Marshal(definition.Document)
	if marshalErr != nil {
		return LoadedDefinition{}, nil, fmt.Errorf("encode resolved sub-workflows: %w", marshalErr)
	}
	definition, err = loadCandidate(workflowstore.Candidate{Scope: candidate.Scope, Reference: candidate.Reference, Content: resolved}, c.valueSchemas)
	return definition, nil, err
}

func validateDefinitionPorts(fields NodeFields, definition NodeDefinition, nodeID Identifier) ValidationErrors {
	issues := ValidationErrors{}
	base := fmt.Sprintf("/spec/nodes/%s/definition", nodeID)
	for id, expected := range definition.Inputs {
		binding, ok := fields.Inputs[id]
		if !ok || binding.ValueType() != expected.Type {
			issues = append(issues, ValidationError{Code: ValidationBindingIncompatible, Message: fmt.Sprintf("definition input %q must be declared as %s", id, expected.Type), Location: base})
		}
	}
	for id, expected := range definition.Outputs {
		output, ok := fields.Outputs[id]
		if !ok || output.Type != expected.Type || expected.Schema != "" && output.Schema != expected.Schema {
			issues = append(issues, ValidationError{Code: ValidationBindingIncompatible, Message: fmt.Sprintf("definition output %q does not match its reusable contract", id), Location: base})
		}
	}
	return issues
}

func (c *Catalog) validateInstalledCallGraph(ctx context.Context, definition Definition, stack map[string]bool, rootLocation string) ValidationErrors {
	issues := make(ValidationErrors, 0)
	for _, nodeID := range sortedNodeIDs(definition.Document.Spec.Nodes) {
		node, ok := definition.Document.Spec.Nodes[nodeID].(SubworkflowNode)
		if !ok {
			continue
		}
		location := fmt.Sprintf("%s -> %s@%s:%s", rootLocation, definition.Version.Name, definition.Version.Version, nodeID)
		key := node.Call.Workflow.Name + "\x00" + node.Call.Workflow.Version
		if stack[key] {
			issues = append(issues, ValidationError{Code: ValidationUnboundedCycle, Message: fmt.Sprintf("recursive sub-workflow call graph detected through %s", location), Location: rootLocation})
			continue
		}
		child, err := c.Definition(ctx, node.Call.Workflow.Name, node.Call.Workflow.Version)
		if err != nil {
			if errors.Is(err, workflowstore.ErrNotFound) {
				issues = append(issues, ValidationError{Code: ValidationReferenceMissing, Message: fmt.Sprintf("transitive sub-workflow %s %s is not installed", node.Call.Workflow.Name, node.Call.Workflow.Version), Location: rootLocation})
			} else {
				issues = append(issues, ValidationError{Code: ValidationSchemaInvalid, Message: fmt.Sprintf("could not inspect transitive sub-workflow through %s: %v", location, err), Location: rootLocation})
			}
			continue
		}
		if node.Call.Workflow.Digest != "" && node.Call.Workflow.Digest != child.Version.Digest {
			issues = append(issues, ValidationError{Code: ValidationReferenceMissing, Message: fmt.Sprintf("transitive sub-workflow digest mismatch through %s", location), Location: rootLocation})
			continue
		}
		issues = append(issues, relocateSubworkflowIssues(validateSubworkflowContract(node, child, rootLocation), location)...)
		next := make(map[string]bool, len(stack)+1)
		for item := range stack {
			next[item] = true
		}
		next[key] = true
		issues = append(issues, c.validateInstalledCallGraph(ctx, child, next, rootLocation)...)
	}
	return issues
}

func validateSubworkflowContract(node SubworkflowNode, child Definition, base string) ValidationErrors {
	issues := make(ValidationErrors, 0)
	if entry, exists := child.Document.Spec.Nodes[node.Call.Entry]; !exists || !entry.Fields().Entry {
		issues = append(issues, ValidationError{Code: ValidationDefaultRouteInvalid, Message: "sub-workflow entry is absent or not entry-capable", Location: base + "/entry"})
	}
	for index, terminalID := range node.Call.Terminals {
		if terminal, exists := child.Document.Spec.Nodes[terminalID]; !exists || !terminal.Fields().Terminal {
			issues = append(issues, ValidationError{Code: ValidationDefaultRouteInvalid, Message: "sub-workflow terminal is absent or not terminal-capable", Location: fmt.Sprintf("%s/terminals/%d", base, index)})
		}
	}
	route, routeIssues := CreateRoute(child.Document, RouteRequest{From: node.Call.Entry, Until: node.Call.Terminals}, RouteContext{})
	for _, issue := range routeIssues {
		issue.Location = base + "/entry"
		issues = append(issues, issue)
	}
	routeNodes := map[Identifier]bool{}
	for _, item := range route.Nodes {
		routeNodes[item.ID] = true
	}
	for _, childInput := range sortedValueDeclarationIDs(child.Document.Spec.Inputs) {
		if _, mapped := node.Call.Inputs[childInput]; !mapped {
			issues = append(issues, ValidationError{Code: ValidationReferenceMissing, Message: fmt.Sprintf("required child input %q has no parent mapping", childInput), Location: fmt.Sprintf("%s/inputs/%s", base, childInput)})
		}
	}
	for _, childInput := range sortedIdentifierMapKeys(node.Call.Inputs) {
		parentInput := node.Call.Inputs[childInput]
		declaration, childExists := child.Document.Spec.Inputs[childInput]
		binding, parentExists := node.Common.Inputs[parentInput]
		if !childExists {
			issues = append(issues, ValidationError{Code: ValidationReferenceMissing, Message: fmt.Sprintf("sub-workflow input %q is not declared by the installed child", childInput), Location: fmt.Sprintf("%s/inputs/%s", base, childInput)})
		} else if !parentExists {
			issues = append(issues, ValidationError{Code: ValidationReferenceMissing, Message: fmt.Sprintf("parent input %q is not declared by the sub-workflow node", parentInput), Location: fmt.Sprintf("%s/inputs/%s", base, childInput)})
		} else if !typesCompatible(binding.ValueType(), declaration.Type) {
			issues = append(issues, ValidationError{Code: ValidationBindingIncompatible, Message: fmt.Sprintf("parent input %q cannot supply child input %q", parentInput, childInput), Location: fmt.Sprintf("%s/inputs/%s", base, childInput)})
		}
	}
	for _, parentOutput := range sortedStringMapKeys(node.Call.Outputs) {
		childReference := node.Call.Outputs[parentOutput]
		parts := strings.Split(childReference, ".")
		parentDeclaration, parentExists := node.Common.Outputs[parentOutput]
		if len(parts) != 4 || parts[0] != "node" || parts[2] != "output" {
			issues = append(issues, ValidationError{Code: ValidationReferenceMissing, Message: fmt.Sprintf("sub-workflow output %q is not a node output reference", childReference), Location: fmt.Sprintf("%s/outputs/%s", base, parentOutput)})
			continue
		}
		childNodeID := Identifier(parts[1])
		childNode, childNodeExists := child.Document.Spec.Nodes[childNodeID]
		if !childNodeExists {
			issues = append(issues, ValidationError{Code: ValidationReferenceMissing, Message: fmt.Sprintf("sub-workflow output references unknown child node %q", parts[1]), Location: fmt.Sprintf("%s/outputs/%s", base, parentOutput)})
			continue
		}
		if !routeNodes[childNodeID] {
			issues = append(issues, ValidationError{Code: ValidationReferenceMissing, Message: fmt.Sprintf("sub-workflow output references child node %q outside the selected route", parts[1]), Location: fmt.Sprintf("%s/outputs/%s", base, parentOutput)})
		}
		if !parentExists {
			issues = append(issues, ValidationError{Code: ValidationReferenceMissing, Message: fmt.Sprintf("parent output %q is not declared by the sub-workflow node", parentOutput), Location: fmt.Sprintf("%s/outputs/%s", base, parentOutput)})
			continue
		}
		childDeclaration, childOutputExists := childNode.Fields().Outputs[Identifier(parts[3])]
		if !childOutputExists {
			issues = append(issues, ValidationError{Code: ValidationReferenceMissing, Message: fmt.Sprintf("sub-workflow output references unknown child output %q", parts[3]), Location: fmt.Sprintf("%s/outputs/%s", base, parentOutput)})
		} else if !typesCompatible(childDeclaration.Type, parentDeclaration.Type) {
			issues = append(issues, ValidationError{Code: ValidationBindingIncompatible, Message: fmt.Sprintf("child output %q cannot populate parent output %q", childReference, parentOutput), Location: fmt.Sprintf("%s/outputs/%s", base, parentOutput)})
		}
	}
	return issues
}

func relocateSubworkflowIssues(issues ValidationErrors, hop string) ValidationErrors {
	for index := range issues {
		issues[index].Message = fmt.Sprintf("%s through %s", issues[index].Message, hop)
	}
	return issues
}

func sortedValueDeclarationIDs(values map[Identifier]ValueDeclaration) []Identifier {
	result := make([]Identifier, 0, len(values))
	for id := range values {
		result = append(result, id)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}
func sortedIdentifierMapKeys(values map[Identifier]Identifier) []Identifier {
	result := make([]Identifier, 0, len(values))
	for id := range values {
		result = append(result, id)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}
func sortedStringMapKeys(values map[Identifier]string) []Identifier {
	result := make([]Identifier, 0, len(values))
	for id := range values {
		result = append(result, id)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

func (c *Catalog) PublishDraft(ctx context.Context, request DraftPublishRequest) (DraftPublishResult, error) {
	c.publishMu.Lock()
	defer c.publishMu.Unlock()
	draft, err := c.store.Draft(ctx, request.ID)
	if err != nil {
		return DraftPublishResult{}, err
	}
	if request.ExpectedRevision == 0 || draft.Revision != request.ExpectedRevision {
		return DraftPublishResult{}, fmt.Errorf("%w: draft %s is revision %d, expected %d", workflowstore.ErrDraftConflict, request.ID, draft.Revision, request.ExpectedRevision)
	}
	validated, issues, err := c.resolveCandidateSubworkflows(ctx, workflowstore.Candidate{Scope: draftScope(draft.Scope), Reference: draft.ScopeReference, Content: draft.Document})
	if err != nil {
		return DraftPublishResult{}, err
	}
	if len(issues) != 0 {
		return DraftPublishResult{}, issues
	}
	if !semanticVersionPattern.MatchString(request.Version) {
		return DraftPublishResult{}, errors.New("invalid semantic version")
	}
	versions, err := c.store.InstalledVersions(ctx, draft.Name)
	if err != nil {
		return DraftPublishResult{}, err
	}
	for _, version := range versions {
		if semanticVersionLess(request.Version, version.Version) {
			return DraftPublishResult{}, fmt.Errorf("%w: publish a version newer than %s", workflowstore.ErrVersionConflict, version.Version)
		}
	}
	publishedDocument, err := rewriteWorkflowMetadata(validated.Document, draft.Name, request.Version)
	if err != nil {
		return DraftPublishResult{}, err
	}
	result, err := c.Install(ctx, workflowstore.Candidate{Scope: draftScope(draft.Scope), Reference: draft.ScopeReference, Content: publishedDocument})
	if err != nil {
		return DraftPublishResult{}, err
	}
	return DraftPublishResult{DraftID: draft.ID, DraftRevision: draft.Revision, SourceValidationDigest: validated.Digest, Published: result.Version, Disposition: result.Disposition}, nil
}

func (c *Catalog) DiscardDraft(ctx context.Context, id string, expectedRevision uint64) error {
	return c.store.DiscardDraft(ctx, id, expectedRevision)
}

func validateDraftIdentity(name string, scope workflowstore.DraftScope, reference, key string) error {
	if strings.TrimSpace(name) == "" || strings.TrimSpace(reference) == "" {
		return errors.New("workflow name and scope reference are required")
	}
	if scope != workflowstore.DraftScopeUser && scope != workflowstore.DraftScopeProject {
		return fmt.Errorf("invalid workflow draft scope %q", scope)
	}
	if key != strings.TrimSpace(key) || len(key) < 8 || len(key) > 128 {
		return errors.New("idempotency key must be 8-128 trimmed bytes")
	}
	return nil
}

func draftID(key string) string {
	digest := sha256.Sum256([]byte("workflow-draft\x00" + key))
	return "draft_" + hex.EncodeToString(digest[:13])
}

func draftScope(scope workflowstore.DraftScope) workflowstore.Scope {
	if scope == workflowstore.DraftScopeProject {
		return workflowstore.ScopeProject
	}
	return workflowstore.ScopeUser
}

func draftDocumentName(content json.RawMessage) string {
	var value struct {
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
	}
	if json.Unmarshal(content, &value) != nil {
		return ""
	}
	return value.Metadata.Name
}

func rewriteWorkflowMetadata(document Document, name, version string) (json.RawMessage, error) {
	document.Metadata.Name, document.Metadata.Version = name, version
	value, err := json.Marshal(document)
	if err != nil {
		return nil, fmt.Errorf("rewrite workflow metadata: %w", err)
	}
	return value, nil
}

func authoringFinding(issue ValidationError, document json.RawMessage) AuthoringFinding {
	result := AuthoringFinding{Code: issue.Code, Severity: ValidationSeverityError, Message: issue.Message,
		Location: issue.Location, Suggestion: "Correct the referenced workflow element and validate again."}
	parts := strings.Split(strings.TrimPrefix(issue.Location, "/"), "/")
	if len(parts) >= 3 && parts[0] == "spec" && parts[1] == "nodes" {
		result.NodeID = Identifier(parts[2])
		if len(parts) >= 5 && parts[3] == "transitions" {
			if transitionIndex, err := strconv.Atoi(parts[4]); err == nil {
				var raw struct {
					Spec struct {
						Nodes map[string]json.RawMessage `json:"nodes"`
					} `json:"spec"`
				}
				if json.Unmarshal(document, &raw) == nil {
					var node struct {
						Transitions []struct {
							ID Identifier `json:"id"`
						} `json:"transitions"`
					}
					if json.Unmarshal(raw.Spec.Nodes[parts[2]], &node) == nil && transitionIndex >= 0 && transitionIndex < len(node.Transitions) {
						result.EdgeID = node.Transitions[transitionIndex].ID
					}
				}
			}
		}
		if len(parts) >= 4 {
			result.Field = strings.Join(parts[3:], ".")
		}
	}
	return result
}

func versionSummary(version workflowstore.InstalledVersion) VersionSummary {
	return VersionSummary{Name: version.Name, Version: version.Version, Digest: version.Digest, SourceScope: version.SourceScope, SourceRef: version.SourceRef, InstalledAt: version.InstalledAt}
}

// Definition returns one exact installed version, or the highest semantic
// version when version is omitted.
func (c *Catalog) Definition(ctx context.Context, name, version string) (Definition, error) {
	if name == "" {
		return Definition{}, errors.New("workflow name is required")
	}
	var installed workflowstore.InstalledVersion
	var err error
	if version != "" {
		installed, err = c.store.InstalledVersion(ctx, name, version)
	} else {
		versions, listErr := c.store.InstalledVersions(ctx, name)
		if listErr != nil {
			return Definition{}, listErr
		}
		archives, archiveErr := c.store.Archives(ctx)
		if archiveErr != nil {
			return Definition{}, archiveErr
		}
		active := versions[:0]
		for _, candidate := range versions {
			archived := false
			for _, entry := range archives {
				if entry.Name == candidate.Name && entry.Version == candidate.Version {
					archived = true
					break
				}
			}
			if !archived {
				active = append(active, candidate)
			}
		}
		versions = active
		if len(versions) == 0 {
			return Definition{}, fmt.Errorf("%w: workflow %s", workflowstore.ErrNotFound, name)
		}
		installed = versions[0]
		for _, candidate := range versions[1:] {
			if semanticVersionLess(installed.Version, candidate.Version) {
				installed = candidate
			}
		}
	}
	if err != nil {
		return Definition{}, err
	}
	document, err := Decode(installed.Document)
	if err != nil {
		return Definition{}, fmt.Errorf("decode installed workflow %s %s: %w", installed.Name, installed.Version, err)
	}
	return Definition{Version: versionSummary(installed), Document: document}, nil
}

func semanticVersionLess(left, right string) bool {
	leftCore, leftPre, leftHasPre := strings.Cut(left, "-")
	rightCore, rightPre, rightHasPre := strings.Cut(right, "-")
	leftParts, rightParts := strings.Split(leftCore, "."), strings.Split(rightCore, ".")
	for index := 0; index < 3; index++ {
		if comparison := compareNumericIdentifier(leftParts[index], rightParts[index]); comparison != 0 {
			return comparison < 0
		}
	}
	if leftHasPre != rightHasPre {
		return leftHasPre
	}
	if !leftHasPre {
		return false
	}
	leftParts, rightParts = strings.Split(leftPre, "."), strings.Split(rightPre, ".")
	for index := 0; index < len(leftParts) && index < len(rightParts); index++ {
		leftNumeric, rightNumeric := numericIdentifier(leftParts[index]), numericIdentifier(rightParts[index])
		if leftNumeric && rightNumeric {
			if comparison := compareNumericIdentifier(leftParts[index], rightParts[index]); comparison != 0 {
				return comparison < 0
			}
			continue
		}
		if leftNumeric != rightNumeric {
			return leftNumeric
		}
		if leftParts[index] != rightParts[index] {
			return leftParts[index] < rightParts[index]
		}
	}
	return len(leftParts) < len(rightParts)
}

func compareNumericIdentifier(left, right string) int {
	left, right = strings.TrimLeft(left, "0"), strings.TrimLeft(right, "0")
	if left == "" {
		left = "0"
	}
	if right == "" {
		right = "0"
	}
	if len(left) < len(right) {
		return -1
	}
	if len(left) > len(right) {
		return 1
	}
	return strings.Compare(left, right)
}

func numericIdentifier(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

// Graph returns a stable node/edge projection of one installed definition.
func (c *Catalog) Graph(ctx context.Context, name, version string) (Graph, error) {
	definition, err := c.Definition(ctx, name, version)
	if err != nil {
		return Graph{}, err
	}
	nodes := make([]GraphNode, 0, len(definition.Document.Spec.Nodes))
	edges := make([]RouteTransition, 0)
	for _, id := range sortedNodeIDs(definition.Document.Spec.Nodes) {
		node := definition.Document.Spec.Nodes[id]
		fields := node.Fields()
		nodes = append(nodes, GraphNode{ID: id, Type: node.Type(), Entry: fields.Entry, Terminal: fields.Terminal})
		for _, transition := range fields.Transitions {
			edges = append(edges, RouteTransition{ID: transition.ID(), From: id, To: transition.Target()})
		}
	}
	identity := WorkflowIdentity{Name: definition.Version.Name, Version: definition.Version.Version, Digest: definition.Version.Digest}
	return Graph{Workflow: identity, Nodes: nodes, Edges: edges}, nil
}

// Preview derives the route that would be frozen for the supplied boundaries and context.
func (c *Catalog) Preview(ctx context.Context, name, version string, request RouteRequest, routeContext RouteContext) (RoutePreview, ValidationErrors, error) {
	definition, err := c.Definition(ctx, name, version)
	if err != nil {
		return RoutePreview{}, nil, err
	}
	route, issues := CreateRoute(definition.Document, request, routeContext)
	if len(issues) != 0 {
		return RoutePreview{}, issues, nil
	}
	identity := WorkflowIdentity{Name: definition.Version.Name, Version: definition.Version.Version, Digest: definition.Version.Digest}
	return RoutePreview{Workflow: identity, Route: route}, nil, nil
}

// PreviewProfile derives the route selected by one authored profile and merges
// its immutable input defaults with the supplied run context.
func (c *Catalog) PreviewProfile(ctx context.Context, name, version string, profile Identifier, routeContext RouteContext) (RoutePreview, ValidationErrors, error) {
	definition, err := c.Definition(ctx, name, version)
	if err != nil {
		return RoutePreview{}, nil, err
	}
	route, issues := CreateProfileRoute(definition.Document, profile, routeContext)
	if len(issues) != 0 {
		return RoutePreview{}, issues, nil
	}
	identity := WorkflowIdentity{Name: definition.Version.Name, Version: definition.Version.Version, Digest: definition.Version.Digest}
	return RoutePreview{Workflow: identity, Route: route}, nil, nil
}

// InstallConfigured validates and installs every selected configured version.
func (c *Catalog) InstallConfigured(ctx context.Context) ([]InstallResult, error) {
	definitions, err := c.Load(ctx)
	if err != nil {
		return nil, err
	}
	results := make([]InstallResult, 0, len(definitions))
	for _, definition := range definitions {
		result, err := c.install(ctx, definition)
		if err != nil {
			return nil, err
		}
		results = append(results, result)
	}
	return results, nil
}

func loadCandidate(candidate workflowstore.Candidate, validators ...valueschema.Validator) (LoadedDefinition, error) {
	if !validSourceScope(candidate.Scope) || candidate.Reference == "" {
		return LoadedDefinition{}, fmt.Errorf("workflow candidate has invalid source %q at %q", candidate.Scope, candidate.Reference)
	}
	document, canonical, digest, err := Canonicalize(candidate.Content)
	if err != nil {
		return LoadedDefinition{}, fmt.Errorf("workflow %s %q: %w", candidate.Scope, candidate.Reference, err)
	}
	if err := validateValueSchemas(document, validators...); err != nil {
		return LoadedDefinition{}, err
	}
	return LoadedDefinition{
		Document: document, CanonicalJSON: canonical, Digest: digest,
		SourceScope: candidate.Scope, SourceRef: candidate.Reference,
	}, nil
}

func (c *Catalog) install(ctx context.Context, definition LoadedDefinition) (InstallResult, error) {
	version, created, err := c.store.Install(ctx, workflowstore.InstallRequest{
		Name: definition.Document.Metadata.Name, Version: definition.Document.Metadata.Version,
		Digest: definition.Digest, Document: definition.CanonicalJSON,
		SourceScope: definition.SourceScope, SourceRef: definition.SourceRef,
		InstalledAt: c.now().UTC().Round(0),
	})
	if err != nil {
		return InstallResult{}, fmt.Errorf("install workflow %s %s: %w", definition.Document.Metadata.Name, definition.Document.Metadata.Version, err)
	}
	disposition := InstallAlreadyInstalled
	if created {
		disposition = InstallCreated
	}
	return InstallResult{Version: versionSummary(version), Disposition: disposition}, nil
}

// SnapshotRun freezes the installed workflow and complete effective
// configuration, including source attribution, for an existing run.
func (c *Catalog) SnapshotRun(ctx context.Context, runID, name, version string, effective config.Effective) (workflowstore.RunSnapshot, bool, error) {
	installed, err := c.store.InstalledVersion(ctx, name, version)
	if err != nil {
		return workflowstore.RunSnapshot{}, false, err
	}
	configSnapshot, err := json.Marshal(struct {
		Values  map[string]any           `json:"values"`
		Sources map[string]config.Source `json:"sources"`
	}{Values: effective.Values(), Sources: effective.Sources()})
	if err != nil {
		return workflowstore.RunSnapshot{}, false, fmt.Errorf("snapshot effective configuration: %w", err)
	}
	configHash := sha256.Sum256(configSnapshot)
	return c.store.CreateRunSnapshot(ctx, workflowstore.RunSnapshotRequest{
		RunID: runID, WorkflowName: installed.Name, WorkflowVersion: installed.Version,
		WorkflowDigest: installed.Digest, WorkflowDocument: installed.Document,
		ConfigDigest: hex.EncodeToString(configHash[:]), ConfigSnapshot: configSnapshot,
		CreatedAt: c.now().UTC().Round(0),
	})
}

func validSourceScope(scope workflowstore.Scope) bool { return scopePrecedence(scope) != 0 }

func scopePrecedence(scope workflowstore.Scope) int {
	switch scope {
	case workflowstore.ScopeDefault:
		return 1
	case workflowstore.ScopeUser:
		return 2
	case workflowstore.ScopeProject:
		return 3
	default:
		return 0
	}
}

func (c *Catalog) WithValueSchemaValidator(validator valueschema.Validator) *Catalog {
	c.valueSchemas = validator
	return c
}
func validateValueSchemas(document Document, validators ...valueschema.Validator) error {
	validate := func(schema, value json.RawMessage) error {
		if len(schema) == 0 {
			return nil
		}
		if len(validators) == 0 || validators[0] == nil {
			return errors.New("workflow requires a value schema validator")
		}
		return validators[0].Validate(schema, value)
	}
	for id, input := range document.Spec.Inputs {
		var value json.RawMessage
		if input.Resource != nil {
			if source, ok := input.Resource.Source.(ConstantResource); ok {
				value = source.Value
			}
		}
		if err := validate(input.SchemaDefinition, value); err != nil {
			return fmt.Errorf("input %s: %w", id, err)
		}
	}
	for id, node := range document.Spec.Nodes {
		for output, decl := range node.Fields().Outputs {
			if err := validate(decl.SchemaDefinition, nil); err != nil {
				return fmt.Errorf("node %s output %s: %w", id, output, err)
			}
		}
	}
	return nil
}
