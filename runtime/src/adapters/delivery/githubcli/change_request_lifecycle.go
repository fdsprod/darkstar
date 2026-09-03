package githubcli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"darkstar/src/ports"
	"darkstar/src/ports/delivery"
)

type remoteChangeRequest struct {
	changeRequest delivery.ChangeRequest
	response      pullRequestResponse
}

func (adapter *Adapter) UpdateChangeRequest(ctx context.Context, request delivery.UpdateChangeRequestRequest) (delivery.ChangeRequestUpdate, error) {
	spec, err := validateChangeRequestUpdate(request)
	if err != nil {
		return delivery.ChangeRequestUpdate{}, err
	}
	normalized := spec.request
	current, found, err := adapter.readChangeRequest(ctx, normalized.Ref, normalized.Owner)
	if err != nil {
		return delivery.ChangeRequestUpdate{}, err
	}
	if !found {
		return delivery.ChangeRequestUpdate{}, failure(ports.FailureNotFound, "pull request does not exist", false)
	}
	desiredTitle := current.changeRequest.Title
	switch edit := spec.title.(type) {
	case delivery.KeepTitle:
	case delivery.ReplaceTitle:
		desiredTitle = edit.Title
	}
	span, err := requireOwnedSection(current.changeRequest, normalized.Owner)
	if err != nil {
		return delivery.ChangeRequestUpdate{}, err
	}
	desiredBody := current.changeRequest.Body[:span.start] + spec.desiredSection + current.changeRequest.Body[span.end:]
	if spec.authorizedHead != "" {
		remoteHead := strings.ToLower(strings.TrimSpace(current.response.Head.SHA))
		if !commitPattern.MatchString(remoteHead) {
			return delivery.ChangeRequestUpdate{}, failure(ports.FailureProtocolDrift, "GitHub pull-request response did not contain the head commit SHA", false)
		}
		if !strings.EqualFold(remoteHead, spec.authorizedHead) {
			return delivery.ChangeRequestUpdate{}, failure(ports.FailureConflict, "pull-request head no longer matches the authorized commit", false)
		}
	}
	if err := requireUpdateModeState(current.changeRequest.State, spec.mode); err != nil {
		return delivery.ChangeRequestUpdate{}, err
	}

	mutated := false
	reconciled := false
	if current.changeRequest.Title != desiredTitle || current.changeRequest.Body != desiredBody {
		payload := map[string]string{"title": desiredTitle, "body": desiredBody}
		encoded, marshalErr := json.Marshal(payload)
		if marshalErr != nil {
			return delivery.ChangeRequestUpdate{}, failure(ports.FailureInternal, "GitHub pull-request update payload could not be encoded", false)
		}
		patch := adapter.execute(ctx, adapter.executable, []string{
			"api", "--hostname", normalized.Ref.Repository.Host,
			repositoryAPIPath(normalized.Ref.Repository) + "/pulls/" + normalized.Ref.ID,
			"--method", "PATCH", "--input", "-",
		}, encoded)
		mutated = true
		after, afterFound, readErr := adapter.readChangeRequest(ctx, normalized.Ref, normalized.Owner)
		if readErr != nil || !afterFound {
			return delivery.ChangeRequestUpdate{}, failure(ports.FailureUncertain, "pull-request update completed without a provable remote result", false)
		}
		if after.changeRequest.Title != desiredTitle || after.changeRequest.Body != desiredBody {
			if patch.err != nil {
				return delivery.ChangeRequestUpdate{}, normalizeCommandFailure(ctx, patch.err)
			}
			return delivery.ChangeRequestUpdate{}, failure(ports.FailureUncertain, "GitHub accepted the pull-request update but the requested content was not observed", false)
		}
		if _, ownErr := requireOwnedSection(after.changeRequest, normalized.Owner); ownErr != nil {
			return delivery.ChangeRequestUpdate{}, ownErr
		}
		if patch.err != nil {
			reconciled = true
		}
		current = after
	}

	if spec.mode == updateFinalizeDraft {
		switch current.changeRequest.State.(type) {
		case delivery.DraftState:
			ready := adapter.execute(ctx, adapter.executable, []string{
				"pr", "ready", normalized.Ref.ID, "--repo", pullRequestRepositorySelector(normalized.Ref.Repository),
			}, nil)
			mutated = true
			after, afterFound, readErr := adapter.readChangeRequest(ctx, normalized.Ref, normalized.Owner)
			if readErr != nil || !afterFound {
				return delivery.ChangeRequestUpdate{}, failure(ports.FailureUncertain, "ready-for-review update completed without a provable remote result", false)
			}
			if _, open := after.changeRequest.State.(delivery.OpenState); !open {
				if ready.err != nil {
					return delivery.ChangeRequestUpdate{}, normalizeCommandFailure(ctx, ready.err)
				}
				return delivery.ChangeRequestUpdate{}, failure(ports.FailureUncertain, "GitHub accepted ready-for-review but the pull request is still draft", false)
			}
			if after.changeRequest.Title != desiredTitle || after.changeRequest.Body != desiredBody {
				return delivery.ChangeRequestUpdate{}, failure(ports.FailureConflict, "pull-request content changed while marking the draft ready", false)
			}
			if ready.err != nil {
				reconciled = true
			}
			current = after
		case delivery.OpenState:
			// A retry after the exact owned draft was already marked ready is a no-op.
		default:
			return delivery.ChangeRequestUpdate{}, failure(ports.FailureConflict, "only an open owned draft can be marked ready", false)
		}
	}

	var outcome delivery.ChangeRequestUpdateOutcome
	switch {
	case reconciled:
		outcome = delivery.ChangeRequestUpdateReconciled{ChangeRequest: current.changeRequest}
	case mutated:
		outcome = delivery.ChangeRequestUpdated{ChangeRequest: current.changeRequest}
	default:
		outcome = delivery.ChangeRequestUnchanged{ChangeRequest: current.changeRequest}
	}
	return delivery.ChangeRequestUpdate{Outcome: outcome, ObservedAt: adapter.now().UTC(), EvidenceRef: current.changeRequest.URL}, nil
}

