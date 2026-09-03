package githubcli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"darkstar/src/ports"
	"darkstar/src/ports/delivery"
)

const (
	maximumChangeRequestTitleBytes = 256
	maximumChangeRequestBodyBytes  = 1 << 20
	ownerMarkerPrefix              = "<!-- darkstar:owned-change-request:v1 owner=sha256:"
	revisionMarkerPrefix           = "<!-- darkstar:owned-change-request-content:v1 revision="
	ownedSectionEnd                = "<!-- /darkstar:owned-change-request:v1 -->"
)

type pullRequestResponse struct {
	Number   int    `json:"number"`
	HTMLURL  string `json:"html_url"`
	Title    string `json:"title"`
	Body     string `json:"body"`
	State    string `json:"state"`
	Draft    bool   `json:"draft"`
	MergedAt string `json:"merged_at"`
	Base     struct {
		Ref  string `json:"ref"`
		Repo struct {
			FullName string `json:"full_name"`
		} `json:"repo"`
	} `json:"base"`
	Head struct {
		Ref  string `json:"ref"`
		SHA  string `json:"sha"`
		Repo struct {
			FullName string `json:"full_name"`
		} `json:"repo"`
	} `json:"head"`
}

type createPullRequestPayload struct {
	Title    string `json:"title"`
	Body     string `json:"body"`
	Base     string `json:"base"`
	Head     string `json:"head"`
	HeadRepo string `json:"head_repo"`
	Draft    bool   `json:"draft"`
}

func (adapter *Adapter) FindChangeRequests(ctx context.Context, request delivery.FindChangeRequestsRequest) (delivery.ChangeRequestSearch, error) {
	if adapter == nil || adapter.runner == nil || adapter.now == nil {
		return delivery.ChangeRequestSearch{}, failure(ports.FailureInternal, "GitHub CLI adapter is not configured", false)
	}
	coordinates, err := validateChangeRequestSearch(request)
	if err != nil {
		return delivery.ChangeRequestSearch{}, err
	}
	query := url.Values{}
	query.Set("base", coordinates.Base.Name)
	query.Set("head", coordinates.Head.Repository.Owner+":"+coordinates.Head.Name)
	query.Set("per_page", "100")
	query.Set("state", "all")
	endpoint := repositoryAPIPath(coordinates.Base.Repository) + "/pulls?" + query.Encode()
	result := adapter.execute(ctx, adapter.executable, []string{
		"api", "--hostname", coordinates.Base.Repository.Host, endpoint, "--method", "GET", "--paginate",
	}, nil)
	if result.err != nil {
		return delivery.ChangeRequestSearch{}, normalizeCommandFailure(ctx, result.err)
	}
	responses := make([]pullRequestResponse, 0)
	decoder := json.NewDecoder(bytes.NewReader(result.stdout))
	decodedPage := false
	for {
		var page []pullRequestResponse
		if err := decoder.Decode(&page); err != nil {
			if err == io.EOF {
				break
			}
			return delivery.ChangeRequestSearch{}, failure(ports.FailureProtocolDrift, "GitHub pull-request search response was invalid", false)
		}
		decodedPage = true
		responses = append(responses, page...)
	}
	if !decodedPage {
		return delivery.ChangeRequestSearch{}, failure(ports.FailureProtocolDrift, "GitHub pull-request search response was empty", false)
	}
	matches := make([]delivery.ChangeRequest, 0, len(responses))
	for _, response := range responses {
		changeRequest, exact, err := translatePullRequest(response, coordinates, request.Owner)
		if err != nil {
			return delivery.ChangeRequestSearch{}, err
		}
		if exact {
			matches = append(matches, changeRequest)
		}
	}
	return delivery.ChangeRequestSearch{
		Matches: matches, ObservedAt: adapter.now().UTC(),
		EvidenceRef: "github://" + coordinates.Base.Repository.Host + "/" + coordinates.Base.Repository.Owner + "/" + coordinates.Base.Repository.Name + "/pulls?" + query.Encode(),
	}, nil
}

