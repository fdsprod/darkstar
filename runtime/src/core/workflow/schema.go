// Package workflow defines the provider-neutral, versioned workflow contract.
// It models authored workflow documents only; graph validation and execution
// state belong to separate workflow services.
package workflow

import "encoding/json"

const (
	APIVersionV1Alpha1 = "darkstar.local/v1alpha1"
	KindWorkflow       = "Workflow"
)

// Identifier is a stable workflow-local name.
type Identifier string

// Document is one authored workflow definition.
type Document struct {
	APIVersion string   `json:"apiVersion"`
	Kind       string   `json:"kind"`
	Metadata   Metadata `json:"metadata"`
	Spec       Spec     `json:"spec"`
}

type Metadata struct {
	Name        string `json:"name"`
	Version     string `json:"version"`
	DisplayName string `json:"displayName,omitempty"`
	Description string `json:"description,omitempty"`
}

type Spec struct {
	Inputs        map[Identifier]ValueDeclaration `json:"inputs,omitempty"`
	RouteDefaults RouteDefaults                   `json:"routeDefaults"`
	Profiles      map[Identifier]RouteProfile     `json:"profiles,omitempty"`
	Nodes         map[Identifier]Node             `json:"nodes"`
}

type RouteDefaults struct {
	Entry     Identifier   `json:"entry"`
	Terminals []Identifier `json:"terminals"`
}

type RouteProfile struct {
	Description   string                         `json:"description"`
	Entry         Identifier                     `json:"entry"`
	Terminals     []Identifier                   `json:"terminals"`
	InputDefaults map[Identifier]json.RawMessage `json:"inputDefaults"`
}

type ValueType string

const (
	ValueNull    ValueType = "null"
	ValueBoolean ValueType = "boolean"
	ValueInteger ValueType = "integer"
	ValueNumber  ValueType = "number"
	ValueString  ValueType = "string"
	ValueArray   ValueType = "array"
	ValueObject  ValueType = "object"
)

type ValueDeclaration struct {
	Type        ValueType `json:"type"`
	Schema      string    `json:"schema,omitempty"`
	Description string    `json:"description,omitempty"`
}

type OutputDeclaration struct {
	Type        ValueType `json:"type"`
	Schema      string    `json:"schema,omitempty"`
	Description string    `json:"description,omitempty"`
	Required    *bool     `json:"required,omitempty"`
}

type NodeType string

const (
	NodeReasoning      NodeType = "reasoning"
	NodeGate           NodeType = "gate"
	NodeCommand        NodeType = "command"
	NodeApproval       NodeType = "approval"
	NodeSubworkflow    NodeType = "subworkflow"
	NodePointExecution NodeType = "point_execution"
)

// Node is a closed executor union. Each concrete node carries exactly one
// executor shape, so a reasoning node cannot also contain command settings.
type Node interface {
	Type() NodeType
	Fields() NodeFields
	isNode()
}

type NodeFields struct {
	DisplayName    string
	Entry          bool
	Terminal       bool
	Inputs         map[Identifier]Binding
	Outputs        map[Identifier]OutputDeclaration
	Readiness      *ReadinessContract
	Validators     []Validator
	Retry          *RetryPolicy
	Checkpoint     Checkpoint
	TransitionMode TransitionMode
	Join           *Join
	Permissions    []string
	Transitions    []Transition
}

// ReadinessContract keeps advisory evidence and remedies separate from the
// executable input, checkpoint, and transition contract. Required inputs are
// derived from the node's closed Binding variants rather than repeated here.
type ReadinessContract struct {
	RecommendedEvidence []EvidenceRequirement `json:"recommendedEvidence"`
	PolicyGates         []ReadinessPolicyGate `json:"policyGates"`
	Invariants          []string              `json:"invariants"`
	Remedies            []ReadinessRemedy     `json:"remedies"`
}

type EvidenceRequirement struct {
	Role        Identifier `json:"role"`
	Description string     `json:"description"`
}

