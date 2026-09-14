package cli

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"time"

	"darkstar/src/adapters/statestore/sqlite"
	"darkstar/src/adapters/tracker/builtin"
	"darkstar/src/api"
	"darkstar/src/core/backlog"
	"darkstar/src/core/ticketexecution"
	"darkstar/src/core/ticketmanagement"
	"darkstar/src/core/trackercontract"
	"darkstar/src/core/trackerrules"
	"darkstar/src/core/workflow"
	"darkstar/src/ports"
	"darkstar/src/ports/statestore"
	"darkstar/src/ports/ticketwriter"
	"darkstar/src/ports/tracker"
	"darkstar/src/ports/worksource"
)

type daemonTrackerMapping struct {
	database  *sqlite.Database
	resolver  daemonBacklogResolver
	backlog   *backlog.Service
	workflows *workflow.Catalog
	native    *ticketmanagement.Service
	intake    *ticketexecution.Service
}

func (service *daemonAPIService) configureTrackerMapping(catalog *workflow.Catalog) error {
	native, err := ticketmanagement.New(service.database, service.database, func(ctx context.Context, project string) (ticketmanagement.Binding, error) {
		adapter, err := builtin.New(service.database, project)
		if err != nil {
			return ticketmanagement.Binding{}, err
		}
		return ticketmanagement.Binding{Source: adapter, Browser: adapter, Writer: adapter, Config: adapter.ConfigPin()}, nil
	})
	if err != nil {
		return err
	}
	engine := &daemonTrackerMapping{database: service.database, resolver: daemonBacklogResolver{native: service.database, connections: service.trackerConnectionManager}, backlog: service.backlog, workflows: catalog, native: native}
	service.trackerMapping = engine
	return service.server.SetTrackerMapping(engine)
}

func mappingFailure(code ports.FailureCode, message string) error {
	return &ports.Failure{Code: code, Message: message}
}

func mappingMissing(err error) bool {
	var failure *ports.Failure
	return errors.As(err, &failure) && failure.Code == ports.FailureNotFound
}

func mappingRevision(value statestore.TrackerMappingRevision) api.TrackerMappingRevision {
	return api.TrackerMappingRevision{Revision: value.Revision, BindingRevision: value.BindingRevision, Rules: value.RulesJSON, CreatedAt: value.CreatedAt.UTC().Format(time.RFC3339Nano)}
}

func (s *daemonTrackerMapping) History(ctx context.Context, project string) (api.TrackerMappingHistory, error) {
	binding, err := s.database.BacklogBinding(ctx, project)
	if err != nil {
		return api.TrackerMappingHistory{}, err
	}
	values, err := s.database.TrackerMappingHistory(ctx, project)
	if err != nil {
		return api.TrackerMappingHistory{}, err
	}
	result := api.TrackerMappingHistory{SchemaVersion: 1, Revisions: []api.TrackerMappingRevision{}}
	for _, value := range values {
		result.Revisions = append(result.Revisions, mappingRevision(value))
	}
	active, err := s.database.ActiveTrackerMapping(ctx, project, binding.Revision)
	if err == nil {
		result.ActiveRevision = active.Revision
	} else if !mappingMissing(err) {
		return result, err
	}
	return result, nil
}