func (adapter *Adapter) CreateChangeRequest(ctx context.Context, request delivery.CreateChangeRequestRequest) (delivery.ChangeRequestCreation, error) {
	spec, err := validateChangeRequestCreation(request)
	if err != nil {
		return delivery.ChangeRequestCreation{}, err
	}
	normalized := spec.request
	searchRequest := delivery.FindChangeRequestsRequest{Coordinates: normalized.Coordinates, Owner: normalized.Owner}
	before, err := adapter.FindChangeRequests(ctx, searchRequest)
	if err != nil {
		return delivery.ChangeRequestCreation{}, err
	}
	if existing, found, err := reconcileChangeRequest(before, spec); err != nil {
		return delivery.ChangeRequestCreation{}, err
	} else if found {
		return adapter.changeRequestCreation(delivery.ChangeRequestReconciled{ChangeRequest: existing}), nil
	}
	if err := adapter.verifyChangeRequestBranches(ctx, normalized.Coordinates, spec.authorizedHead); err != nil {
		return delivery.ChangeRequestCreation{}, err
	}

	payload, err := json.Marshal(createPullRequestPayload{
		Title: normalized.Title, Body: spec.body, Base: normalized.Coordinates.Base.Name,
		Head:     normalized.Coordinates.Head.Repository.Owner + ":" + normalized.Coordinates.Head.Name,
		HeadRepo: normalized.Coordinates.Head.Repository.Name, Draft: spec.draft,
	})
	if err != nil {
		return delivery.ChangeRequestCreation{}, failure(ports.FailureInternal, "GitHub pull-request payload could not be encoded", false)
	}
	create := adapter.execute(ctx, adapter.executable, []string{
		"api", "--hostname", normalized.Coordinates.Base.Repository.Host,
		repositoryAPIPath(normalized.Coordinates.Base.Repository) + "/pulls", "--method", "POST", "--input", "-",
	}, payload)

	after, readErr := adapter.FindChangeRequests(ctx, searchRequest)
	if readErr != nil {
		return delivery.ChangeRequestCreation{}, failure(ports.FailureUncertain, "pull-request creation completed without a provable remote result", false)
	}
	changeRequest, found, reconcileErr := reconcileChangeRequest(after, spec)
	if reconcileErr != nil {
		return delivery.ChangeRequestCreation{}, reconcileErr
	}
	if found {
		if create.err == nil {
			return adapter.changeRequestCreation(delivery.ChangeRequestCreated{ChangeRequest: changeRequest}), nil
		}
		return adapter.changeRequestCreation(delivery.ChangeRequestReconciled{ChangeRequest: changeRequest}), nil
	}
	if create.err != nil {
		return delivery.ChangeRequestCreation{}, normalizeCommandFailure(ctx, create.err)
	}
	return delivery.ChangeRequestCreation{}, failure(ports.FailureUncertain, "GitHub accepted pull-request creation but the exact request could not be re-read", false)
}

func (adapter *Adapter) verifyChangeRequestBranches(ctx context.Context, coordinates delivery.ChangeRequestCoordinates, validatedHead string) error {
	base, err := adapter.ObserveBranch(ctx, delivery.ObserveBranchRequest{Branch: coordinates.Base})
	if err != nil {
		return err
	}
	if _, found := base.Outcome.(delivery.BranchFound); !found {
		return failure(ports.FailureNotFound, "pull-request base branch does not exist", false)
	}
	head, err := adapter.ObserveBranch(ctx, delivery.ObserveBranchRequest{Branch: coordinates.Head})
	if err != nil {
		return err
	}
	found, ok := head.Outcome.(delivery.BranchFound)
	if !ok {
		return failure(ports.FailureNotFound, "pull-request head branch does not exist", false)
	}
	if !strings.EqualFold(found.CommitSHA, validatedHead) {
		return failure(ports.FailureConflict, "pull-request head no longer matches the final validated commit", false)
	}
	return nil
}

