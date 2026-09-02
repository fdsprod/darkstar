// Package lateevidence assesses scoped effects without mutating attempts,
// routes, artifacts, or their immutable context manifests.
package lateevidence

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/fdsprod/darkstar/runtime/src/ports/artifactbinding"
	"github.com/fdsprod/darkstar/runtime/src/ports/artifactlineage"
	"github.com/fdsprod/darkstar/runtime/src/ports/artifactregistry"
	"github.com/fdsprod/darkstar/runtime/src/ports/contextmanifest"
	"github.com/fdsprod/darkstar/runtime/src/ports/impactassessment"
	"github.com/fdsprod/darkstar/runtime/src/ports/statestore"
)

var ErrEvidenceNotBound = errors.New("late evidence is not actively bound to the requested target")

type RuntimeReader interface {
	ActiveAttempts(context.Context) ([]statestore.AttemptProjection, error)
	NodesForRun(context.Context, string) ([]statestore.NodeProjection, error)
}

type Policy struct{ FocusedRoles []string }

func DefaultPolicy() Policy {
	return Policy{FocusedRoles: []string{"dataset", "log", "recording", "report", "ticket_export"}}
}

type Request struct {
	Evidence artifactregistry.VersionRef
	Target   artifactbinding.Target
	RunID    string
}

type Service struct {
	artifacts artifactregistry.Registry
	bindings  artifactbinding.Store
	lineage   artifactlineage.Store
	manifests contextmanifest.Store
	runtime   RuntimeReader
	policy    Policy
}

func New(artifacts artifactregistry.Registry, bindings artifactbinding.Store, lineage artifactlineage.Store, manifests contextmanifest.Store, runtime RuntimeReader) (*Service, error) {
	return NewWithPolicy(artifacts, bindings, lineage, manifests, runtime, DefaultPolicy())
}

func NewWithPolicy(artifacts artifactregistry.Registry, bindings artifactbinding.Store, lineage artifactlineage.Store, manifests contextmanifest.Store, runtime RuntimeReader, policy Policy) (*Service, error) {
	if artifacts == nil || bindings == nil || lineage == nil || manifests == nil || runtime == nil {
		return nil, errors.New("artifact, binding, lineage, manifest, and runtime readers are required")
	}
	roles, err := canonicalStrings(policy.FocusedRoles)
	if err != nil {
		return nil, fmt.Errorf("focused role policy: %w", err)
	}
	return &Service{artifacts: artifacts, bindings: bindings, lineage: lineage, manifests: manifests, runtime: runtime, policy: Policy{FocusedRoles: roles}}, nil
}

func (service *Service) Assess(ctx context.Context, request Request) (impactassessment.Assessment, error) {
	if !strings.HasPrefix(request.Evidence.ArtifactID, "artifact_") || request.Evidence.Version == 0 || strings.TrimSpace(request.Target.ID) == "" {
		return impactassessment.Assessment{}, errors.New("exact evidence and binding target are required")
	}
	request.RunID = strings.TrimSpace(request.RunID)
	if request.RunID == "" && request.Target.Kind == artifactbinding.TargetRun {
		request.RunID = request.Target.ID
	}
	evidence, err := service.artifacts.ArtifactVersion(ctx, request.Evidence)
	if err != nil {
		return impactassessment.Assessment{}, fmt.Errorf("read late evidence: %w", err)
	}
	bindings, err := service.bindings.ActiveBindings(ctx, request.Target)
	if err != nil {
		return impactassessment.Assessment{}, fmt.Errorf("read evidence bindings: %w", err)
	}
	if !slices.ContainsFunc(bindings, func(binding artifactbinding.Version) bool { return binding.Artifact == request.Evidence }) {
		return impactassessment.Assessment{}, ErrEvidenceNotBound
	}
	roles := append([]string(nil), evidence.Roles...)
	sort.Strings(roles)
	assessment := impactassessment.Assessment{
		Evidence: request.Evidence, Target: request.Target, RunID: request.RunID,
		Roles: roles, Coverage: []impactassessment.AttemptCoverage{}, Proposals: []impactassessment.Proposal{},
	}

	affected, err := service.lineage.AffectedBy(ctx, request.Evidence)
	if err != nil {
		return impactassessment.Assessment{}, fmt.Errorf("read revision impact: %w", err)
	}
	invalidated, stale := splitEffects(affected)
	if len(invalidated) > 0 {
		assessment.Proposals = append(assessment.Proposals, impactassessment.InvalidateProposal{Artifacts: invalidated, Reason: "revision_invalidated_descendants"})
	}
	if len(stale) > 0 {
		assessment.Proposals = append(assessment.Proposals, impactassessment.ReviseProposal{Artifacts: stale, Reason: "revision_made_descendants_potentially_stale"})
	}

	active, err := service.runtime.ActiveAttempts(ctx)
	if err != nil {
		return impactassessment.Assessment{}, fmt.Errorf("read active attempts: %w", err)
	}
	active = scopedAttempts(active, request)
	for _, attempt := range active {
		coverage, err := service.coverage(ctx, attempt, request.Evidence)
		if err != nil {
			return impactassessment.Assessment{}, err
		}
		assessment.Coverage = append(assessment.Coverage, coverage)
		if coverage.State == impactassessment.CoverageNotSupplied || coverage.State == impactassessment.CoverageUnavailable {
			assessment.Proposals = append(assessment.Proposals, impactassessment.RefreshProposal{AttemptID: attempt.AttemptID, Reason: "active_attempt_missing_exact_evidence"})
		}
	}

	completedTarget, err := service.completedNodeTarget(ctx, request)
	if err != nil {
		return impactassessment.Assessment{}, err
	}
	hasRefresh := slices.ContainsFunc(assessment.Proposals, func(proposal impactassessment.Proposal) bool {
		return proposal.Action() == impactassessment.ActionRefresh
	})
	if request.RunID != "" && !hasRefresh && (completedTarget || (len(active) == 0 && hasFocusedRole(roles, service.policy.FocusedRoles))) {
		reason := "evidence_requires_focused_interpretation"
		if completedTarget {
			reason = "evidence_arrived_after_target_node_completed"
		}
		assessment.Proposals = append(assessment.Proposals, impactassessment.InsertProposal{RunID: request.RunID, Target: request.Target, Roles: roles, Reason: reason})
	}
	if len(assessment.Proposals) == 0 {
		assessment.Proposals = append(assessment.Proposals, impactassessment.ContinueProposal{Reason: "evidence_will_be_selected_by_a_future_or_pending_context_manifest"})
	}
	return assessment, nil
}