// Discovery is reconstructed from the selected provider and installed workflow
// bytes. API callers can submit rules, never replacement capability facts.
func (s *daemonTrackerMapping) discoveryFor(ctx context.Context, project, observationID string) (trackerrules.Discovery, *trackerrules.Observation, error) {
	binding, err := s.database.BacklogBinding(ctx, project)
	if err != nil {
		return trackerrules.Discovery{}, nil, err
	}
	resolved, err := s.resolver.Resolve(ctx, binding)
	if err != nil {
		return trackerrules.Discovery{}, nil, err
	}
	manifest, err := resolved.Source.Discover(ctx, resolved.Config)
	if err != nil {
		return trackerrules.Discovery{}, nil, err
	}
	result := trackerrules.Discovery{Manifest: manifest, Fields: []trackerrules.FieldDefinition{}, Sprints: []tracker.NamedID{}, Transitions: []tracker.Transition{}, Workflows: []trackerrules.WorkflowPin{}, ReadinessPolicies: []string{"require_approval"}, Milestones: []trackerrules.MilestoneContract{}}
	// Metadata discovery is optional. An absent catalog never authorizes IDs
	// inferred from display labels or untrusted configuration.
	catalogFields := map[string]bool{}
	if metadata, ok := resolved.Source.(worksource.TrackerMetadataV1); ok {
		fields, err := metadata.DiscoverFieldCatalog(ctx, manifest.Pin)
		if err != nil {
			return result, nil, err
		}
		for _, field := range fields {
			catalogFields[field.Identity.ID] = true
			if field.Identity.ID == "sprint" {
				result.Sprints = field.Values
				continue
			}
			result.Fields = append(result.Fields, trackerrules.FieldDefinition{ID: field.Identity.ID, Values: field.Values})
		}
	}
	if observationID == "" {
		cached, err := s.database.BacklogTickets(ctx, project, binding.Revision, false, "", 1000)
		if err != nil {
			return result, nil, err
		}
		for _, value := range cached {
			if value.State != statestore.BacklogAvailable {
				continue
			}
			ticket, err := trackercontract.DecodeTicket(value.Observation.Ticket)
			if err != nil {
				return result, nil, err
			}
			addObservedMappingFields(&result, ticket, catalogFields)
			if observationID == "" {
				observationID = value.ObservationID
			}
		}
	}
	var observation *trackerrules.Observation
	if observationID != "" {
		stored, err := s.database.BacklogObservation(ctx, observationID)
		if err != nil {
			return result, nil, err
		}
		cached, err := s.database.BacklogTicket(ctx, project, binding.Revision, stored.TicketKey)
		if err != nil || cached.ObservationID != observationID || cached.State != statestore.BacklogAvailable {
			return result, nil, mappingFailure(ports.FailureConflict, "Preview ticket is not the current available observation for the selected source.")
		}
		ticket, err := trackercontract.DecodeTicket(stored.Ticket)
		if err != nil {
			return result, nil, err
		}
		observation = &trackerrules.Observation{ProjectID: project, BindingRevision: binding.Revision, Pin: manifest.Pin, Ticket: ticket, WorkflowID: tracker.Unsupported[tracker.NamedID]{Reason: "Provider workflow identity is not exposed by this source."}}
		addObservedMappingFields(&result, ticket, catalogFields)
		if observer, ok := resolved.Source.(ticketwriter.CapabilityObserverV1); ok {
			state := mappingKnownID(ticket.BusinessState)
			issueType := mappingKnownID(ticket.IssueType)
			options, err := observer.Inspect(ctx, ticketwriter.InspectRequest{Pin: manifest.Pin, Destination: manifest.Scope, Scope: tracker.TicketScope{Ref: ticket.Ref, Revision: ticket.Revision, StateID: state, IssueTypeID: issueType, Sprint: ticket.Sprint}})
			if err != nil {
				return result, nil, err
			}
			if scoped, ok := options.Scope.(tracker.TicketScope); ok && scoped.Revision != ticket.Revision {
				return result, nil, mappingFailure(ports.FailureConflict, "Ticket changed since the observation; refresh before previewing actions.")
			}
			if transitions, ok := options.Transitions.(tracker.Known[[]tracker.Transition]); ok {
				result.Transitions = transitions.Value
				for _, transition := range transitions.Value {
					if !catalogFields["state"] {
						addMappingFieldValue(&result, "state", transition.ToState)
					}
				}
			}
		}
	}
	if s.workflows != nil {
		versions, err := s.workflows.List(ctx, "")
		if err != nil {
			return result, nil, err
		}
		for _, version := range versions {
			pin := trackerrules.WorkflowPin{ID: version.Name, Version: version.Version, Digest: version.Digest}
			result.Workflows = append(result.Workflows, pin)
			definition, err := s.workflows.Definition(ctx, version.Name, version.Version)
			if err != nil {
				return result, nil, err
			}
			for nodeID, node := range definition.Document.Spec.Nodes {
				for outputName, output := range node.Fields().Outputs {
					result.Milestones = append(result.Milestones, trackerrules.MilestoneContract{ID: string(nodeID) + "." + string(outputName), Workflow: pin, EvidenceTypes: []string{string(output.Type)}})
				}
			}
		}
	}
	sort.Slice(result.Fields, func(i, j int) bool {
		return result.Fields[i].ID < result.Fields[j].ID
	})
	sort.Slice(result.Milestones, func(i, j int) bool {
		return result.Milestones[i].Workflow.ID+result.Milestones[i].ID < result.Milestones[j].Workflow.ID+result.Milestones[j].ID
	})
	return result, observation, nil
}

func mappingKnownID(value tracker.Knowledge[tracker.NamedID]) string {
	if known, ok := value.(tracker.Known[tracker.NamedID]); ok {
		return known.Value.ID
	}
	return ""
}

func addMappingFieldValue(discovery *trackerrules.Discovery, fieldID string, value tracker.NamedID) {
	for index := range discovery.Fields {
		if discovery.Fields[index].ID != fieldID {
			continue
		}
		for _, existing := range discovery.Fields[index].Values {
			if existing.ID == value.ID {
				return
			}
		}
		discovery.Fields[index].Values = append(discovery.Fields[index].Values, value)
		return
	}
	discovery.Fields = append(discovery.Fields, trackerrules.FieldDefinition{ID: fieldID, Values: []tracker.NamedID{value}})
}

