package lateevidence

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"darkstar/src/ports/artifactbinding"
	"darkstar/src/ports/artifactlineage"
	"darkstar/src/ports/artifactregistry"
	"darkstar/src/ports/contextmanifest"
	"darkstar/src/ports/impactassessment"
	"darkstar/src/ports/statestore"
)

func TestAssessmentNeverClaimsUnsuppliedEvidenceReachedActiveAttempt(t *testing.T) {
	t.Parallel()
	evidence := artifactregistry.VersionRef{ArtifactID: "artifact_evidence", Version: 1}
	target := artifactbinding.Target{Kind: artifactbinding.TargetNode, ID: "design"}
	service := newService(t, fixture{
		evidence: evidence, target: target, roles: []string{"note"},
		attempts: []statestore.AttemptProjection{{AttemptID: "attempt_active", RunID: "run_one", NodeID: "design", Status: statestore.AttemptRunning}},
		manifests: map[string]contextmanifest.Manifest{"attempt_active": {
			ManifestID: "manifest_active", Entries: []contextmanifest.Entry{{ArtifactID: "artifact_other", ArtifactVersion: 1}},
		}},
	})
	assessment, err := service.Assess(context.Background(), Request{Evidence: evidence, Target: target, RunID: "run_one"})
	if err != nil {
		t.Fatal(err)
	}
	if len(assessment.Coverage) != 1 || assessment.Coverage[0].State != impactassessment.CoverageNotSupplied {
		t.Fatalf("coverage = %#v", assessment.Coverage)
	}
	if got := actions(assessment.Proposals); !reflect.DeepEqual(got, []impactassessment.Action{impactassessment.ActionRefresh}) {
		t.Fatalf("actions = %#v", got)
	}
}

func TestPendingFreezeCanContinueAndExactManifestEntryIsSupplied(t *testing.T) {
	t.Parallel()
	evidence := artifactregistry.VersionRef{ArtifactID: "artifact_evidence", Version: 1}
	target := artifactbinding.Target{Kind: artifactbinding.TargetRun, ID: "run_one"}
	for _, test := range []struct {
		name      string
		attempt   statestore.AttemptProjection
		manifests map[string]contextmanifest.Manifest
		coverage  impactassessment.CoverageState
	}{
		{name: "pending freeze", attempt: statestore.AttemptProjection{AttemptID: "attempt_created", RunID: "run_one", NodeID: "design", Status: statestore.AttemptCreated}, manifests: map[string]contextmanifest.Manifest{}, coverage: impactassessment.CoveragePendingFreeze},
		{name: "supplied", attempt: statestore.AttemptProjection{AttemptID: "attempt_running", RunID: "run_one", NodeID: "design", Status: statestore.AttemptRunning}, manifests: map[string]contextmanifest.Manifest{"attempt_running": {ManifestID: "manifest_running", Entries: []contextmanifest.Entry{{ArtifactID: evidence.ArtifactID, ArtifactVersion: evidence.Version}}}}, coverage: impactassessment.CoverageSupplied},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			service := newService(t, fixture{evidence: evidence, target: target, roles: []string{"note"}, attempts: []statestore.AttemptProjection{test.attempt}, manifests: test.manifests})
			assessment, err := service.Assess(context.Background(), Request{Evidence: evidence, Target: target, RunID: "run_one"})
			if err != nil || assessment.Coverage[0].State != test.coverage || !reflect.DeepEqual(actions(assessment.Proposals), []impactassessment.Action{impactassessment.ActionContinue}) {
				t.Fatalf("assessment = %#v, error = %v", assessment, err)
			}
		})
	}
}