type changeRequestUpdateMode uint8

const (
	updateOwnedOpen changeRequestUpdateMode = iota + 1
	updateAcceptedDraft
	updateFinalizeDraft
)

type changeRequestUpdateSpec struct {
	request        delivery.UpdateChangeRequestRequest
	title          delivery.TitleEdit
	desiredSection string
	authorizedHead string
	mode           changeRequestUpdateMode
}

func validateChangeRequestUpdate(request delivery.UpdateChangeRequestRequest) (changeRequestUpdateSpec, error) {
	if strings.TrimSpace(request.OperationID) == "" || request.OperationID != strings.TrimSpace(request.OperationID) {
		return changeRequestUpdateSpec{}, failure(ports.FailureInvalidRequest, "pull-request update operation ID is required and must be trimmed", false)
	}
	ref, err := validateChangeRequestRef(request.Ref)
	if err != nil {
		return changeRequestUpdateSpec{}, err
	}
	request.Ref = ref
	if err := validateChangeRequestOwner(request.Owner); err != nil {
		return changeRequestUpdateSpec{}, err
	}
	var title delivery.TitleEdit
	var section delivery.OwnedSection
	var content delivery.FinalChangeRequestContent
	var structuredContent bool
	var authorizedHead string
	var mode changeRequestUpdateMode
	switch intent := request.Intent.(type) {
	case delivery.UpdateOwnedChangeRequest:
		title, section, mode = intent.Title, intent.OwnedSection, updateOwnedOpen
	case delivery.UpdateIncrementalDraft:
		title, content, structuredContent, mode = intent.Title, intent.Content, true, updateAcceptedDraft
		authorizedHead = strings.ToLower(strings.TrimSpace(intent.Authorization.AcceptedHeadSHA))
		if !commitPattern.MatchString(authorizedHead) || strings.TrimSpace(intent.Authorization.PointID) == "" || intent.Authorization.PointID != strings.TrimSpace(intent.Authorization.PointID) || intent.Authorization.PointRevision == 0 {
			return changeRequestUpdateSpec{}, failure(ports.FailureInvalidRequest, "incremental draft update requires an accepted point, revision, and exact head commit SHA", false)
		}
	case delivery.FinalizeIncrementalDraft:
		title, content, structuredContent, mode = intent.Title, intent.Content, true, updateFinalizeDraft
		authorizedHead = strings.ToLower(strings.TrimSpace(intent.Authorization.ValidatedHeadSHA))
		if !commitPattern.MatchString(authorizedHead) {
			return changeRequestUpdateSpec{}, failure(ports.FailureInvalidRequest, "draft finalization requires final-validation authorization for an exact head commit SHA", false)
		}
	default:
		return changeRequestUpdateSpec{}, failure(ports.FailureInvalidRequest, "pull-request update intent is required", false)
	}
	switch edit := title.(type) {
	case delivery.KeepTitle:
	case delivery.ReplaceTitle:
		if err := validateBodyText(edit.Title, "pull-request title", maximumChangeRequestTitleBytes); err != nil {
			return changeRequestUpdateSpec{}, err
		}
	default:
		return changeRequestUpdateSpec{}, failure(ports.FailureInvalidRequest, "pull-request title edit is required", false)
	}
	var desiredSection string
	if structuredContent {
		if err := validateFinalContent(content, authorizedHead); err != nil {
			return changeRequestUpdateSpec{}, err
		}
		if intent, ok := request.Intent.(delivery.UpdateIncrementalDraft); ok && content.PointChecklist[len(content.PointChecklist)-1].ID != intent.Authorization.PointID {
			return changeRequestUpdateSpec{}, failure(ports.FailureInvalidRequest, "incremental draft content must end with the authorized accepted point", false)
		}
		desiredSection = renderFinalChangeRequestBody(request.Owner, content)
	} else {
		if err := validateBodyText(section.Revision, "owned content revision", 256); err != nil {
			return changeRequestUpdateSpec{}, err
		}
		body := section.Body
		if strings.TrimSpace(body) == "" || !utf8.ValidString(body) || len(body) > maximumChangeRequestBodyBytes || strings.ContainsAny(body, "\r\x00") || strings.Contains(body, "<!-- darkstar:") {
			return changeRequestUpdateSpec{}, failure(ports.FailureInvalidRequest, "owned content body is invalid", false)
		}
		section.Body = strings.TrimSpace(body)
		desiredSection = renderOwnedSection(request.Owner, section)
	}
	if len(desiredSection) > maximumChangeRequestBodyBytes {
		return changeRequestUpdateSpec{}, failure(ports.FailureResourceExhausted, "owned content exceeds the adapter limit", false)
	}
	return changeRequestUpdateSpec{request: request, title: title, desiredSection: desiredSection, authorizedHead: authorizedHead, mode: mode}, nil
}