func addObservedMappingFields(discovery *trackerrules.Discovery, ticket tracker.Ticket, catalogFields map[string]bool) {
	for fieldID, knowledge := range map[string]tracker.Knowledge[tracker.NamedID]{"state": ticket.BusinessState, "issue_type": ticket.IssueType, "priority": ticket.Priority} {
		if catalogFields[fieldID] {
			continue
		}
		if known, ok := knowledge.(tracker.Known[tracker.NamedID]); ok {
			addMappingFieldValue(discovery, fieldID, known.Value)
		}
	}
	for fieldID, knowledge := range map[string]tracker.Knowledge[[]tracker.NamedID]{"label": ticket.Labels, "assignee": ticket.Assignees} {
		if catalogFields[fieldID] {
			continue
		}
		if known, ok := knowledge.(tracker.Known[[]tracker.NamedID]); ok {
			for _, value := range known.Value {
				addMappingFieldValue(discovery, fieldID, value)
			}
		}
	}
}

func (s *daemonTrackerMapping) validated(ctx context.Context, project string, input api.TrackerMappingRequest) (trackerrules.RuleSet, trackerrules.Discovery, *trackerrules.Observation, error) {
	rules, err := trackerrules.Decode(input.Rules)
	if err != nil {
		return rules, trackerrules.Discovery{}, nil, mappingFailure(ports.FailureInvalidRequest, err.Error())
	}
	if rules.Scope.ProjectID != project {
		return rules, trackerrules.Discovery{}, nil, mappingFailure(ports.FailureInvalidRequest, "Rules must name this project.")
	}
	discovery, observation, err := s.discoveryFor(ctx, project, input.ObservationID)
	if err != nil {
		return rules, discovery, observation, err
	}
	if err := trackerrules.Validate(rules, discovery); err != nil {
		return rules, discovery, observation, mappingFailure(ports.FailureInvalidRequest, err.Error())
	}
	if strconv.FormatUint(rules.Scope.BindingRevision, 10) != discovery.Manifest.Pin.BindingRevision {
		return rules, discovery, observation, mappingFailure(ports.FailureConflict, "Mapping binding revision differs from the selected source pin.")
	}
	return rules, discovery, observation, nil
}

func (s *daemonTrackerMapping) Save(ctx context.Context, project string, input api.TrackerMappingRequest) (api.TrackerMappingRevision, error) {
	rules, _, _, err := s.validated(ctx, project, input)
	if err != nil {
		return api.TrackerMappingRevision{}, err
	}
	encoded, err := trackerrules.Encode(rules)
	if err != nil {
		return api.TrackerMappingRevision{}, err
	}
	record := statestore.TrackerMappingRevision{ProjectID: project, Revision: rules.Revision, BindingRevision: rules.Scope.BindingRevision, RulesJSON: encoded, CreatedAt: time.Now().UTC()}
	if err := s.database.SaveTrackerMapping(ctx, record); err != nil {
		return api.TrackerMappingRevision{}, err
	}
	stored, err := s.database.TrackerMapping(ctx, project, rules.Revision)
	return mappingRevision(stored), err
}

func (s *daemonTrackerMapping) Activate(ctx context.Context, project string, input api.TrackerMappingActivation) (api.TrackerMappingHistory, error) {
	record, err := s.database.TrackerMapping(ctx, project, input.Revision)
	if err != nil {
		return api.TrackerMappingHistory{}, err
	}
	if _, _, _, err := s.validated(ctx, project, api.TrackerMappingRequest{Rules: record.RulesJSON, ObservationID: input.ObservationID}); err != nil {
		return api.TrackerMappingHistory{}, err
	}
	if err := s.database.ActivateTrackerMapping(ctx, project, input.Revision, input.ExpectedActiveRevision, time.Now().UTC()); err != nil {
		return api.TrackerMappingHistory{}, err
	}
	return s.History(ctx, project)
}