func validateChangeRequestSearch(request delivery.FindChangeRequestsRequest) (delivery.ChangeRequestCoordinates, error) {
	coordinates := request.Coordinates
	coordinates.Base.Repository = normalizedRepository(coordinates.Base.Repository)
	coordinates.Head.Repository = normalizedRepository(coordinates.Head.Repository)
	if err := validateBranch(coordinates.Base); err != nil {
		return delivery.ChangeRequestCoordinates{}, err
	}
	if err := validateBranch(coordinates.Head); err != nil {
		return delivery.ChangeRequestCoordinates{}, err
	}
	if !strings.EqualFold(coordinates.Base.Repository.Host, coordinates.Head.Repository.Host) {
		return delivery.ChangeRequestCoordinates{}, failure(ports.FailureInvalidRequest, "pull-request base and head must use the same GitHub host", false)
	}
	if sameRepository(coordinates.Base.Repository, coordinates.Head.Repository) && coordinates.Base.Name == coordinates.Head.Name {
		return delivery.ChangeRequestCoordinates{}, failure(ports.FailureInvalidRequest, "pull-request base and head must be distinct", false)
	}
	if err := validateChangeRequestOwner(request.Owner); err != nil {
		return delivery.ChangeRequestCoordinates{}, err
	}
	return coordinates, nil
}

type changeRequestCreationSpec struct {
	request        delivery.CreateChangeRequestRequest
	body           string
	revision       string
	authorizedHead string
	draft          bool
}

func validateChangeRequestCreation(request delivery.CreateChangeRequestRequest) (changeRequestCreationSpec, error) {
	if strings.TrimSpace(request.OperationID) == "" || request.OperationID != strings.TrimSpace(request.OperationID) {
		return changeRequestCreationSpec{}, failure(ports.FailureInvalidRequest, "pull-request operation ID is required and must be trimmed", false)
	}
	coordinates, err := validateChangeRequestSearch(delivery.FindChangeRequestsRequest{Coordinates: request.Coordinates, Owner: request.Owner})
	if err != nil {
		return changeRequestCreationSpec{}, err
	}
	request.Coordinates = coordinates
	if err := validateBodyText(request.Title, "pull-request title", maximumChangeRequestTitleBytes); err != nil {
		return changeRequestCreationSpec{}, err
	}
	var content delivery.FinalChangeRequestContent
	var authorizedHead string
	var draft bool
	switch intent := request.Intent.(type) {
	case delivery.CreateFinalChangeRequest:
		content = intent.Content
		authorizedHead = strings.ToLower(strings.TrimSpace(intent.Authorization.ValidatedHeadSHA))
		if !commitPattern.MatchString(authorizedHead) {
			return changeRequestCreationSpec{}, failure(ports.FailureInvalidRequest, "final-validation authorization requires an exact head commit SHA", false)
		}
	case delivery.CreateIncrementalDraft:
		content = intent.Content
		authorizedHead = strings.ToLower(strings.TrimSpace(intent.Authorization.AcceptedHeadSHA))
		draft = true
		if !commitPattern.MatchString(authorizedHead) || strings.TrimSpace(intent.Authorization.PointID) == "" || intent.Authorization.PointID != strings.TrimSpace(intent.Authorization.PointID) || intent.Authorization.PointRevision == 0 {
			return changeRequestCreationSpec{}, failure(ports.FailureInvalidRequest, "incremental draft creation requires an accepted point, revision, and exact head commit SHA", false)
		}
	default:
		return changeRequestCreationSpec{}, failure(ports.FailureInvalidRequest, "pull-request creation intent is required", false)
	}
	if err := validateFinalContent(content, authorizedHead); err != nil {
		return changeRequestCreationSpec{}, err
	}
	if intent, ok := request.Intent.(delivery.CreateIncrementalDraft); ok && content.PointChecklist[len(content.PointChecklist)-1].ID != intent.Authorization.PointID {
		return changeRequestCreationSpec{}, failure(ports.FailureInvalidRequest, "incremental draft content must end with the authorized accepted point", false)
	}
	body := renderFinalChangeRequestBody(request.Owner, content)
	if len(body) > maximumChangeRequestBodyBytes {
		return changeRequestCreationSpec{}, failure(ports.FailureResourceExhausted, "pull-request body exceeds the adapter limit", false)
	}
	return changeRequestCreationSpec{request: request, body: body, revision: content.Revision, authorizedHead: authorizedHead, draft: draft}, nil
}