func (service *Service) coverage(ctx context.Context, attempt statestore.AttemptProjection, evidence artifactregistry.VersionRef) (impactassessment.AttemptCoverage, error) {
	coverage := impactassessment.AttemptCoverage{AttemptID: attempt.AttemptID, NodeID: attempt.NodeID}
	manifest, err := service.manifests.ManifestForAttempt(ctx, attempt.AttemptID)
	if errors.Is(err, contextmanifest.ErrNotFound) {
		if attempt.Status == statestore.AttemptCreated {
			coverage.State = impactassessment.CoveragePendingFreeze
		} else {
			coverage.State = impactassessment.CoverageUnavailable
		}
		return coverage, nil
	}
	if err != nil {
		return coverage, fmt.Errorf("read context for active attempt %s: %w", attempt.AttemptID, err)
	}
	coverage.ManifestID = manifest.ManifestID
	coverage.State = impactassessment.CoverageNotSupplied
	for _, entry := range manifest.Entries {
		if entry.ArtifactID == evidence.ArtifactID && entry.ArtifactVersion == evidence.Version {
			coverage.State = impactassessment.CoverageSupplied
			break
		}
	}
	return coverage, nil
}

func (service *Service) completedNodeTarget(ctx context.Context, request Request) (bool, error) {
	if request.RunID == "" || request.Target.Kind != artifactbinding.TargetNode {
		return false, nil
	}
	nodes, err := service.runtime.NodesForRun(ctx, request.RunID)
	if err != nil {
		return false, fmt.Errorf("read target node state: %w", err)
	}
	for _, node := range nodes {
		if node.NodeID == request.Target.ID && node.Status.Terminal() {
			return true, nil
		}
	}
	return false, nil
}

func scopedAttempts(values []statestore.AttemptProjection, request Request) []statestore.AttemptProjection {
	if request.RunID == "" {
		return []statestore.AttemptProjection{}
	}
	result := make([]statestore.AttemptProjection, 0, len(values))
	for _, attempt := range values {
		if request.RunID != "" && attempt.RunID != request.RunID {
			continue
		}
		if request.Target.Kind == artifactbinding.TargetNode && attempt.NodeID != request.Target.ID {
			continue
		}
		result = append(result, attempt)
	}
	sort.Slice(result, func(left, right int) bool { return result[left].AttemptID < result[right].AttemptID })
	return result
}

func splitEffects(values []artifactlineage.Invalidation) ([]impactassessment.ArtifactEffect, []impactassessment.ArtifactEffect) {
	invalidated, stale := make([]impactassessment.ArtifactEffect, 0), make([]impactassessment.ArtifactEffect, 0)
	for _, value := range values {
		effect := impactassessment.ArtifactEffect{Artifact: value.Descendant, Freshness: value.Freshness}
		switch value.Freshness {
		case artifactlineage.FreshnessInvalidated:
			invalidated = append(invalidated, effect)
		case artifactlineage.FreshnessPotentiallyStale:
			stale = append(stale, effect)
		}
	}
	return invalidated, stale
}

func hasFocusedRole(roles, focused []string) bool {
	return slices.ContainsFunc(roles, func(role string) bool { return slices.Contains(focused, role) })
}

func canonicalStrings(values []string) ([]string, error) {
	result := append([]string(nil), values...)
	sort.Strings(result)
	for index, value := range result {
		if strings.TrimSpace(value) == "" || value != strings.TrimSpace(value) || (index > 0 && result[index-1] == value) {
			return nil, errors.New("roles must be unique, non-empty, and trimmed")
		}
	}
	return result, nil
}
