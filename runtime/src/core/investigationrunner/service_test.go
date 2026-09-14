package investigationrunner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"darkstar/src/core/artifactingest"
	"darkstar/src/core/artifactops"
	"darkstar/src/core/investigation"
	"darkstar/src/core/repositoryscope"
	"darkstar/src/ports/artifactregistry"
	"darkstar/src/ports/provider"
	"darkstar/src/ports/repositorysnapshot"
	"darkstar/src/ports/statestore"
)

type memoryArtifacts struct {
	values  map[string][]byte
	origins []artifactregistry.Provenance
	fail    bool
}

func (a *memoryArtifacts) Ingest(_ context.Context, input artifactops.IngestInput, key string) (artifactingest.Result, error) {
	if a.fail {
		return artifactingest.Result{}, errors.New("storage unavailable")
	}
	id := "artifact_" + digest([]byte(key))[:26]
	if old, ok := a.values[id]; ok && !bytes.Equal(old, input.Content) {
		return artifactingest.Result{}, errors.New("idempotent output differs")
	}
	a.values[id] = append([]byte{}, input.Content...)
	a.origins = append(a.origins, input.GeneratedBy)
	return artifactingest.Result{Artifact: artifactregistry.ArtifactVersion{ArtifactID: id, Version: 1, BlobDigest: digest(input.Content), Provenance: input.GeneratedBy}}, nil
}

func (a *memoryArtifacts) OriginalContent(_ context.Context, ref artifactregistry.VersionRef) (artifactops.Content, error) {
	value, ok := a.values[ref.ArtifactID]
	if !ok || ref.Version != 1 {
		return artifactops.Content{}, errors.New("artifact missing")
	}
	return artifactops.Content{Reader: io.NopCloser(bytes.NewReader(value)), Digest: digest(value), Size: int64(len(value))}, nil
}

type memoryEvidence struct {
	manifest repositorysnapshot.Manifest
	content  []byte
	corrupt  bool
}

func (e *memoryEvidence) Verify(context.Context, repositorysnapshot.Evidence) error {
	if e.corrupt {
		return errors.New("corrupt retained evidence")
	}
	return nil
}
func (e *memoryEvidence) Manifest(ctx context.Context, value repositorysnapshot.Evidence) (repositorysnapshot.Manifest, error) {
	return e.manifest, e.Verify(ctx, value)
}
func (e *memoryEvidence) ReadFile(_ context.Context, _ repositorysnapshot.Evidence, path string) ([]byte, error) {
	if path != "src/main.go" || e.corrupt {
		return nil, errors.New("path outside manifest")
	}
	return e.content, nil
}

type memoryRecorder struct {
	prepared      bool
	request       json.RawMessage
	contextDigest string
	fingerprint   string
	handle        *provider.AttemptHandle
	events        []provider.Event
	submission    json.RawMessage
	result        *investigation.UnitResult
	fail          string
}

func (r *memoryRecorder) RecordPrepared(_ context.Context, fingerprint, contextDigest string, request json.RawMessage) error {
	if r.fail == "prepared" {
		return errors.New("record failed")
	}
	r.prepared = true
	r.request = request
	r.contextDigest = contextDigest
	r.fingerprint = fingerprint
	return nil
}
func (r *memoryRecorder) RecordHandle(_ context.Context, handle provider.AttemptHandle, _, _ string) error {
	if r.fail == "handle" {
		return errors.New("record failed")
	}
	r.handle = &handle
	return nil
}
func (r *memoryRecorder) RecordEvent(_ context.Context, event provider.Event) error {
	r.events = append(r.events, event)
	return nil
}
func (r *memoryRecorder) RecordSubmission(_ context.Context, raw json.RawMessage) error {
	if r.fail == "submission" {
		return errors.New("record failed")
	}
	r.submission = append([]byte{}, raw...)
	return nil
}
func (r *memoryRecorder) RecordResult(_ context.Context, value investigation.UnitResult) error {
	if r.fail == "result" {
		return errors.New("record failed")
	}
	r.result = &value
	return nil
}

type fakeProvider struct {
	provider.Provider
	capabilities provider.CapabilityManifest
	request      provider.AttemptRequest
	start        func(provider.AttemptRequest) error
	starts       int
	resumes      int
	cancels      int
	final        provider.AttemptResult
}