func validateFinalContent(content delivery.FinalChangeRequestContent, validatedHead string) error {
	if err := validateBodyText(content.Revision, "content revision", 256); err != nil {
		return err
	}
	if err := validateBodyText(content.Outcome, "outcome", 4096); err != nil {
		return err
	}
	if len(content.Scope) == 0 || len(content.ArtifactLinks) == 0 || len(content.PointChecklist) == 0 || len(content.Commits) == 0 || len(content.Evidence) == 0 {
		return failure(ports.FailureInvalidRequest, "final pull request requires scope, artifact links, points, commits, and evidence", false)
	}
	for index, item := range content.Scope {
		if err := validateBodyText(item, fmt.Sprintf("scope item %d", index+1), 4096); err != nil {
			return err
		}
	}
	for index, item := range content.ArtifactLinks {
		if err := validateLink(item.Label, item.URL, fmt.Sprintf("artifact link %d", index+1)); err != nil {
			return err
		}
	}
	for index, point := range content.PointChecklist {
		if err := validateBodyText(point.ID, fmt.Sprintf("point %d identity", index+1), 256); err != nil {
			return err
		}
		if err := validateBodyText(point.Summary, fmt.Sprintf("point %d summary", index+1), 4096); err != nil {
			return err
		}
	}
	for index, commit := range content.Commits {
		if !commitPattern.MatchString(strings.TrimSpace(commit.SHA)) {
			return failure(ports.FailureInvalidRequest, fmt.Sprintf("commit %d requires an exact SHA", index+1), false)
		}
		if err := validateBodyText(commit.Summary, fmt.Sprintf("commit %d summary", index+1), 4096); err != nil {
			return err
		}
	}
	if !strings.EqualFold(strings.TrimSpace(content.Commits[len(content.Commits)-1].SHA), validatedHead) {
		return failure(ports.FailureInvalidRequest, "the final listed commit must be the validated head commit", false)
	}
	if err := validateBodyText(content.RiskRollback.Risk, "risk", 8192); err != nil {
		return err
	}
	if err := validateBodyText(content.RiskRollback.Rollback, "rollback", 8192); err != nil {
		return err
	}
	for index, evidence := range content.Evidence {
		if err := validateLink(evidence.Label, evidence.URL, fmt.Sprintf("evidence link %d", index+1)); err != nil {
			return err
		}
		if err := validateBodyText(evidence.Summary, fmt.Sprintf("evidence %d summary", index+1), 4096); err != nil {
			return err
		}
	}
	return nil
}

func validateLink(label, value, name string) error {
	if err := validateBodyText(label, name+" label", 1024); err != nil {
		return err
	}
	parsed, err := url.ParseRequestURI(strings.TrimSpace(value))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") || value != strings.TrimSpace(value) || strings.ContainsAny(value, "()<> \t\r\n") {
		return failure(ports.FailureInvalidRequest, name+" must be an absolute HTTP(S) URL", false)
	}
	return nil
}

func validateBodyText(value, name string, maximumBytes int) error {
	if strings.TrimSpace(value) == "" || value != strings.TrimSpace(value) || !utf8.ValidString(value) || len(value) > maximumBytes || strings.ContainsAny(value, "\r\n\x00") || strings.Contains(value, "<!-- darkstar:") {
		return failure(ports.FailureInvalidRequest, name+" is invalid", false)
	}
	return nil
}

func validateChangeRequestOwner(owner delivery.ChangeRequestOwner) error {
	if err := validateBodyText(owner.DeliveryLineID, "change-request delivery-line identity", 256); err != nil {
		return err
	}
	if err := validateBodyText(owner.WorkItemID, "change-request work-item identity", 256); err != nil {
		return err
	}
	return nil
}