func TestRevisionImpactAndCompletedTargetProduceScopedProposals(t *testing.T) {
	t.Parallel()
	evidence := artifactregistry.VersionRef{ArtifactID: "artifact_evidence", Version: 2}
	target := artifactbinding.Target{Kind: artifactbinding.TargetNode, ID: "design"}
	service := newService(t, fixture{
		evidence: evidence, target: target, roles: []string{"dataset"},
		affected: []artifactlineage.Invalidation{
			{Trigger: evidence, Descendant: artifactregistry.VersionRef{ArtifactID: "artifact_code", Version: 1}, Freshness: artifactlineage.FreshnessPotentiallyStale},
			{Trigger: evidence, Descendant: artifactregistry.VersionRef{ArtifactID: "artifact_design", Version: 1}, Freshness: artifactlineage.FreshnessInvalidated},
		},
		nodes: []statestore.NodeProjection{{RunID: "run_one", NodeID: "design", Status: statestore.NodeSucceeded}},
	})
	assessment, err := service.Assess(context.Background(), Request{Evidence: evidence, Target: target, RunID: "run_one"})
	if err != nil {
		t.Fatal(err)
	}
	want := []impactassessment.Action{impactassessment.ActionInvalidate, impactassessment.ActionRevise, impactassessment.ActionInsert}
	if got := actions(assessment.Proposals); !reflect.DeepEqual(got, want) {
		t.Fatalf("actions = %#v, want %#v", got, want)
	}
}

func TestAssessmentRequiresActiveExactBinding(t *testing.T) {
	t.Parallel()
	evidence := artifactregistry.VersionRef{ArtifactID: "artifact_evidence", Version: 1}
	service := newService(t, fixture{evidence: evidence, target: artifactbinding.Target{Kind: artifactbinding.TargetRun, ID: "another"}})
	_, err := service.Assess(context.Background(), Request{Evidence: evidence, Target: artifactbinding.Target{Kind: artifactbinding.TargetRun, ID: "run_one"}, RunID: "run_one"})
	if !errors.Is(err, ErrEvidenceNotBound) {
		t.Fatalf("error = %v", err)
	}
}

func actions(proposals []impactassessment.Proposal) []impactassessment.Action {
	result := make([]impactassessment.Action, len(proposals))
	for index, proposal := range proposals {
		result[index] = proposal.Action()
	}
	return result
}

type fixture struct {
	evidence  artifactregistry.VersionRef
	target    artifactbinding.Target
	roles     []string
	affected  []artifactlineage.Invalidation
	attempts  []statestore.AttemptProjection
	nodes     []statestore.NodeProjection
	manifests map[string]contextmanifest.Manifest
}

func newService(t *testing.T, value fixture) *Service {
	t.Helper()
	service, err := New(
		fakeArtifacts{version: artifactregistry.ArtifactVersion{ArtifactID: value.evidence.ArtifactID, Version: value.evidence.Version, Roles: value.roles}},
		fakeBindings{target: value.target, evidence: value.evidence}, fakeLineage{affected: value.affected},
		fakeManifests{values: value.manifests}, fakeRuntime{attempts: value.attempts, nodes: value.nodes},
	)
	if err != nil {
		t.Fatal(err)
	}
	return service
}

type fakeArtifacts struct {
	version artifactregistry.ArtifactVersion
}

func (fake fakeArtifacts) Register(context.Context, artifactregistry.RegisterRequest) (artifactregistry.ArtifactVersion, bool, error) {
	return artifactregistry.ArtifactVersion{}, false, nil
}
func (fake fakeArtifacts) ArtifactVersion(_ context.Context, reference artifactregistry.VersionRef) (artifactregistry.ArtifactVersion, error) {
	if reference.ArtifactID == fake.version.ArtifactID && reference.Version == fake.version.Version {
		return fake.version, nil
	}
	return artifactregistry.ArtifactVersion{}, artifactregistry.ErrNotFound
}
func (fake fakeArtifacts) LatestVersion(context.Context, string) (artifactregistry.ArtifactVersion, error) {
	return fake.version, nil
}
func (fake fakeArtifacts) Versions(context.Context, string) ([]artifactregistry.ArtifactVersion, error) {
	return []artifactregistry.ArtifactVersion{fake.version}, nil
}
func (fake fakeArtifacts) VersionsByDigest(context.Context, string) ([]artifactregistry.ArtifactVersion, error) {
	return nil, nil
}
func (fake fakeArtifacts) Artifacts(context.Context) ([]artifactregistry.ArtifactVersion, error) {
	return []artifactregistry.ArtifactVersion{fake.version}, nil
}