func (p *fakeProvider) Capabilities(context.Context) (provider.CapabilityManifest, error) {
	return p.capabilities, nil
}
func (p *fakeProvider) StartAttempt(_ context.Context, request provider.AttemptRequest) (provider.AttemptHandle, error) {
	p.starts++
	p.request = request
	if p.start != nil {
		if err := p.start(request); err != nil {
			return provider.AttemptHandle{}, err
		}
	}
	return provider.AttemptHandle{AttemptID: request.AttemptID, Provider: "fake", ProviderThreadID: "thread", ProviderTurnID: "turn"}, nil
}
func (p *fakeProvider) ResumeAttempt(context.Context, provider.ResumeRequest) (provider.AttemptHandle, error) {
	p.resumes++
	return provider.AttemptHandle{}, errors.New("resume prohibited")
}
func (p *fakeProvider) StreamEvents(_ context.Context, request provider.EventRequest) (provider.EventStream, error) {
	return &eventStream{events: []provider.Event{{AttemptID: request.Handle.AttemptID, Sequence: 1, Kind: "assistant_message"}}}, nil
}
func (p *fakeProvider) GetResult(context.Context, provider.ResultRequest) (provider.AttemptResult, error) {
	return p.final, nil
}
func (p *fakeProvider) CancelAttempt(context.Context, provider.CancelRequest) (provider.CancelResult, error) {
	p.cancels++
	return provider.CancelResult{Disposition: provider.CancelUncertain}, nil
}

type eventStream struct {
	events []provider.Event
}

func (s *eventStream) Receive() (provider.Event, error) {
	if len(s.events) == 0 {
		return provider.Event{}, io.EOF
	}
	event := s.events[0]
	s.events = s.events[1:]
	return event, nil
}
func (*eventStream) Close() error {
	return nil
}

type runnerFixture struct {
	service   *Service
	execution investigation.Execution
	provider  *fakeProvider
	artifacts *memoryArtifacts
	evidence  *memoryEvidence
	recorder  *memoryRecorder
	findings  RepositoryFindings
}

