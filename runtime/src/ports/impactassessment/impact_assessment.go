// Package impactassessment defines deterministic late-evidence recommendations.
package impactassessment

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/fdsprod/darkstar/runtime/src/ports/artifactbinding"
	"github.com/fdsprod/darkstar/runtime/src/ports/artifactlineage"
	"github.com/fdsprod/darkstar/runtime/src/ports/artifactregistry"
)

type CoverageState string

const (
	CoverageSupplied      CoverageState = "supplied"
	CoveragePendingFreeze CoverageState = "pending_freeze"
	CoverageNotSupplied   CoverageState = "not_supplied"
	CoverageUnavailable   CoverageState = "unavailable"
)

type AttemptCoverage struct {
	AttemptID  string        `json:"attemptId"`
	NodeID     string        `json:"nodeId"`
	ManifestID string        `json:"manifestId,omitempty"`
	State      CoverageState `json:"state"`
}

type Action string

const (
	ActionContinue   Action = "continue"
	ActionRefresh    Action = "refresh"
	ActionRevise     Action = "revise"
	ActionInsert     Action = "insert"
	ActionInvalidate Action = "invalidate"
)

// Proposal is a closed action union. Each concrete type has only the fields
// meaningful for its action and marshals its discriminator as a constant.
type Proposal interface {
	Action() Action
	isProposal()
}

type ContinueProposal struct {
	Reason string `json:"reason"`
}

func (ContinueProposal) Action() Action { return ActionContinue }
func (ContinueProposal) isProposal()    {}
func (value ContinueProposal) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Action Action `json:"action"`
		Reason string `json:"reason"`
	}{ActionContinue, value.Reason})
}

type RefreshProposal struct {
	AttemptID string `json:"attemptId"`
	Reason    string `json:"reason"`
}

func (RefreshProposal) Action() Action { return ActionRefresh }
func (RefreshProposal) isProposal()    {}
func (value RefreshProposal) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Action    Action `json:"action"`
		AttemptID string `json:"attemptId"`
		Reason    string `json:"reason"`
	}{ActionRefresh, value.AttemptID, value.Reason})
}

type ArtifactEffect struct {
	Artifact  artifactregistry.VersionRef `json:"artifact"`
	Freshness artifactlineage.Freshness   `json:"freshness"`
}

type ReviseProposal struct {
	Artifacts []ArtifactEffect `json:"artifacts"`
	Reason    string           `json:"reason"`
}

func (ReviseProposal) Action() Action { return ActionRevise }
func (ReviseProposal) isProposal()    {}
func (value ReviseProposal) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Action    Action           `json:"action"`
		Artifacts []ArtifactEffect `json:"artifacts"`
		Reason    string           `json:"reason"`
	}{ActionRevise, value.Artifacts, value.Reason})
}

type InsertProposal struct {
	RunID  string                 `json:"runId"`
	Target artifactbinding.Target `json:"target"`
	Roles  []string               `json:"roles"`
	Reason string                 `json:"reason"`
}

func (InsertProposal) Action() Action { return ActionInsert }
func (InsertProposal) isProposal()    {}
func (value InsertProposal) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Action Action                 `json:"action"`
		RunID  string                 `json:"runId"`
		Target artifactbinding.Target `json:"target"`
		Roles  []string               `json:"roles"`
		Reason string                 `json:"reason"`
	}{ActionInsert, value.RunID, value.Target, value.Roles, value.Reason})
}

type InvalidateProposal struct {
	Artifacts []ArtifactEffect `json:"artifacts"`
	Reason    string           `json:"reason"`
}

func (InvalidateProposal) Action() Action { return ActionInvalidate }
func (InvalidateProposal) isProposal()    {}
func (value InvalidateProposal) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Action    Action           `json:"action"`
		Artifacts []ArtifactEffect `json:"artifacts"`
		Reason    string           `json:"reason"`
	}{ActionInvalidate, value.Artifacts, value.Reason})
}

type Assessment struct {
	Evidence  artifactregistry.VersionRef `json:"evidence"`
	Target    artifactbinding.Target      `json:"target"`
	RunID     string                      `json:"runId,omitempty"`
	Roles     []string                    `json:"roles"`
	Coverage  []AttemptCoverage           `json:"coverage"`
	Proposals []Proposal                  `json:"proposals"`
}

func (value Assessment) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Kind          string                      `json:"kind"`
		SchemaVersion int                         `json:"schemaVersion"`
		Evidence      artifactregistry.VersionRef `json:"evidence"`
		Target        artifactbinding.Target      `json:"target"`
		RunID         string                      `json:"runId,omitempty"`
		Roles         []string                    `json:"roles"`
		Coverage      []AttemptCoverage           `json:"coverage"`
		Proposals     []Proposal                  `json:"proposals"`
	}{"impact_assessment", 1, value.Evidence, value.Target, value.RunID, value.Roles, value.Coverage, value.Proposals})
}

func (value *Assessment) UnmarshalJSON(content []byte) error {
	var wire struct {
		Kind          string                      `json:"kind"`
		SchemaVersion int                         `json:"schemaVersion"`
		Evidence      artifactregistry.VersionRef `json:"evidence"`
		Target        artifactbinding.Target      `json:"target"`
		RunID         string                      `json:"runId"`
		Roles         []string                    `json:"roles"`
		Coverage      []AttemptCoverage           `json:"coverage"`
		Proposals     []json.RawMessage           `json:"proposals"`
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("impact assessment must contain one JSON object")
	}
	if wire.Kind != "impact_assessment" || wire.SchemaVersion != 1 {
		return errors.New("unsupported impact assessment boundary")
	}
	proposals := make([]Proposal, 0, len(wire.Proposals))
	for index, raw := range wire.Proposals {
		var discriminator struct {
			Action Action `json:"action"`
		}
		if err := json.Unmarshal(raw, &discriminator); err != nil {
			return err
		}
		var proposal Proposal
		switch discriminator.Action {
		case ActionContinue:
			proposal = &ContinueProposal{}
		case ActionRefresh:
			proposal = &RefreshProposal{}
		case ActionRevise:
			proposal = &ReviseProposal{}
		case ActionInsert:
			proposal = &InsertProposal{}
		case ActionInvalidate:
			proposal = &InvalidateProposal{}
		default:
			return fmt.Errorf("proposals[%d] has unsupported action %q", index, discriminator.Action)
		}
		if err := json.Unmarshal(raw, proposal); err != nil {
			return err
		}
		proposals = append(proposals, proposal)
	}
	*value = Assessment{Evidence: wire.Evidence, Target: wire.Target, RunID: wire.RunID, Roles: wire.Roles, Coverage: wire.Coverage, Proposals: proposals}
	return nil
}