func translatePullRequest(response pullRequestResponse, expected delivery.ChangeRequestCoordinates, owner delivery.ChangeRequestOwner) (delivery.ChangeRequest, bool, error) {
	if response.Number <= 0 || strings.TrimSpace(response.HTMLURL) == "" {
		return delivery.ChangeRequest{}, false, failure(ports.FailureProtocolDrift, "GitHub pull-request response was incomplete", false)
	}
	baseRepository, err := repositoryFromFullName(expected.Base.Repository.Host, response.Base.Repo.FullName)
	if err != nil {
		return delivery.ChangeRequest{}, false, err
	}
	headRepository, err := repositoryFromFullName(expected.Head.Repository.Host, response.Head.Repo.FullName)
	if err != nil {
		return delivery.ChangeRequest{}, false, err
	}
	if !sameRepository(baseRepository, expected.Base.Repository) || response.Base.Ref != expected.Base.Name || !sameRepository(headRepository, expected.Head.Repository) || response.Head.Ref != expected.Head.Name {
		return delivery.ChangeRequest{}, false, nil
	}
	state, err := translatePullRequestState(response)
	if err != nil {
		return delivery.ChangeRequest{}, false, err
	}
	ownership := delivery.ChangeRequestOwnership(delivery.UnownedChangeRequest{})
	if revision, ok := ownedRevision(response.Body, owner); ok {
		ownership = delivery.OwnedChangeRequest{Owner: owner, Revision: revision}
	} else if _, err := locateOwnedSection(response.Body); err != nil && strings.Contains(response.Body, "darkstar:owned-change-request") {
		ownership = delivery.MalformedChangeRequestOwnership{Reason: "DARKSTAR ownership markers are malformed or duplicated"}
	}
	coordinates := delivery.ChangeRequestCoordinates{
		Base: delivery.BranchRef{Repository: baseRepository, Name: response.Base.Ref},
		Head: delivery.BranchRef{Repository: headRepository, Name: response.Head.Ref},
	}
	return delivery.ChangeRequest{
		Ref:         delivery.ChangeRequestRef{Repository: baseRepository, ID: strconv.Itoa(response.Number)},
		Coordinates: coordinates, URL: response.HTMLURL, Title: response.Title, Body: response.Body,
		State: state, Ownership: ownership,
	}, true, nil
}

func translatePullRequestState(response pullRequestResponse) (delivery.ChangeRequestState, error) {
	if strings.TrimSpace(response.MergedAt) != "" {
		return delivery.MergedState{}, nil
	}
	switch strings.ToLower(strings.TrimSpace(response.State)) {
	case "open":
		if response.Draft {
			return delivery.DraftState{}, nil
		}
		return delivery.OpenState{}, nil
	case "closed":
		return delivery.ClosedState{}, nil
	default:
		return nil, failure(ports.FailureProtocolDrift, "GitHub pull-request state was unknown", false)
	}
}

func repositoryFromFullName(host, fullName string) (delivery.Repository, error) {
	owner, name, ok := strings.Cut(strings.TrimSpace(fullName), "/")
	if !ok || strings.Contains(name, "/") {
		return delivery.Repository{}, failure(ports.FailureProtocolDrift, "GitHub pull-request repository was invalid", false)
	}
	repository := delivery.Repository{Provider: Provider, Host: host, Owner: owner, Name: name}
	if err := validateRepository(repository); err != nil {
		return delivery.Repository{}, failure(ports.FailureProtocolDrift, "GitHub pull-request repository was invalid", false)
	}
	return normalizedRepository(repository), nil
}