func fixture(t *testing.T) runnerFixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo, commit, tree, blob := "repo_one", strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 40)
	evidence := &memoryEvidence{manifest: repositorysnapshot.Manifest{Request: repositorysnapshot.ExportRequest{RepositoryID: repo, CommitSHA: commit}, TreeSHA: tree, Files: []repositorysnapshot.File{{Path: "src/main.go", BlobSHA: blob}}, Exclusions: []repositorysnapshot.Exclusion{}}, content: []byte("first\nsecond\n")}
	artifacts := &memoryArtifacts{values: map[string][]byte{}}
	implementation := &fakeProvider{capabilities: provider.CapabilityManifest{Provider: "fake", Fingerprint: "frozen", Features: map[string]provider.Capability{provider.CapabilityScopedReadFilesystem: provider.AvailableCapability{Version: "v1"}}}, final: provider.SucceededResult{StructuredOutput: json.RawMessage(`{"fake":"final is not authority"}`)}}
	service, err := New(Dependencies{Schema: testSchema{}, Provider: func(context.Context, investigation.ProviderSelection, string, bool) (provider.Provider, error) {
		return implementation, nil
	}, ResolveProvider: func(context.Context, string) (investigation.ProviderSelection, error) {
		return investigation.ProviderSelection{Provider: "fake", CapabilityFingerprint: "frozen"}, nil
	}, Evidence: evidence, Artifacts: artifacts, ValidateBrief: func(context.Context, string, json.RawMessage) error {
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	task, err := service.ResolveTask(t.Context(), investigation.TaskInput{Kind: "text", Text: "Investigate the current interface."}, "project_one")
	if err != nil {
		t.Fatal(err)
	}
	execution := investigation.Execution{CollectionID: "investigation_one", ProjectID: "project_one", UnitID: "unit_one", AttemptID: "attempt_one", Kind: "repository", Task: task, Provider: investigation.ProviderSelection{Provider: "fake", CapabilityFingerprint: "frozen"}, Scope: repositoryscope.AttemptView{Binding: statestore.RepositoryScopeAttemptBinding{ScopeDigest: strings.Repeat("d", 64), EvidenceDigest: strings.Repeat("e", 64)}, Repositories: []statestore.FrozenRepositoryScopeEntry{{Repository: statestore.RepositoryRecord{RepositoryID: repo}, Membership: statestore.RepositoryMembership{Revision: 2}, Revision: repositorysnapshot.ResolvedRevision{CommitSHA: commit, TreeSHA: tree}, ConfigurationDigest: strings.Repeat("f", 64)}}, Evidence: []statestore.RepositoryScopeEvidence{{RepositoryID: repo, Evidence: repositorysnapshot.Evidence{Root: root}}}}}
	findings := RepositoryFindings{SchemaVersion: 1, RepositoryID: repo, CommitSHA: commit, Quality: "complete", Summary: "The interface is defined in the committed file.", CodeReferences: []CodeReference{{RepositoryID: repo, CommitSHA: commit, Path: "src/main.go", BlobSHA: blob, StartLine: 1, EndLine: 2}}, AffectedInterfaces: []string{"Current interface"}, ReusablePatterns: []string{}, Constraints: []string{}, Risks: []string{}, UnresolvedQuestions: []string{}, Limitations: []string{}}
	return runnerFixture{service, execution, implementation, artifacts, evidence, &memoryRecorder{}, findings}
}

func TestRunnerRequiresScopedProviderAndFrozenFingerprintBeforeDispatch(t *testing.T) {
	for _, test := range []string{"unsupported", "drift", "corrupt", "prepared"} {
		t.Run(test, func(t *testing.T) {
			f := fixture(t)
			switch test {
			case "unsupported":
				f.provider.capabilities.Features = map[string]provider.Capability{}
			case "drift":
				f.provider.capabilities.Fingerprint = "changed"
			case "corrupt":
				f.evidence.corrupt = true
			case "prepared":
				f.recorder.fail = "prepared"
			}
			outcome := f.service.Execute(t.Context(), f.execution, f.recorder)
			if outcome.State != "failed" || f.provider.starts != 0 {
				t.Fatalf("unsafe dispatch: %#v starts=%d", outcome, f.provider.starts)
			}
		})
	}
}

func TestRunnerDurableSubmissionOwnEvidenceAndExplicitProvenance(t *testing.T) {
	f := fixture(t)
	f.provider.start = func(request provider.AttemptRequest) error {
		if !f.recorder.prepared || request.RunID != "" || request.NodeID != "" || len(request.Filesystem.ReadRoots) != 1 || len(request.Inputs) != 2 || request.Inputs[0].Name != "task" || request.Inputs[1].Name != "repository" {
			t.Fatalf("invalid execution projection: %#v", request)
		}
		if strings.Contains(request.Inputs[1].Text, "configuration") || strings.Contains(request.Inputs[1].Text, "Root") {
			t.Fatal("authority leaked into content")
		}
		if _, err := request.ToolHandler.Call(t.Context(), "read", "read_repository_file", json.RawMessage(`{"path":"../other/secret"}`)); err == nil {
			t.Fatal("out-of-scope read accepted")
		}
		raw, _ := json.Marshal(f.findings)
		if _, err := request.ToolHandler.Call(t.Context(), "submit", "submit_output", raw); err != nil {
			return err
		}
		if f.recorder.result == nil || len(f.recorder.submission) == 0 {
			t.Fatal("submit acknowledged without durable records")
		}
		f.findings.Summary = "Different output"
		other, _ := json.Marshal(f.findings)
		if _, err := request.ToolHandler.Call(t.Context(), "submit2", "submit_output", other); err == nil {
			t.Fatal("second different submission accepted")
		}
		return nil
	}
	outcome := f.service.Execute(t.Context(), f.execution, f.recorder)
	if outcome.State != "succeeded" || outcome.Result == nil || len(f.recorder.events) != 1 {
		t.Fatalf("outcome %#v", outcome)
	}
	origin, ok := f.artifacts.origins[0].(artifactregistry.InvestigationProvenance)
	if !ok || origin.CollectionID != f.execution.CollectionID || origin.UnitID != f.execution.UnitID || origin.AttemptID != f.execution.AttemptID {
		t.Fatalf("incorrect provenance %#v", f.artifacts.origins)
	}
}

func TestFinalJSONCannotCompleteAndInvalidCitationsNeverPersist(t *testing.T) {
	f := fixture(t)
	raw, _ := json.Marshal(f.findings)
	f.provider.final = provider.SucceededResult{StructuredOutput: raw}
	if got := f.service.Execute(t.Context(), f.execution, f.recorder); got.State != "failed" || f.recorder.result != nil {
		t.Fatalf("final assistant output accepted: %#v", got)
	}
	for _, change := range []func(*RepositoryFindings){
		func(v *RepositoryFindings) {
			v.CodeReferences[0].RepositoryID = "repo_other"
		},
		func(v *RepositoryFindings) {
			v.CodeReferences[0].CommitSHA = strings.Repeat("0", 40)
		},
		func(v *RepositoryFindings) {
			v.CodeReferences[0].Path = "../secret"
		},
		func(v *RepositoryFindings) {
			v.CodeReferences[0].BlobSHA = strings.Repeat("0", 40)
		},
		func(v *RepositoryFindings) {
			v.CodeReferences[0].EndLine = 3
		},
		func(v *RepositoryFindings) {
			v.Quality = "partial"
		},
	} {
		candidate := f.findings
		candidate.CodeReferences = append([]CodeReference{}, candidate.CodeReferences...)
		change(&candidate)
		raw, _ := json.Marshal(candidate)
		handler := &tools{service: f.service, execution: f.execution, recorder: f.recorder}
		if _, err := handler.submit(t.Context(), raw); err == nil {
			t.Fatalf("invalid evidence accepted: %s", raw)
		}
	}
	if len(f.artifacts.values) != 0 {
		t.Fatal("invalid outputs reached artifact storage")
	}
}

func TestReconcileOnlyObservesRetainedAttemptAndRecoversSubmission(t *testing.T) {
	f := fixture(t)
	raw, _ := json.Marshal(f.findings)
	request, err := f.service.request(t.Context(), f.execution, f.provider, &tools{service: f.service, execution: f.execution, recorder: f.recorder})
	if err != nil {
		t.Fatal(err)
	}
	frozen, _ := json.Marshal(request)
	attempt := investigation.Attempt{Request: frozen, ContextDigest: digest(frozen), CapabilityFingerprint: "frozen", Handle: &provider.AttemptHandle{AttemptID: f.execution.AttemptID}, Submission: raw}
	outcome := f.service.Reconcile(t.Context(), f.execution, attempt, f.recorder)
	if outcome.State != "succeeded" || f.provider.starts != 0 || f.provider.resumes != 0 || f.recorder.result == nil {
		t.Fatalf("recovery %#v", outcome)
	}
	outcome = f.service.Reconcile(t.Context(), f.execution, investigation.Attempt{}, f.recorder)
	if outcome.State != "uncertain" || f.provider.starts != 0 {
		t.Fatal("unknown dispatch was replayed")
	}
	f.execution.CancelRequested = true
	f.provider.final = provider.UnknownResult{}
	if got := f.service.Reconcile(t.Context(), f.execution, attempt, f.recorder); got.State != "uncertain" || f.provider.cancels != 1 {
		t.Fatalf("uncertain cancellation became terminal: %#v", got)
	}
	f.provider.final = provider.CancelledResult{}
	if got := f.service.Reconcile(t.Context(), f.execution, attempt, f.recorder); got.State != "cancelled" {
		t.Fatalf("confirmed cancellation: %#v", got)
	}
}

func TestSubmissionStorageFailureRetainsExactRetryAndNoSuccess(t *testing.T) {
	f := fixture(t)
	f.artifacts.fail = true
	handler := &tools{service: f.service, execution: f.execution, recorder: f.recorder}
	raw, _ := json.Marshal(f.findings)
	if _, err := handler.submit(t.Context(), raw); err == nil || len(f.recorder.submission) == 0 || handler.result() != nil {
		t.Fatal("partial output persistence incorrectly acknowledged")
	}
	f.artifacts.fail = false
	if _, err := handler.submit(t.Context(), raw); err != nil || handler.result() == nil {
		t.Fatalf("exact submission retry failed: %v", err)
	}
}

func TestFeatureBriefExactBytesProjectAndFrozenMembership(t *testing.T) {
	f := fixture(t)
	raw := []byte(`{"artifactType":"feature_brief","projectId":"project_one","repositories":[{"repositoryId":"repo_one","membershipRevision":2}]}`)
	f.artifacts.values["artifact_brief"] = raw
	input := investigation.TaskInput{Kind: "feature_brief", FeatureBrief: &investigation.ArtifactReference{ArtifactID: "artifact_brief", Version: 1, SHA256: digest(raw)}}
	task, err := f.service.ResolveTask(t.Context(), input, "project_one")
	if err != nil || !bytes.Equal(task.Content, raw) {
		t.Fatalf("brief resolution %v", err)
	}
	f.execution.Task = task
	if err := validateTaskScope(f.execution); err != nil {
		t.Fatal(err)
	}
	f.execution.Scope.Repositories[0].Membership.Revision = 3
	if validateTaskScope(f.execution) == nil {
		t.Fatal("brief accepted different membership revision")
	}
	input.FeatureBrief.SHA256 = strings.Repeat("f", 64)
	if _, err := f.service.ResolveTask(t.Context(), input, "project_one"); err == nil {
		t.Fatal("brief digest drift accepted")
	}
	input.FeatureBrief.SHA256 = digest(raw)
	if _, err := f.service.ResolveTask(t.Context(), input, "project_other"); err == nil {
		t.Fatal("brief for another project accepted")
	}
}

func TestSynthesisConsumesOnlyExactAcceptedArtifactsAndMissingUnits(t *testing.T) {
	f := fixture(t)
	raw, _ := json.Marshal(f.findings)
	handler := &tools{service: f.service, execution: f.execution, recorder: f.recorder}
	if _, err := handler.submit(t.Context(), raw); err != nil {
		t.Fatal(err)
	}
	result := *handler.result()
	f.execution.Kind = "synthesis"
	f.execution.Scope.Repositories = []statestore.FrozenRepositoryScopeEntry{}
	f.execution.Scope.Evidence = []statestore.RepositoryScopeEvidence{}
	f.execution.Findings = []investigation.UnitResult{result}
	f.execution.Missing = []investigation.MissingUnit{{RepositoryID: "repo_failed", Reason: "Revision unavailable"}}
	synthesis := Synthesis{SchemaVersion: 1, Quality: "partial", EvidenceStatus: "repository_evidence", Summary: "One repository was investigated; another remains missing.", Findings: []FindingReference{{result.RepositoryID, result.Artifact.ArtifactID, result.Artifact.Version, result.Digest}}, Missing: f.execution.Missing, CrossRepositoryImplications: []string{"Impact in the missing repository is unknown."}, Constraints: []string{}, Risks: []string{}, UnresolvedQuestions: []string{}, Limitations: []string{"A required repository investigation failed."}}
	raw, _ = json.Marshal(synthesis)
	if _, err := f.service.validateOutput(t.Context(), f.execution, raw); err != nil {
		t.Fatal(err)
	}
	synthesis.Findings[0].Version++
	raw, _ = json.Marshal(synthesis)
	if _, err := f.service.validateOutput(t.Context(), f.execution, raw); err == nil {
		t.Fatal("synthesis cited unaccepted artifact version")
	}
	f.provider.start = func(request provider.AttemptRequest) error {
		if len(request.Filesystem.ReadRoots) != 0 || len(request.DynamicTools) != 1 || request.Inputs[1].Name != "accepted_findings" {
			t.Fatal("synthesis received repository access")
		}
		return nil
	}
	_ = f.service.Execute(t.Context(), f.execution, &memoryRecorder{})
	if f.provider.starts != 1 {
		t.Fatal("valid synthesis could not dispatch")
	}
}

func TestZeroRepositorySynthesisIsDurableWithoutProvider(t *testing.T) {
	f := fixture(t)
	f.execution.Kind = "synthesis"
	f.execution.Provider = investigation.ProviderSelection{Provider: "daemon", CapabilityFingerprint: digest([]byte("investigation-synthesis/v1"))}
	f.execution.Scope.Repositories = []statestore.FrozenRepositoryScopeEntry{}
	f.execution.Scope.Evidence = []statestore.RepositoryScopeEvidence{}
	outcome := f.service.Execute(t.Context(), f.execution, f.recorder)
	if outcome.State != "succeeded" || outcome.Result == nil || outcome.Result.Quality != "missing" || f.provider.starts != 0 || !strings.Contains(string(outcome.Result.Findings), "no_repository_evidence") {
		t.Fatalf("zero scope outcome %#v", outcome)
	}
	repeated := f.service.Reconcile(t.Context(), f.execution, investigation.Attempt{Result: outcome.Result}, &memoryRecorder{})
	if !reflect.DeepEqual(repeated.Result, outcome.Result) {
		t.Fatalf("zero scope retry changed immutable result %#v", repeated)
	}
}

// The production composition supplies the JSON Schema adapter. This narrow
// test double verifies that runner validation crosses the schema boundary.
type testSchema struct{}

func (testSchema) Validate(schema, value json.RawMessage) error {
	var definition struct {
		Required []string `json:"required"`
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(schema, &definition) != nil || json.Unmarshal(value, &object) != nil {
		return errors.New("schema validation failed")
	}
	for _, key := range definition.Required {
		if raw, ok := object[key]; !ok || bytes.Equal(raw, []byte("null")) {
			return errors.New("required field missing or null")
		}
	}
	return nil
}

func TestRecoveryRejectsChangedFrozenRequestBeforeCancellation(t *testing.T) {
	f := fixture(t)
	request, err := f.service.request(t.Context(), f.execution, f.provider, &tools{service: f.service, execution: f.execution, recorder: f.recorder})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(request)
	attempt := investigation.Attempt{Request: raw, ContextDigest: digest(raw), CapabilityFingerprint: "frozen", Handle: &provider.AttemptHandle{AttemptID: f.execution.AttemptID}}
	f.execution.CancelRequested = true
	attempt.Request = append(attempt.Request, ' ')
	outcome := f.service.Reconcile(t.Context(), f.execution, attempt, f.recorder)
	if outcome.State != "uncertain" || f.provider.cancels != 0 {
		t.Fatalf("changed request authorized provider interaction: %#v", outcome)
	}
}

func TestMissingAndNullOutputFieldsStopBeforePersistence(t *testing.T) {
	f := fixture(t)
	raw, _ := json.Marshal(f.findings)
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	for _, value := range []json.RawMessage{nil, json.RawMessage("null")} {
		delete(fields, "schemaVersion")
		if value != nil {
			fields["schemaVersion"] = value
		}
		raw, _ := json.Marshal(fields)
		handler := &tools{service: f.service, execution: f.execution, recorder: f.recorder}
		if _, err := handler.submit(t.Context(), raw); err == nil || f.recorder.submission != nil {
			t.Fatal("schema-invalid submission persisted")
		}
	}
}

func TestFeatureBriefCanonicalContextRetainsExactOriginalDigest(t *testing.T) {
	f := fixture(t)
	original := []byte("{\n  \"artifactType\": \"feature_brief\",\n  \"projectId\": \"project_one\",\n  \"title\": \"Prefer x < y & z\",\n  \"repositories\": [{\"repositoryId\":\"repo_one\",\"membershipRevision\":2}]\n}")
	f.artifacts.values["artifact_pretty"] = original
	task, err := f.service.ResolveTask(t.Context(), investigation.TaskInput{Kind: "feature_brief", FeatureBrief: &investigation.ArtifactReference{ArtifactID: "artifact_pretty", Version: 1, SHA256: digest(original)}}, "project_one")
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := json.Marshal(task)
	if err != nil {
		t.Fatal(err)
	}
	var restored investigation.FrozenTask
	if err := json.Unmarshal(persisted, &restored); err != nil {
		t.Fatal(err)
	}
	f.execution.Task = restored
	if digest(restored.Content) != restored.Digest || restored.Source.SHA256 != digest(original) || !bytes.Equal(f.artifacts.values["artifact_pretty"], original) || restored.Digest == restored.Source.SHA256 {
		t.Fatalf("context/source identity was lost: %#v", restored)
	}
	if err := validateTaskScope(f.execution); err != nil {
		t.Fatal(err)
	}
}