func requireUpdateModeState(state delivery.ChangeRequestState, mode changeRequestUpdateMode) error {
	switch mode {
	case updateOwnedOpen:
		if _, ok := state.(delivery.OpenState); ok {
			return nil
		}
		return failure(ports.FailureConflict, "ordinary owned-content updates require an open non-draft pull request", false)
	case updateAcceptedDraft:
		if _, ok := state.(delivery.DraftState); ok {
			return nil
		}
		return failure(ports.FailureConflict, "accepted-point updates require the owned incremental draft", false)
	case updateFinalizeDraft:
		switch state.(type) {
		case delivery.DraftState, delivery.OpenState:
			return nil
		default:
			return failure(ports.FailureConflict, "draft finalization requires the owned draft or its already-ready retry state", false)
		}
	default:
		return failure(ports.FailureProtocolDrift, "pull-request update mode is unknown", false)
	}
}

func requireOwnedSection(changeRequest delivery.ChangeRequest, owner delivery.ChangeRequestOwner) (ownedSectionSpan, error) {
	switch ownership := changeRequest.Ownership.(type) {
	case delivery.OwnedChangeRequest:
		if ownership.Owner != owner {
			return ownedSectionSpan{}, failure(ports.FailureConflict, "pull request is owned by a different delivery lifecycle", false)
		}
	case delivery.MalformedChangeRequestOwnership:
		return ownedSectionSpan{}, failure(ports.FailureConflict, ownership.Reason, false)
	default:
		return ownedSectionSpan{}, failure(ports.FailureConflict, "pull request does not contain the expected DARKSTAR-owned section", false)
	}
	span, err := locateOwnedSection(changeRequest.Body)
	if err != nil || span.ownerLine != ownerMarker(owner) {
		return ownedSectionSpan{}, failure(ports.FailureConflict, "pull request has missing, duplicate, malformed, or mismatched DARKSTAR ownership markers", false)
	}
	return span, nil
}