func (s *daemonTrackerMapping) Discovery(ctx context.Context, project, observationID string) (any, error) {
	discovery, _, err := s.discoveryFor(ctx, project, observationID)
	if err != nil {
		return nil, err
	}
	binding, err := s.database.BacklogBinding(ctx, project)
	if err != nil {
		return nil, err
	}
	if strconv.FormatUint(binding.Revision, 10) != discovery.Manifest.Pin.BindingRevision {
		return nil, mappingFailure(ports.FailureConflict, "Selected source changed during discovery; reload the mapping configuration.")
	}
	history, err := s.History(ctx, project)
	if err != nil {
		return nil, err
	}
	revision := uint64(1)
	for _, prior := range history.Revisions {
		if prior.Revision >= revision {
			revision = prior.Revision + 1
		}
	}
	template := trackerrules.RuleSet{Version: trackerrules.Version, ID: "project-tracker", Revision: revision, Scope: trackerrules.RuleScope{ProjectID: project, BindingRevision: binding.Revision, Pin: discovery.Manifest.Pin, Source: discovery.Manifest.Scope}, Intake: []trackerrules.IntakeRule{}, Outbound: []trackerrules.OutboundRule{}, Display: trackerrules.DisplayMapping{Groups: []trackerrules.DisplayGroup{}, UnknownGroup: trackerrules.DisplayGroup{ID: "unmapped", Name: "Unmapped statuses", StateIDs: []string{}}}}
	for _, field := range discovery.Fields {
		if field.ID == "state" {
			for _, state := range field.Values {
				template.Display.Groups = append(template.Display.Groups, trackerrules.DisplayGroup{ID: "status-" + state.ID, Name: state.Name, StateIDs: []string{state.ID}})
			}
		}
	}
	encoded, err := json.Marshal(template)
	if err != nil {
		return nil, err
	}
	capabilities := []map[string]any{}
	for id, value := range discovery.Manifest.Capabilities {
		item := map[string]any{"id": id}
		switch value := value.(type) {
		case tracker.Known[bool]:
			item["state"], item["value"] = "known", value.Value
		case tracker.Unsupported[bool]:
			item["state"], item["reason"] = "unsupported", value.Reason
		case tracker.Unknown[bool]:
			item["state"], item["reason"] = "unknown", value.Reason
		}
		capabilities = append(capabilities, item)
	}
	transitions := []map[string]any{}
	for _, transition := range discovery.Transitions {
		action := mappingBoardAction(transition)
		transitions = append(transitions, map[string]any{"id": action.ID, "name": action.Name, "targetStateId": action.TargetStateID, "requiredFields": action.RequiredFields, "available": action.Availability == "available", "reason": action.Reason})
	}
	createReason := "The selected source does not expose an authorized ticket creation command. No native shadow ticket will be created."
	canCreate := discovery.Manifest.Scope.Namespace.Provider == "built_in" && trackercontract.Require(discovery.Manifest, tracker.Create) == nil
	if canCreate {
		createReason = ""
	}
	return map[string]any{"schemaVersion": 1, "bindingRevision": binding.Revision, "template": json.RawMessage(encoded), "fields": discovery.Fields, "sprints": discovery.Sprints, "transitions": transitions, "workflows": discovery.Workflows, "readinessPolicies": discovery.ReadinessPolicies, "milestones": discovery.Milestones, "capabilities": capabilities, "creation": map[string]any{"available": canCreate, "reason": createReason}}, nil
}

func mappingBoardAction(transition tracker.Transition) api.TrackerBoardAction {
	result := api.TrackerBoardAction{ID: transition.Identity.ID, Name: transition.Identity.Name, TargetStateID: transition.ToState.ID, Availability: "available", Automation: []string{}, RequiredFields: []string{}}
	for _, field := range transition.Fields {
		if field.Required {
			result.RequiredFields = append(result.RequiredFields, field.Identity.ID)
		}
	}
	if len(result.RequiredFields) > 0 {
		result.Availability = "unavailable"
		result.Reason = "This action requires provider fields; configure and preview them before requesting a transition."
	}
	for _, guard := range transition.Guards {
		known, ok := guard.Satisfied.(tracker.Known[bool])
		if !ok || !known.Value {
			result.Availability = "unavailable"
			result.Reason = "Provider guard is not satisfied: " + guard.Identity.Name
		}
	}
	return result
}

func (s *daemonTrackerMapping) ResolveRules(ctx context.Context, project, observationID string) (trackerrules.RuleSet, trackerrules.Discovery, error) {
	binding, err := s.database.BacklogBinding(ctx, project)
	if err != nil {
		return trackerrules.RuleSet{}, trackerrules.Discovery{}, err
	}
	record, err := s.database.ActiveTrackerMapping(ctx, project, binding.Revision)
	if err != nil {
		return trackerrules.RuleSet{}, trackerrules.Discovery{}, err
	}
	rules, discovery, _, err := s.validated(ctx, project, api.TrackerMappingRequest{Rules: record.RulesJSON, ObservationID: observationID})
	return rules, discovery, err
}

func (s *daemonTrackerMapping) DiscoverRules(ctx context.Context, project, observationID string) (trackerrules.Discovery, error) {
	discovery, _, err := s.discoveryFor(ctx, project, observationID)
	return discovery, err
}