type ReadinessGateEnforcement string

const (
	ReadinessGateAdvisory ReadinessGateEnforcement = "advisory"
	ReadinessGateBlocking ReadinessGateEnforcement = "blocking"
	ReadinessGateExternal ReadinessGateEnforcement = "external"
)

type ReadinessPolicyGate struct {
	Policy      Identifier               `json:"policy"`
	Enforcement ReadinessGateEnforcement `json:"enforcement"`
	Description string                   `json:"description"`
}

type ReadinessRemedyAction string

const (
	ReadinessSupplyInput       ReadinessRemedyAction = "supply_input"
	ReadinessReviseArtifact    ReadinessRemedyAction = "revise_artifact"
	ReadinessClarifyDecision   ReadinessRemedyAction = "clarify_decision"
	ReadinessInstallCapability ReadinessRemedyAction = "install_capability"
	ReadinessRerunValidation   ReadinessRemedyAction = "rerun_validation"
)

type ReadinessRemedy struct {
	Code        Identifier            `json:"code"`
	Target      Identifier            `json:"target"`
	Action      ReadinessRemedyAction `json:"action"`
	Description string                `json:"description"`
}

type ReasoningNode struct {
	Common   NodeFields
	Executor ReasoningExecutor
}

func (ReasoningNode) Type() NodeType       { return NodeReasoning }
func (n ReasoningNode) Fields() NodeFields { return n.Common }
func (ReasoningNode) isNode()              {}

type GateNode struct {
	Common   NodeFields
	Executor GateExecutor
}

func (GateNode) Type() NodeType       { return NodeGate }
func (n GateNode) Fields() NodeFields { return n.Common }
func (GateNode) isNode()              {}

type CommandNode struct {
	Common   NodeFields
	Executor CommandExecutor
}

func (CommandNode) Type() NodeType       { return NodeCommand }
func (n CommandNode) Fields() NodeFields { return n.Common }
func (CommandNode) isNode()              {}

type ApprovalNode struct {
	Common   NodeFields
	Executor ApprovalExecutor
}

func (ApprovalNode) Type() NodeType       { return NodeApproval }
func (n ApprovalNode) Fields() NodeFields { return n.Common }
func (ApprovalNode) isNode()              {}

type SubworkflowNode struct {
	Common NodeFields
	Call   SubworkflowCall
}

func (SubworkflowNode) Type() NodeType       { return NodeSubworkflow }
func (n SubworkflowNode) Fields() NodeFields { return n.Common }
func (SubworkflowNode) isNode()              {}

// PointExecutionNode delegates one visit to the typed implementation-point
// executor. Its policy is workflow data rather than provider reasoning output.
type PointExecutionNode struct {
	Common   NodeFields
	Executor PointExecutionExecutor
}

func (PointExecutionNode) Type() NodeType       { return NodePointExecution }
func (n PointExecutionNode) Fields() NodeFields { return n.Common }
func (PointExecutionNode) isNode()              {}

type PointApprovalMode string

const (
	PointApprovalNone     PointApprovalMode = "none"
	PointApprovalEvery    PointApprovalMode = "every"
	PointApprovalRisk     PointApprovalMode = "risk"
	PointApprovalCombined PointApprovalMode = "combined"
)

type PointValidationMode string

const (
	PointValidationEach            PointValidationMode = "each"
	PointValidationCombined        PointValidationMode = "combined"
	PointValidationEachAndCombined PointValidationMode = "each_and_combined"
)

type PointPublishingMode string

const (
	PointPublishingAfterStory PointPublishingMode = "after_story_validation"
	PointPublishingAfterEach  PointPublishingMode = "after_each_point"
)

type PointExecutionExecutor struct {
	PlanInput  Identifier          `json:"planInput"`
	Approval   PointApprovalMode   `json:"approval"`
	RiskTags   []string            `json:"riskTags"`
	Validation PointValidationMode `json:"validation"`
	Publishing PointPublishingMode `json:"publishing"`
}