func (adapter *Adapter) ObserveChangeRequest(ctx context.Context, request delivery.ObserveChangeRequestRequest) (delivery.ChangeRequestObservation, error) {
	if adapter == nil || adapter.runner == nil || adapter.now == nil {
		return delivery.ChangeRequestObservation{}, failure(ports.FailureInternal, "GitHub CLI adapter is not configured", false)
	}
	ref, err := validateChangeRequestRef(request.Ref)
	if err != nil {
		return delivery.ChangeRequestObservation{}, err
	}
	if err := validateChangeRequestOwner(request.Owner); err != nil {
		return delivery.ChangeRequestObservation{}, err
	}
	remote, found, err := adapter.readChangeRequest(ctx, ref, request.Owner)
	if err != nil {
		return delivery.ChangeRequestObservation{}, err
	}
	if !found {
		return delivery.ChangeRequestObservation{Outcome: delivery.ChangeRequestMissing{}, ObservedAt: adapter.now().UTC(), EvidenceRef: changeRequestEvidenceRef(ref)}, nil
	}
	checks, err := adapter.observeRequiredChecks(ctx, ref)
	if err != nil {
		return delivery.ChangeRequestObservation{}, err
	}
	review, err := adapter.observeRequiredReview(ctx, ref, remote.changeRequest.URL)
	if err != nil {
		return delivery.ChangeRequestObservation{}, err
	}
	return delivery.ChangeRequestObservation{
		Outcome:    delivery.ChangeRequestFound{ChangeRequest: remote.changeRequest, Checks: checks, Review: review},
		ObservedAt: adapter.now().UTC(), EvidenceRef: remote.changeRequest.URL,
	}, nil
}

func (adapter *Adapter) readChangeRequest(ctx context.Context, ref delivery.ChangeRequestRef, owner delivery.ChangeRequestOwner) (remoteChangeRequest, bool, error) {
	ref, err := validateChangeRequestRef(ref)
	if err != nil {
		return remoteChangeRequest{}, false, err
	}
	result := adapter.execute(ctx, adapter.executable, []string{
		"api", "--hostname", ref.Repository.Host, repositoryAPIPath(ref.Repository) + "/pulls/" + ref.ID, "--method", "GET",
	}, nil)
	if result.err != nil {
		if commandReportsNotFound(result.stderr) {
			return remoteChangeRequest{}, false, nil
		}
		return remoteChangeRequest{}, false, normalizeCommandFailure(ctx, result.err)
	}
	var response pullRequestResponse
	decoder := json.NewDecoder(bytes.NewReader(result.stdout))
	if err := decoder.Decode(&response); err != nil {
		return remoteChangeRequest{}, false, failure(ports.FailureProtocolDrift, "GitHub pull-request response was invalid", false)
	}
	wantID, _ := strconv.Atoi(ref.ID)
	if response.Number != wantID {
		return remoteChangeRequest{}, false, failure(ports.FailureProtocolDrift, "GitHub pull-request response did not match the requested identity", false)
	}
	baseRepository, err := repositoryFromFullName(ref.Repository.Host, response.Base.Repo.FullName)
	if err != nil || !sameRepository(baseRepository, ref.Repository) {
		return remoteChangeRequest{}, false, failure(ports.FailureProtocolDrift, "GitHub pull-request response did not match the requested repository", false)
	}
	headRepository, err := repositoryFromFullName(ref.Repository.Host, response.Head.Repo.FullName)
	if err != nil {
		return remoteChangeRequest{}, false, err
	}
	expected := delivery.ChangeRequestCoordinates{
		Base: delivery.BranchRef{Repository: baseRepository, Name: response.Base.Ref},
		Head: delivery.BranchRef{Repository: headRepository, Name: response.Head.Ref},
	}
	changeRequest, exact, err := translatePullRequest(response, expected, owner)
	if err != nil || !exact {
		if err != nil {
			return remoteChangeRequest{}, false, err
		}
		return remoteChangeRequest{}, false, failure(ports.FailureProtocolDrift, "GitHub pull-request coordinates were inconsistent", false)
	}
	return remoteChangeRequest{changeRequest: changeRequest, response: response}, true, nil
}