func reconcileChangeRequest(search delivery.ChangeRequestSearch, spec changeRequestCreationSpec) (delivery.ChangeRequest, bool, error) {
	if len(search.Matches) > 1 {
		return delivery.ChangeRequest{}, false, failure(ports.FailureConflict, "multiple pull requests match the exact base and head coordinates", false)
	}
	if len(search.Matches) == 0 {
		return delivery.ChangeRequest{}, false, nil
	}
	existing := search.Matches[0]
	request := spec.request
	owned, ok := existing.Ownership.(delivery.OwnedChangeRequest)
	if !ok || owned.Owner != request.Owner {
		return delivery.ChangeRequest{}, false, failure(ports.FailureConflict, "an unowned pull request already uses the exact base and head coordinates", false)
	}
	if spec.draft {
		if _, ok := existing.State.(delivery.DraftState); !ok {
			return delivery.ChangeRequest{}, false, failure(ports.FailureConflict, "the owned matching pull request is not the requested incremental draft", false)
		}
	} else {
		switch existing.State.(type) {
		case delivery.DraftState:
			return delivery.ChangeRequest{}, false, failure(ports.FailureConflict, "the owned matching pull request is draft and must be finalized through an authorized update", false)
		case delivery.ClosedState:
			return delivery.ChangeRequest{}, false, failure(ports.FailureConflict, "the owned matching pull request was closed without merge and requires operator action", false)
		case delivery.OpenState, delivery.MergedState:
			// An open or already-merged owned request is the single prior effect.
		default:
			return delivery.ChangeRequest{}, false, failure(ports.FailureProtocolDrift, "the owned pull request has an unknown lifecycle state", false)
		}
	}
	span, parseErr := locateOwnedSection(existing.Body)
	if parseErr != nil || existing.Title != request.Title || existing.Body[span.start:span.end] != spec.body || owned.Revision != spec.revision {
		return delivery.ChangeRequest{}, false, failure(ports.FailureConflict, "the owned pull request does not match the requested final content", false)
	}
	return existing, true, nil
}

func (adapter *Adapter) changeRequestCreation(outcome delivery.ChangeRequestCreationOutcome) delivery.ChangeRequestCreation {
	var evidence string
	switch value := outcome.(type) {
	case delivery.ChangeRequestCreated:
		evidence = value.ChangeRequest.URL
	case delivery.ChangeRequestReconciled:
		evidence = value.ChangeRequest.URL
	}
	return delivery.ChangeRequestCreation{Outcome: outcome, ObservedAt: adapter.now().UTC(), EvidenceRef: evidence}
}

func renderFinalChangeRequestBody(owner delivery.ChangeRequestOwner, content delivery.FinalChangeRequestContent) string {
	return renderOwnedSection(owner, delivery.OwnedSection{Revision: content.Revision, Body: renderFinalChangeRequestContent(content)})
}

func renderFinalChangeRequestContent(content delivery.FinalChangeRequestContent) string {
	var body strings.Builder
	body.WriteString("## Outcome\n\n")
	body.WriteString(markdownText(content.Outcome))
	body.WriteString("\n\n## Scope\n\n")
	for _, item := range content.Scope {
		fmt.Fprintf(&body, "- %s\n", markdownText(item))
	}
	body.WriteString("\n## Artifacts\n\n")
	for _, artifact := range content.ArtifactLinks {
		fmt.Fprintf(&body, "- [%s](%s)\n", markdownText(artifact.Label), artifact.URL)
	}
	body.WriteString("\n## Implementation points\n\n")
	for _, point := range content.PointChecklist {
		fmt.Fprintf(&body, "- [x] `%s` — %s\n", markdownCode(point.ID), markdownText(point.Summary))
	}
	body.WriteString("\n## Commits\n\n")
	for _, commit := range content.Commits {
		fmt.Fprintf(&body, "- `%s` — %s\n", strings.ToLower(commit.SHA), markdownText(commit.Summary))
	}
	body.WriteString("\n## Risk and rollback\n\n")
	fmt.Fprintf(&body, "**Risk:** %s\n\n**Rollback:** %s\n", markdownText(content.RiskRollback.Risk), markdownText(content.RiskRollback.Rollback))
	body.WriteString("\n## Validation evidence\n\n")
	for _, evidence := range content.Evidence {
		fmt.Fprintf(&body, "- [%s](%s) — %s\n", markdownText(evidence.Label), evidence.URL, markdownText(evidence.Summary))
	}
	return strings.TrimSpace(body.String())
}