type fakeBindings struct {
	target   artifactbinding.Target
	evidence artifactregistry.VersionRef
}

func (fakeBindings) Bind(context.Context, artifactbinding.BindRequest) (artifactbinding.Version, bool, error) {
	return artifactbinding.Version{}, false, nil
}
func (fakeBindings) Unbind(context.Context, artifactbinding.UnbindRequest) (artifactbinding.Version, bool, error) {
	return artifactbinding.Version{}, false, nil
}
func (fakeBindings) BindingVersion(context.Context, string, uint64) (artifactbinding.Version, error) {
	return artifactbinding.Version{}, artifactbinding.ErrNotFound
}
func (fakeBindings) LatestBinding(context.Context, string) (artifactbinding.Version, error) {
	return artifactbinding.Version{}, artifactbinding.ErrNotFound
}
func (fakeBindings) BindingVersions(context.Context, string) ([]artifactbinding.Version, error) {
	return nil, nil
}
func (fake fakeBindings) ActiveBindings(_ context.Context, target artifactbinding.Target) ([]artifactbinding.Version, error) {
	if target == fake.target {
		return []artifactbinding.Version{{BindingID: "binding_test", State: artifactbinding.StateBound, Artifact: fake.evidence, Target: target}}, nil
	}
	return nil, nil
}

type fakeLineage struct {
	affected []artifactlineage.Invalidation
}

func (fakeLineage) AddDependency(context.Context, artifactlineage.AddRequest) (artifactlineage.Dependency, bool, error) {
	return artifactlineage.Dependency{}, false, nil
}
func (fakeLineage) Dependencies(context.Context, artifactregistry.VersionRef) ([]artifactlineage.Dependency, error) {
	return nil, nil
}
func (fakeLineage) Dependents(context.Context, artifactregistry.VersionRef) ([]artifactlineage.Dependency, error) {
	return nil, nil
}
func (fakeLineage) Freshness(context.Context, artifactregistry.VersionRef) (artifactlineage.Freshness, error) {
	return artifactlineage.FreshnessCurrent, nil
}
func (fakeLineage) Invalidations(context.Context, artifactregistry.VersionRef) ([]artifactlineage.Invalidation, error) {
	return nil, nil
}
func (fake fakeLineage) AffectedBy(context.Context, artifactregistry.VersionRef) ([]artifactlineage.Invalidation, error) {
	return append([]artifactlineage.Invalidation(nil), fake.affected...), nil
}

type fakeManifests struct {
	values map[string]contextmanifest.Manifest
}

func (fakeManifests) StoreManifest(context.Context, contextmanifest.Manifest, string) (contextmanifest.Manifest, bool, error) {
	return contextmanifest.Manifest{}, false, nil
}
func (fakeManifests) Manifest(context.Context, string) (contextmanifest.Manifest, error) {
	return contextmanifest.Manifest{}, contextmanifest.ErrNotFound
}
func (fake fakeManifests) ManifestForAttempt(_ context.Context, attemptID string) (contextmanifest.Manifest, error) {
	value, ok := fake.values[attemptID]
	if !ok {
		return contextmanifest.Manifest{}, contextmanifest.ErrNotFound
	}
	return value, nil
}

type fakeRuntime struct {
	attempts []statestore.AttemptProjection
	nodes    []statestore.NodeProjection
}

func (fake fakeRuntime) ActiveAttempts(context.Context) ([]statestore.AttemptProjection, error) {
	return append([]statestore.AttemptProjection(nil), fake.attempts...), nil
}
func (fake fakeRuntime) NodesForRun(context.Context, string) ([]statestore.NodeProjection, error) {
	return append([]statestore.NodeProjection(nil), fake.nodes...), nil
}