func validateChangeRequestRef(ref delivery.ChangeRequestRef) (delivery.ChangeRequestRef, error) {
	ref.Repository = normalizedRepository(ref.Repository)
	if err := validateRepository(ref.Repository); err != nil {
		return delivery.ChangeRequestRef{}, err
	}
	id, err := strconv.Atoi(ref.ID)
	if err != nil || id <= 0 || strconv.Itoa(id) != ref.ID {
		return delivery.ChangeRequestRef{}, failure(ports.FailureInvalidRequest, "pull-request identity must be a positive canonical integer", false)
	}
	return ref, nil
}

func changeRequestEvidenceRef(ref delivery.ChangeRequestRef) string {
	repository := normalizedRepository(ref.Repository)
	return fmt.Sprintf("github://%s/%s/%s/pulls/%s", repository.Host, repository.Owner, repository.Name, ref.ID)
}

func pullRequestRepositorySelector(repository delivery.Repository) string {
	repository = normalizedRepository(repository)
	selector := repository.Owner + "/" + repository.Name
	if !strings.EqualFold(repository.Host, "github.com") {
		return repository.Host + "/" + selector
	}
	return selector
}

type checkResponse struct {
	Name        string `json:"name"`
	State       string `json:"state"`
	Description string `json:"description"`
	Link        string `json:"link"`
	Bucket      string `json:"bucket"`
}

func (adapter *Adapter) observeRequiredChecks(ctx context.Context, ref delivery.ChangeRequestRef) (delivery.RequiredChecksOutcome, error) {
	result := adapter.execute(ctx, adapter.executable, []string{
		"pr", "checks", ref.ID, "--repo", pullRequestRepositorySelector(ref.Repository),
		"--required", "--json", "name,state,description,link,bucket",
	}, nil)
	var responses []checkResponse
	if err := json.Unmarshal(result.stdout, &responses); err != nil {
		if result.err != nil && commandReportsNoRequiredChecks(result.stderr) {
			return delivery.RequiredChecksNotConfigured{}, nil
		}
		if result.err != nil {
			return nil, normalizeCommandFailure(ctx, result.err)
		}
		return nil, failure(ports.FailureProtocolDrift, "GitHub required-check response was invalid", false)
	}
	if len(responses) == 0 {
		return delivery.RequiredChecksNotConfigured{}, nil
	}
	failing := make([]delivery.RequiredCheckDetail, 0)
	pending := make([]delivery.RequiredCheckDetail, 0)
	for _, response := range responses {
		if strings.TrimSpace(response.Name) == "" || strings.TrimSpace(response.State) == "" {
			return nil, failure(ports.FailureProtocolDrift, "GitHub required-check response was incomplete", false)
		}
		detail := delivery.RequiredCheckDetail{Name: response.Name, State: response.State, Summary: response.Description, EvidenceRef: response.Link}
		switch strings.ToLower(strings.TrimSpace(response.Bucket)) {
		case "pass", "skipping":
		case "pending":
			pending = append(pending, detail)
		case "fail", "cancel":
			failing = append(failing, detail)
		default:
			return nil, failure(ports.FailureProtocolDrift, "GitHub required-check response contained an unknown outcome", false)
		}
	}
	sortCheckDetails(failing)
	sortCheckDetails(pending)
	if len(failing) > 0 {
		return delivery.RequiredChecksFailed{Checks: failing}, nil
	}
	if len(pending) > 0 {
		return delivery.RequiredChecksPending{Checks: pending}, nil
	}
	return delivery.RequiredChecksSuccessful{}, nil
}