type ReasoningExecutor struct {
	Agent  string   `json:"agent"`
	Skills []string `json:"skills,omitempty"`
	Tools  []string `json:"tools,omitempty"`
}

// GateExecutor is the deterministic conditional executor. Reasoning-produced
// assessments must be committed as inputs before this predicate is evaluated.
type GateExecutor struct {
	Policy    string
	Condition Predicate
}

type CommandExecutor struct {
	Argv           []string `json:"argv"`
	CWD            string   `json:"cwd,omitempty"`
	TimeoutSeconds *uint64  `json:"timeoutSeconds,omitempty"`
}

// ApprovalExecutor is a closed actor union.
type ApprovalExecutor interface {
	Actor() string
	isApprovalExecutor()
}

type NamedApproval struct{ Name string }

func (a NamedApproval) Actor() string     { return a.Name }
func (NamedApproval) isApprovalExecutor() {}

type ExternalApproval struct {
	ExternalCondition string
	EvidenceOutput    Identifier
}

func (ExternalApproval) Actor() string       { return "external" }
func (ExternalApproval) isApprovalExecutor() {}

type WorkflowReference struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Digest  string `json:"digest,omitempty"`
	Path    string `json:"path,omitempty"`
}

type SubworkflowCall struct {
	Workflow  WorkflowReference         `json:"workflow"`
	Entry     Identifier                `json:"entry"`
	Terminals []Identifier              `json:"terminals"`
	Inputs    map[Identifier]Identifier `json:"inputs"`
	Outputs   map[Identifier]string     `json:"outputs"`
}

// Binding is a closed required/optional union. A default can exist only on an
// explicitly optional binding.
type Binding interface {
	Source() string
	ValueType() ValueType
	isBinding()
}

type RequiredBinding struct {
	From        string
	Pointer     string
	Type        ValueType
	Description string
}

func (b RequiredBinding) Source() string       { return b.From }
func (b RequiredBinding) ValueType() ValueType { return b.Type }
func (RequiredBinding) isBinding()             {}

type OptionalBinding struct {
	From        string
	Pointer     string
	Type        ValueType
	Default     json.RawMessage
	Description string
}

func (b OptionalBinding) Source() string       { return b.From }
func (b OptionalBinding) ValueType() ValueType { return b.Type }
func (OptionalBinding) isBinding()             {}

// Validator is a closed schema/command union.
type Validator interface{ isValidator() }

type SchemaValidator struct {
	Output Identifier `json:"output"`
	Schema string     `json:"schema"`
}

func (SchemaValidator) isValidator() {}

type CommandValidator struct {
	Command []string `json:"command"`
}

func (CommandValidator) isValidator() {}

type RetryFailure string

const (
	RetryProviderUnavailable RetryFailure = "provider_unavailable"
	RetryProviderRateLimit   RetryFailure = "provider_rate_limit"
	RetryProcessFailure      RetryFailure = "process_failure"
	RetryValidatorFailure    RetryFailure = "validator_failure"
	RetryTimeout             RetryFailure = "timeout"
	RetryInterrupted         RetryFailure = "interrupted"
)

type RetryPolicy struct {
	MaxAttempts uint8          `json:"maxAttempts"`
	On          []RetryFailure `json:"on"`
}

// Checkpoint is a closed policy union.
type Checkpoint interface {
	Mode() CheckpointMode
	isCheckpoint()
}

type CheckpointMode string

const (
	CheckpointNone            CheckpointMode = "none"
	CheckpointAcknowledge     CheckpointMode = "acknowledge"
	CheckpointApprove         CheckpointMode = "approve"
	CheckpointApproveOnChange CheckpointMode = "approve_on_change"
	CheckpointExternal        CheckpointMode = "external"
)

type NoCheckpoint struct{}