func renderOwnedSection(owner delivery.ChangeRequestOwner, section delivery.OwnedSection) string {
	return ownerMarker(owner) + "\n" + revisionMarker(section.Revision) + "\n\n" + strings.TrimSpace(section.Body) + "\n\n" + ownedSectionEnd
}

func ownerMarker(owner delivery.ChangeRequestOwner) string {
	identity := fmt.Sprintf("%d:%s%d:%s", len(owner.DeliveryLineID), owner.DeliveryLineID, len(owner.WorkItemID), owner.WorkItemID)
	digest := sha256.Sum256([]byte(identity))
	return ownerMarkerPrefix + hex.EncodeToString(digest[:]) + " -->"
}

func revisionMarker(revision string) string {
	return revisionMarkerPrefix + base64.RawURLEncoding.EncodeToString([]byte(revision)) + " -->"
}

type ownedSectionSpan struct {
	start     int
	end       int
	ownerLine string
	revision  string
}

func locateOwnedSection(body string) (ownedSectionSpan, error) {
	type markerLine struct {
		line  string
		start int
		end   int
		index int
	}
	lines := strings.Split(body, "\n")
	offset := 0
	owners := make([]markerLine, 0, 1)
	revisions := make([]markerLine, 0, 1)
	ends := make([]markerLine, 0, 1)
	markerLines := 0
	for index, line := range lines {
		trimmed := strings.TrimSuffix(line, "\r")
		entry := markerLine{line: trimmed, start: offset, end: offset + len(trimmed), index: index}
		if strings.Contains(trimmed, "darkstar:owned-change-request") {
			markerLines++
			switch {
			case strings.HasPrefix(trimmed, ownerMarkerPrefix):
				owners = append(owners, entry)
			case strings.HasPrefix(trimmed, revisionMarkerPrefix):
				revisions = append(revisions, entry)
			case trimmed == ownedSectionEnd:
				ends = append(ends, entry)
			}
		}
		offset += len(line)
		if index < len(lines)-1 {
			offset++
		}
	}
	if markerLines != 3 || len(owners) != 1 || len(revisions) != 1 || len(ends) != 1 {
		return ownedSectionSpan{}, fmt.Errorf("owned section requires exactly one owner, revision, and end marker")
	}
	owner := owners[0]
	revision := revisions[0]
	end := ends[0]
	digest := strings.TrimSuffix(strings.TrimPrefix(owner.line, ownerMarkerPrefix), " -->")
	decodedDigest, digestErr := hex.DecodeString(digest)
	if !strings.HasSuffix(owner.line, " -->") || len(decodedDigest) != sha256.Size || digestErr != nil || revision.index != owner.index+1 || end.index <= revision.index {
		return ownedSectionSpan{}, fmt.Errorf("owned section marker structure is invalid")
	}
	encoded := strings.TrimSuffix(strings.TrimPrefix(revision.line, revisionMarkerPrefix), " -->")
	decodedRevision, revisionErr := base64.RawURLEncoding.DecodeString(encoded)
	if !strings.HasSuffix(revision.line, " -->") || revisionErr != nil || validateBodyText(string(decodedRevision), "owned content revision", 256) != nil {
		return ownedSectionSpan{}, fmt.Errorf("owned section revision marker is invalid")
	}
	return ownedSectionSpan{start: owner.start, end: end.end, ownerLine: owner.line, revision: string(decodedRevision)}, nil
}

func ownedRevision(body string, owner delivery.ChangeRequestOwner) (string, bool) {
	span, err := locateOwnedSection(body)
	if err != nil || span.ownerLine != ownerMarker(owner) {
		return "", false
	}
	return span.revision, true
}

func markdownText(value string) string {
	return strings.NewReplacer("\\", "\\\\", "`", "\\`", "*", "\\*", "_", "\\_", "[", "\\[", "]", "\\]", "<", "&lt;", ">", "&gt;").Replace(value)
}

func markdownCode(value string) string {
	return strings.ReplaceAll(value, "`", "\\`")
}