func commandReportsNoRequiredChecks(stderr []byte) bool {
	message := strings.ToLower(string(stderr))
	return strings.Contains(message, "no checks reported") || strings.Contains(message, "no required checks")
}

func sortCheckDetails(details []delivery.RequiredCheckDetail) {
	sort.Slice(details, func(i, j int) bool {
		left := details[i].Name + "\x00" + details[i].State + "\x00" + details[i].EvidenceRef
		right := details[j].Name + "\x00" + details[j].State + "\x00" + details[j].EvidenceRef
		return left < right
	})
}

type reviewViewResponse struct {
	ReviewDecision string `json:"reviewDecision"`
	Reviews        []struct {
		Author struct {
			Login string `json:"login"`
		} `json:"author"`
		Body        string `json:"body"`
		State       string `json:"state"`
		URL         string `json:"url"`
		SubmittedAt string `json:"submittedAt"`
	} `json:"reviews"`
}

func (adapter *Adapter) observeRequiredReview(ctx context.Context, ref delivery.ChangeRequestRef, pullURL string) (delivery.RequiredReviewOutcome, error) {
	result := adapter.execute(ctx, adapter.executable, []string{
		"pr", "view", ref.ID, "--repo", pullRequestRepositorySelector(ref.Repository), "--json", "reviewDecision,reviews",
	}, nil)
	if result.err != nil {
		return nil, normalizeCommandFailure(ctx, result.err)
	}
	var response reviewViewResponse
	if err := json.Unmarshal(result.stdout, &response); err != nil {
		return nil, failure(ports.FailureProtocolDrift, "GitHub review response was invalid", false)
	}
	switch strings.ToUpper(strings.TrimSpace(response.ReviewDecision)) {
	case "":
		return delivery.ReviewNotRequired{}, nil
	case "APPROVED":
		return delivery.ReviewApproved{}, nil
	case "REVIEW_REQUIRED":
		return delivery.ReviewPending{}, nil
	case "CHANGES_REQUESTED":
		latestByReviewer := make(map[string]struct {
			body        string
			state       string
			url         string
			submittedAt string
		})
		for _, review := range response.Reviews {
			reviewer := strings.TrimSpace(review.Author.Login)
			if reviewer == "" {
				continue
			}
			candidate := struct {
				body        string
				state       string
				url         string
				submittedAt string
			}{review.Body, review.State, review.URL, review.SubmittedAt}
			prior, exists := latestByReviewer[reviewer]
			if !exists || strings.TrimSpace(candidate.submittedAt) == "" || candidate.submittedAt >= prior.submittedAt {
				latestByReviewer[reviewer] = candidate
			}
		}
		details := make([]delivery.ReviewDetail, 0)
		for reviewer, review := range latestByReviewer {
			if strings.ToUpper(strings.TrimSpace(review.state)) != "CHANGES_REQUESTED" {
				continue
			}
			evidence := strings.TrimSpace(review.url)
			if evidence == "" {
				evidence = pullURL + "#review-" + url.QueryEscape(reviewer)
			}
			details = append(details, delivery.ReviewDetail{Reviewer: reviewer, Summary: strings.TrimSpace(review.body), EvidenceRef: evidence})
		}
		if len(details) == 0 {
			return nil, failure(ports.FailureProtocolDrift, "GitHub reported requested changes without review details", false)
		}
		sort.Slice(details, func(i, j int) bool {
			return details[i].Reviewer+"\x00"+details[i].EvidenceRef < details[j].Reviewer+"\x00"+details[j].EvidenceRef
		})
		return delivery.ReviewChangesRequested{Reviews: details}, nil
	default:
		return nil, failure(ports.FailureProtocolDrift, "GitHub review response contained an unknown decision", false)
	}
}