func (NoCheckpoint) Mode() CheckpointMode { return CheckpointNone }
func (NoCheckpoint) isCheckpoint()        {}

type AcknowledgeCheckpoint struct{}

func (AcknowledgeCheckpoint) Mode() CheckpointMode { return CheckpointAcknowledge }
func (AcknowledgeCheckpoint) isCheckpoint()        {}

type ApproveCheckpoint struct{ MaxRevisions *uint8 }

func (ApproveCheckpoint) Mode() CheckpointMode { return CheckpointApprove }
func (ApproveCheckpoint) isCheckpoint()        {}

type ApproveOnChangeCheckpoint struct {
	When         Predicate
	MaxRevisions *uint8
}

func (ApproveOnChangeCheckpoint) Mode() CheckpointMode { return CheckpointApproveOnChange }
func (ApproveOnChangeCheckpoint) isCheckpoint()        {}

type ExternalCheckpoint struct{ ExternalCondition string }

func (ExternalCheckpoint) Mode() CheckpointMode { return CheckpointExternal }
func (ExternalCheckpoint) isCheckpoint()        {}

type TransitionMode string

const (
	TransitionExclusive TransitionMode = "exclusive"
	TransitionFanout    TransitionMode = "fanout"
)

type JoinMode string

const (
	JoinOne JoinMode = "one"
	JoinAll JoinMode = "all"
)

type Join struct {
	Mode JoinMode     `json:"mode"`
	From []Identifier `json:"from"`
}

// Transition is a closed normal/bounded union. Only a bounded transition owns
// a traversal budget.
type Transition interface {
	ID() Identifier
	Target() Identifier
	isTransition()
}

type TransitionFields struct {
	TransitionID     Identifier
	To               Identifier
	When             Predicate
	EnabledByDefault *bool
}

type NormalTransition struct{ Common TransitionFields }

func (t NormalTransition) ID() Identifier     { return t.Common.TransitionID }
func (t NormalTransition) Target() Identifier { return t.Common.To }
func (NormalTransition) isTransition()        {}

type BoundedTransition struct {
	Common        TransitionFields
	MaxTraversals uint16
}

func (t BoundedTransition) ID() Identifier     { return t.Common.TransitionID }
func (t BoundedTransition) Target() Identifier { return t.Common.To }
func (BoundedTransition) isTransition()        {}

// Predicate is the closed v1alpha1 data-only expression tree.
type Predicate interface{ isPredicate() }

type ConstantPredicate struct{ Value bool }

func (ConstantPredicate) isPredicate() {}

type ComparisonOperator string

const (
	CompareEqual          ComparisonOperator = "eq"
	CompareNotEqual       ComparisonOperator = "ne"
	CompareLess           ComparisonOperator = "lt"
	CompareLessOrEqual    ComparisonOperator = "lte"
	CompareGreater        ComparisonOperator = "gt"
	CompareGreaterOrEqual ComparisonOperator = "gte"
)

type ComparisonPredicate struct {
	Operator ComparisonOperator
	Args     [2]Operand
}

func (ComparisonPredicate) isPredicate() {}

type PresentPredicate struct{ Reference ReferenceOperand }

func (PresentPredicate) isPredicate() {}

type LogicalOperator string

const (
	LogicalAll LogicalOperator = "all"
	LogicalAny LogicalOperator = "any"
)

type LogicalPredicate struct {
	Operator LogicalOperator
	Args     []Predicate
}

func (LogicalPredicate) isPredicate() {}

type NotPredicate struct{ Arg Predicate }

func (NotPredicate) isPredicate() {}

// Operand is a closed reference/literal union. RawMessage preserves the exact
// JSON literal type without introducing coercion.
type Operand interface{ isOperand() }

type ReferenceOperand struct{ Ref string }

func (ReferenceOperand) isOperand() {}

type LiteralOperand struct{ Literal json.RawMessage }

func (LiteralOperand) isOperand() {}
