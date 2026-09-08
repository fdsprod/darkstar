// Package routeadvisor defines semantic advice, never executable routing authority.
package routeadvisor

import (
	"context"
	"encoding/json"
	"errors"
)

var ErrEvidenceUnavailable = errors.New("route evidence is unavailable")

type Evidence struct {
	Reference string `json:"reference"`
	Digest    string `json:"digest,omitempty"`
	Content   string `json:"content,omitempty"`
}

type Candidate struct {
	Entry     string          `json:"entry"`
	Terminals []string        `json:"terminals"`
	Nodes     []string        `json:"nodes"`
	Contracts json.RawMessage `json:"contracts"`
}

type PlanningContext struct {
	ProjectID        string                     `json:"projectId"`
	ProjectName      string                     `json:"projectName"`
	DefaultEntry     string                     `json:"defaultEntry"`
	DefaultTerminals []string                   `json:"defaultTerminals"`
	RunInputs        map[string]json.RawMessage `json:"runInputs"`
}

type Request struct {
	Context    PlanningContext   `json:"context"`
	Digest     string            `json:"digest"`
	Outcome    string            `json:"outcome"`
	Details    string            `json:"details"`
	Answers    map[string]string `json:"answers"`
	Evidence   []Evidence        `json:"evidence"`
	Candidates []Candidate       `json:"candidates"`
}

type Question struct {
	ID     string `json:"id"`
	Prompt string `json:"prompt"`
}

type CandidateAdvice struct {
	Entry       string     `json:"entry"`
	Terminals   []string   `json:"terminals"`
	Disposition string     `json:"disposition"` // suitable, unsuitable, input_required
	Rationale   string     `json:"rationale"`
	Questions   []Question `json:"questions"`
	Assumptions []string   `json:"assumptions"`
}

type Advice struct {
	Confidence   string            `json:"confidence"` // high, medium, low
	Candidates   []CandidateAdvice `json:"candidates"`
	EvidenceUsed []string          `json:"evidenceUsed"`
}

type Advisor interface {
	Assess(context.Context, Request) (Advice, error)
}
type EvidenceResolver interface {
	Resolve(context.Context, string) (Evidence, error)
}
type AdvisorFunc func(context.Context, Request) (Advice, error)

func (f AdvisorFunc) Assess(ctx context.Context, request Request) (Advice, error) {
	return f(ctx, request)
}
