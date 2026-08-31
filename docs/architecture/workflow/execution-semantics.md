# DARKSTAR Workflow Execution Semantics

> [Documentation index](../../README.md)

**Status:** Proposed normative contract for `darkstar.local/v1alpha1`  
**Decision:** DS-002  
**Scope:** Workflow validation, route freezing, node execution, transitions, joins, bounded loops, checkpoints, sub-workflows, route patches, versioning, and stable errors

---

## 1. Decision

DARKSTAR workflows are immutable, typed, directed graphs. They are not scripts.
The language has only five control-flow mechanisms:

1. a node becomes eligible after an entry activation or incoming transition;
2. explicit input bindings copy typed values into an immutable node-visit input snapshot;
3. a data-only predicate selects one transition or an explicit fanout set;
4. a declared join combines transition tokens; and
5. an edge with a finite traversal budget may close a cycle.

Sub-workflows create a child execution frame pinned to an exact workflow version.
Route patches can only enable or disable transitions already present in the frozen
workflow and change the authorized terminal boundary. They cannot add code,
expressions, commands, or node definitions.

The language intentionally has no variables, assignment, functions, arithmetic,
string interpolation, unbounded iteration, dynamic node creation, exception
handling, or model-authored state mutation. Executors produce data; the daemon
validates and commits control-flow decisions.

## 2. Normative terms

The words **MUST**, **MUST NOT**, **SHOULD**, and **MAY** are normative.

| Term | Meaning |
|---|---|
| Workflow definition | Human-authored graph before installation. |
| Workflow version | Immutable installed definition identified by name, semantic version, and SHA-256 digest. |
| Route | Frozen authorized subgraph for one run, with one entry and one or more terminal boundaries. |
| Node | Static executor and contract declaration. |
| Node visit | One activation of a node in one execution frame. A bounded edge can create another visit to the same node. |
| Attempt | One executor invocation for a visit. Retries and checkpoint revisions create new attempts. |
| Transition token | Durable fact that a particular transition fired from a successful visit. |
| Execution frame | State and counters for one workflow invocation. A sub-workflow has a child frame. |
| Logical closure | Point at which a declared incoming transition can no longer fire for the current join epoch. |

## 3. Installed form and identity

Authoring files MAY be YAML or JSON. Installation parses them to the data model in
[`schemas/workflow-v1alpha1.schema.json`](../../../schemas/workflow-v1alpha1.schema.json),
rejects duplicate YAML keys and non-JSON values, and serializes canonical JSON:

- UTF-8;
- object keys sorted lexicographically;
- arrays retained in authored order;
- no insignificant whitespace; and
- integers and finite JSON numbers only.

The workflow digest is lowercase SHA-256 of those canonical bytes. The tuple
`(metadata.name, metadata.version)` is immutable. Reinstalling the same tuple and
digest is idempotent; a different digest for the same tuple fails with
`WF_VERSION_CONFLICT`.

A run pins:

- the root workflow digest;
- every transitively referenced sub-workflow digest;
- the frozen route and route revision;
- run-affecting project policy and executor configuration; and
- the schema versions used for input, output, and artifact validation.

An active run never floats to a newer workflow. Migration to a different digest is
outside `v1alpha1`; the safe MVP operation is a new run linked to the old run.

## 4. Node contract

Every node has a stable identifier matching `[a-z][a-z0-9_]{0,63}`. Renaming an ID
is a breaking workflow change. A node declares:

- `type`: `reasoning`, `gate`, `command`, `approval`, or `subworkflow`;
- whether it is eligible as an explicit `entry` or `terminal` boundary;
- typed `inputs` and `outputs`;
- its executor or pinned sub-workflow call;
- deterministic validators;
- retry, timeout, cancellation, permission, and concurrency policy;
- checkpoint policy;
- `transitionMode`: `exclusive` (default) or `fanout`; and
- ordered outgoing `transitions` with globally unique IDs.

`terminal: true` means **terminal-capable**, not “has no outgoing transitions.” A
technical-design node can therefore be the boundary of a design-only route and
still have outgoing transitions in an end-to-end route. Once a node is a terminal
boundary in the frozen route, its outgoing transitions are suppressed.

Node types affect execution only:

| Type | Executor result |
|---|---|
| `reasoning` | A provider adapter returns declared structured outputs and artifacts. |
| `gate` | The daemon evaluates a versioned data-only condition over committed input scores and emits a durable boolean decision plus gate evidence. No provider is invoked. |
| `command` | A deterministic command adapter returns declared structured outputs and evidence. Argument arrays are used; the workflow language performs no shell interpolation. |
| `approval` | A named human or external actor supplies a typed decision/output. This is a node, not a provider permission. |
| `subworkflow` | A child execution frame runs an exact referenced workflow and maps declared child outputs back to the parent node. |

## 5. Input binding and outputs

Inputs are named bindings. A binding has one source, a declared JSON value type,
and `required` (default `true`). The only source forms are:

```text
run.input.<name>
node.<node_id>.output.<name>
```

An optional RFC 6901 `pointer` selects a value inside the source. No selector can
call a function, concatenate strings, query a collection, read undeclared daemon
state, or choose “the latest artifact” implicitly.

Binding rules:

1. Bindings resolve only after join requirements are satisfied.
2. A required missing source produces `RUN_INPUT_REQUIRED`; it is never coerced to
   `null`, `false`, or an empty value.
3. A missing optional source is absent from the input snapshot. If `default` is
   declared, that literal is used and type-checked.
4. Source and target types MUST be statically compatible. Runtime values MUST also
   match their declaration. There is no numeric or string coercion.
5. Each visit stores an immutable input snapshot. A retry reuses it. A checkpoint
   revision adds the prior candidate and feedback through daemon-owned metadata,
   without altering the original snapshot.
6. `node.*` resolves to the most recent successful visit in the current execution
   frame that happened-before the new visit. History remains addressable through
   runtime APIs but is not selectable by transition expressions in `v1alpha1`.

Artifacts are registered separately, but artifact handles can be ordinary typed
outputs. Artifact content is never embedded in workflow control flow.

## 6. Predicate language

`when` is a JSON expression tree. Omitted `when` means `{ "const": true }`.
Operands are literals or references to:

```text
output.<declared_output_name>[.<object_member>...]
input.<declared_gate_input_name>[.<object_member>...]
run.input.<declared_input_name>[.<object_member>...]
```

The complete operator set is:

| Operator | Arguments | Result |
|---|---|---|
| `eq`, `ne` | two values of the same type | Strict JSON equality or inequality. |
| `lt`, `lte`, `gt`, `gte` | two numbers or two strings | Numeric order or Unicode code-point string order. |
| `present` | one reference | Whether the reference resolves; a present `null` is present. |
| `all`, `any` | one or more predicates | Boolean conjunction/disjunction, evaluated in authored order. |
| `not` | one predicate | Boolean negation. |

There is no truthiness. A predicate always returns a boolean or a stable error.
Missing references are errors except as the argument to `present`. `eq` does not
coerce `1`, `1.0`, `true`, and `"1"`. Implementations evaluate operands in authored
order and return the first error, which makes error selection reproducible.

### 6.1 Reasoning assessments and deterministic gates

Reasoning output is evidence, never a control-flow decision. When an LLM evaluates
readiness, risk, quality, applicability, or any other gate-like concern, execution
MUST be split into two visits:

1. a `reasoning` node produces a typed assessment containing named scores,
   observations, confidence, and evidence references;
2. that assessment is durably recorded and bound as input to a `gate` node; and
3. the `gate` node applies a versioned deterministic condition and records
   `passed` plus `gate_evidence` before any branch is selected.

Illustrative normalized form:

```json
{
  "readiness_assessor": {
    "type": "reasoning",
    "outputs": {"assessment": {"type": "object"}},
    "transitions": [{"id": "assessment_to_gate", "to": "readiness_gate"}]
  },
  "readiness_gate": {
    "type": "gate",
    "inputs": {
      "assessment": {
        "from": "node.readiness_assessor.output.assessment",
        "type": "object"
      }
    },
    "outputs": {
      "passed": {"type": "boolean"},
      "gate_evidence": {"type": "object"}
    },
    "gate": {
      "policy": "project/readiness-v1",
      "condition": {
        "op": "all",
        "args": [
          {
            "op": "gte",
            "args": [
              {"ref": "input.assessment.completeness_score"},
              {"literal": 0.8}
            ]
          },
          {
            "op": "gte",
            "args": [
              {"ref": "input.assessment.evidence_score"},
              {"literal": 0.7}
            ]
          }
        ]
      }
    }
  }
}
```

Conditions on transitions leaving a `reasoning` node MUST NOT reference
`output.*`. A reasoning node may have one unconditional successor or may branch on
values that were already frozen independently of that attempt, such as explicit
human/project run inputs. LLM output MUST NOT be copied into mutable `run.input`
state to bypass this rule. `approve_on_change` checkpoint policy on reasoning
output likewise requires a downstream `gate` when its decision depends on an LLM
assessment.

A `gate` condition may reference only `input.*` and frozen `run.input.*`. Its
declared outputs MUST include `passed: boolean` and `gate_evidence: object`.
`gate_evidence` contains the policy identifier, condition digest, input-snapshot
digest, and result. Adding another evaluation score changes versioned gate policy,
not orchestration code or prompts.

## 7. Transition evaluation

Transitions are evaluated only after executor success, output validation, and any
checkpoint commit. They are evaluated against the committed output snapshot.

For `transitionMode: exclusive`:

- exactly one eligible predicate MUST be true;
- zero true predicates produce `RUN_EDGE_NO_MATCH`; and
- more than one produces `RUN_EDGE_AMBIGUOUS`.

Authored order never acts as hidden priority. If priority is desired, predicates
must be mutually exclusive by construction.

For `transitionMode: fanout`, every true transition fires, in authored order. Zero
true predicates produce `RUN_EDGE_NO_MATCH`. Fanout is the only way one visit can
activate more than one successor.

A fired transition appends a durable transition-token event before its target can
become ready. Replaying that event is idempotent by `(source_visit_id,
transition_id)`.

## 8. Joins

A node with more than one authored incoming transition MUST declare a `join` and
list every authored incoming transition ID. The frozen route projects that set onto
its included transitions; a projection of zero or one is an implicit single-input
join. There are two modes:

| Mode | Ready condition | Invalid condition |
|---|---|---|
| `all` | Every declared transition emitted one token for the join epoch. | Any declared transition closes without emitting after another has emitted: `RUN_JOIN_UNSATISFIABLE`. |
| `one` | Logical closure proves exactly one declared transition emitted. | Zero emits: node is not activated. More than one emits: `RUN_JOIN_MULTIPLE`. |

`one` is a deterministic merge of alternatives, not a race. It MUST wait for
logical closure; the first task to finish does not win. `all` is used after an
explicit fanout. A node with zero or one included incoming transition has an
implicit single-input join.

The scheduler calculates logical closure from durable predecessor and transition
state in the frozen route. If it cannot prove closure, it waits. Workflow static
validation rejects joins whose declared producers can never all be activated or
whose alternative merge can be proven to admit multiple tokens.

Each activation after a budgeted back-edge starts a new join epoch. Tokens never
leak between epochs.

## 9. Bounded cycles, repair, and retry

Every transition that can close a cycle MUST declare `kind: bounded` and a positive
`maxTraversals`. Static validation removes all bounded transitions and requires the
remaining graph to be acyclic. Therefore every directed cycle consumes at least
one finite budget and total visits are bounded.

Budgets are per transition, per execution frame. When a bounded predicate is true:

1. if budget remains, the counter is durably incremented and the transition fires;
2. if no budget remains, it does not silently become false; evaluation fails with
   `RUN_LOOP_LIMIT_EXHAUSTED`.

Node retry is different. A retry creates another attempt for the same visit, uses
the same input snapshot, and does not evaluate transitions or consume a bounded
edge. Only declared failure classes are retryable, and `maxAttempts` includes the
first attempt.

## 10. Checkpoints

Checkpoint modes are `none`, `acknowledge`, `approve`, `approve_on_change`, and
`external`. A checkpoint occurs after validators pass and before the visit succeeds.

| Action | Effect |
|---|---|
| approve / acknowledge / external satisfied | Commit candidate outputs, mark visit successful, then evaluate transitions. |
| request changes | Preserve the candidate, feedback, and audit actor; create a revision attempt for the same visit. No transition fires. |
| reject | Preserve the candidate and mark the visit rejected; the run waits for an explicit route/control decision. |
| duplicate same action key | Return the previously committed result without another transition. |

Provider command/file/network permission is never a checkpoint action. A provider
approval cannot commit node outputs or satisfy workflow policy.

If a route terminates at a checkpointed node, the run completes only after that
checkpoint commits. Moving the terminal while a checkpoint is open requires an
authorized route patch and does not approve or discard the candidate.

## 11. Terminal behavior and run completion

At route creation the selected terminal set is frozen. Every selected terminal MUST
be terminal-capable, reachable from the selected entry, and allowed by project
policy. Reachability is calculated with all enabled transition outcomes, not with a
single guessed predicate result.

When a successful visit is in the selected terminal set:

- it emits a terminal-reached event;
- it emits no outgoing transition token; and
- its branch is closed.

A run completes when at least one selected terminal was reached and every activated
branch is closed at a selected terminal. It fails deterministically if a live branch
has no matching transition, reaches a non-selected dead end, exhausts a bounded
edge, or makes a join impossible. Completion never means “the queue happens to be
empty.”

## 12. Sub-workflows

A `subworkflow` node declares an exact name and version (resolved to a digest at
installation), a child entry, child terminal set, and explicit input/output maps.

Execution:

1. the parent visit resolves and snapshots its inputs;
2. the daemon creates a child frame identified by the parent visit ID;
3. mapped child run inputs are copied by value or immutable artifact handle;
4. the child executes under its own counters, joins, checkpoints, and route revision;
5. child completion maps declared outputs to the parent candidate; and
6. child failure or waiting state propagates as the parent visit's corresponding
   state without pretending the parent succeeded.

The installed sub-workflow call graph MUST be acyclic. Recursive workflow calls are
not supported even if an inner route would appear bounded. Fanout over a collection
is also outside `v1alpha1`; product-story scheduling is a daemon aggregate concern,
not a hidden loop in the workflow language.

## 13. Route creation

Route selection precedence remains:

1. explicit human entry/terminal/range;
2. deterministic project mapping;
3. structured route-assessor proposal; and
4. workflow defaults.

The daemon constructs the candidate route by stopping traversal at the requested
terminal set. It then verifies:

- entry and terminals are declared eligible;
- every enabled possible outcome from a reachable non-terminal node stays inside
  the route and can reach a selected terminal;
- each frozen join is the projection of the authored join onto included incoming
  transitions and therefore contains every included incoming transition;
- required inputs come from run inputs, already accepted artifacts, or an included
  predecessor;
- bounded-edge and sub-workflow rules hold; and
- checkpoints and policy-required nodes were not bypassed.

Missing run-time input yields a structured waiting result. A structurally impossible
binding or route is a validation error.

## 14. Route patches

The schema is [`schemas/route-patch-v1alpha1.schema.json`](../../../schemas/route-patch-v1alpha1.schema.json).
A patch carries the run ID, expected route revision, rationale, and an ordered list
of these operations:

- `enableTransition` for a predeclared disabled transition;
- `disableTransition` for a predeclared enabled transition; and
- `setTerminals` to replace the future terminal set.

The patch language cannot author a predicate or node. A workflow that permits a
focused clarification/research/design insertion must predeclare the node and its
disabled transitions. Enabling that path and disabling the bypass edge inserts it.

Patch application is compare-and-swap and atomic:

1. `expectedRouteRevision` must equal the current revision;
2. no affected attempt may be running;
3. a transition that already emitted a token cannot be disabled;
4. a successful, active, or terminal-reached node cannot be removed from history;
5. explicit human terminal expansion requires an attributable approval;
6. the complete patched future route is revalidated; and
7. one event records old/new revisions, operations, rationale, actor, and validation
   digest.

Any failure rejects the entire patch. Patches do not change the workflow digest.

## 15. State transition tables

### 15.1 Run

| From | Event | To | Guard |
|---|---|---|---|
| `draft` | route frozen | `ready` | Workflow and route valid. |
| `ready` | start | `queued` | Required run input available or representable as a wait. |
| `queued` | visit ready | `running` | Lease acquired. |
| `running` | checkpoint/input/external wait | `waiting` | Durable wait reason exists. |
| `waiting` | wait satisfied/resume | `queued` | Idempotent control accepted. |
| `running` | non-retryable dependency | `blocked` | Structured blocker exists. |
| `blocked` | blocker resolved | `queued` | Resolution is durable. |
| `running` | terminal closure | `completed` | All activated branches closed at selected terminals. |
| `running` | deterministic execution error | `failed` | No automatic retry/repair remains. |
| `failed` | explicit retry | `queued` | Policy permits retry. |
| nonterminal | cancel | `cancelled` | Owned processes are reconciled and partial state recorded. |

Terminal states are `completed` and `cancelled`. `failed` is resumable by explicit
retry. Repeating the same transition command with the same idempotency key returns
the committed state.

### 15.2 Node visit

| From | Event | To |
|---|---|---|
| `pending` | entry/token/join satisfied | `ready` |
| `ready` | input snapshot committed | `running` |
| `running` | executor result received | `validating` |
| `validating` | validators pass, no checkpoint | `succeeded` |
| `validating` | validators pass, checkpoint required | `waiting_checkpoint` |
| `waiting_checkpoint` | approve/acknowledge/satisfy | `succeeded` |
| `waiting_checkpoint` | request changes | `running` (new revision attempt) |
| `waiting_checkpoint` | reject | `rejected` |
| `running` / `validating` | retryable failure | `ready` (same visit, new attempt) |
| nonterminal | cancel | `cancelled` |
| `running` / `validating` | exhausted/non-retryable failure | `failed` |

Only `succeeded` visits evaluate transitions. Candidate outputs from failed,
rejected, interrupted, or cancelled attempts are never bindable as successful
outputs.

### 15.3 Attempt

| From | Event | To |
|---|---|---|
| `created` | resources acquired | `starting` |
| `starting` | executor confirmed | `running` |
| `running` | result received | `validating` |
| `validating` | accepted | `succeeded` |
| active | classified error | `failed` |
| active | daemon loses ownership | `interrupted` |
| active | cancellation reconciled | `cancelled` |

Attempt states are append-only history. A retry always creates a new attempt ID.

## 16. Deterministic error contract

Every error has:

```json
{
  "code": "RUN_EDGE_AMBIGUOUS",
  "message": "node 'assess' matched 2 exclusive transitions",
  "location": "/spec/nodes/assess/transitions",
  "details": {"transitionIds": ["to_prd", "to_design"]}
}
```

`code`, `message` template, and JSON Pointer `location` are stable within the API
version. Detail object keys and identifier lists are lexicographically sorted.
Provider text and stack traces are evidence references, never the stable message.

| Code | Meaning |
|---|---|
| `WF_SCHEMA_INVALID` | Installed document violates the versioned schema. |
| `WF_REFERENCE_MISSING` | Node, transition, output, or sub-workflow reference does not exist. |
| `WF_BINDING_INCOMPATIBLE` | Declared binding types are incompatible. |
| `WF_REASONING_EDGE_INVALID` | A reasoning result is used directly for control flow instead of passing through a deterministic gate. |
| `WF_GATE_INVALID` | Gate policy, condition, or required evidence outputs are invalid. |
| `WF_UNREACHABLE_NODE` | A node is unreachable from every entry. |
| `WF_UNBOUNDED_CYCLE` | Removing bounded edges leaves a directed cycle. |
| `WF_JOIN_INVALID` | Join declaration is incomplete or statically impossible. |
| `WF_DEFAULT_ROUTE_INVALID` | Default entry/terminal route is invalid. |
| `WF_VERSION_CONFLICT` | Installed name/version already has another digest. |
| `WF_SUBWORKFLOW_RECURSION` | Installed call graph contains a cycle. |
| `ROUTE_ENTRY_INVALID` | Selected entry is absent or not entry-capable. |
| `ROUTE_TERMINAL_INVALID` | Selected terminal is absent, ineligible, or unreachable. |
| `ROUTE_PATH_INCOMPLETE` | A possible enabled branch cannot reach the boundary. |
| `ROUTE_POLICY_VIOLATION` | Selection bypasses a required control. |
| `ROUTE_PATCH_CONFLICT` | Expected route revision is stale. |
| `ROUTE_PATCH_PAST_EFFECT` | Patch tries to alter already committed execution. |
| `RUN_INPUT_REQUIRED` | Required binding source is absent. |
| `RUN_OUTPUT_INVALID` | Executor output is missing or has the wrong type/schema. |
| `RUN_PREDICATE_INVALID` | Predicate reference or operand type is invalid. |
| `RUN_EDGE_NO_MATCH` | No exclusive/fanout transition matched. |
| `RUN_EDGE_AMBIGUOUS` | Multiple exclusive transitions matched. |
| `RUN_JOIN_MULTIPLE` | A `one` join received more than one token. |
| `RUN_JOIN_UNSATISFIABLE` | An activated `all` join can no longer receive all tokens. |
| `RUN_LOOP_LIMIT_EXHAUSTED` | A true bounded transition has no budget remaining. |
| `RUN_DEAD_END` | A branch ended outside its selected terminal. |
| `RUN_CHECKPOINT_ACTION_INVALID` | Action is illegal for the current checkpoint state. |
| `RUN_INVARIANT_VIOLATION` | Durable state contradicts the frozen contract. |

When multiple static errors exist, validation sorts them by `location`, then `code`,
then canonicalized details. Implementations MUST NOT depend on map iteration order.

## 17. Validation algorithm

A conforming validator performs at least these ordered phases:

1. schema and canonical-value validation;
2. identifier and reference resolution;
3. input/output type compatibility;
4. transition-expression type checking;
5. entry, terminal, reachability, and dead-end checks;
6. join-set and join-satisfiability checks;
7. bounded-cycle check by deleting bounded transitions and testing for a DAG;
8. pinned sub-workflow resolution and call-graph cycle check; and
9. default-route construction and policy-independent validation.

All errors from a phase are collected and sorted. Later phases MAY be skipped when
an earlier failure makes them unsafe, but the result is identical for identical
bytes and installed dependencies.

## 18. Rejected alternatives

| Alternative | Reason rejected for MVP |
|---|---|
| String expressions such as CEL/JMESPath/JavaScript | Broader parsing/coercion surface, harder stable errors, and unnecessary language features. A typed expression tree covers routing needs. |
| Branching directly on an LLM readiness answer | Couples orchestration to a probabilistic response and makes later policy changes prompt-dependent. Assessments are persisted first and evaluated by versioned deterministic gates. |
| “First matching edge wins” | Author order becomes hidden priority and overlapping predicates silently change behavior. |
| Queue/race-based `any` joins | Completion timing would affect results. `one` waits for logical closure. |
| Arbitrary route-patch nodes or predicates | Makes model proposals executable workflow code and breaks version identity. |
| Unbounded or condition-only loops | A provider could keep a run alive indefinitely. Every cycle must consume a static budget. |
| Recursive sub-workflows | Adds a second form of unbounded execution and complicates recovery/version proofs. |
| Implicit artifact lookup or expression access to daemon state | Produces non-reproducible bindings and leaks orchestration internals into workflow definitions. |
| Treating `terminal: true` as a graph sink | Prevents the same node from serving design-only and end-to-end routes. Terminal capability and selected boundary are distinct. |

## 19. Executable reference

The standard-library-only reference interpreter is
[`scripts/workflow-reference.mjs`](../../../scripts/workflow-reference.mjs). It validates the
semantic rules, freezes an entry/terminal route, evaluates fixture-backed nodes,
executes joins and bounded edges, and invokes pinned local sub-workflow examples.
It is an executable specification, not production daemon code.

Run:

```text
node scripts/workflow-reference.mjs validate examples/workflows/*.json
node scripts/workflow-reference.mjs run examples/workflows/mvp-walking-skeleton.json --fixture examples/scenarios/mvp-walking-skeleton.json
node --test tests/workflow-reference.test.mjs
```

The examples encode the full default product graph, its story sub-workflow, the MVP
walking skeleton, and a custom split-design workflow. The tests also pin outcomes
for ambiguous transitions, missing inputs, terminal suppression, joins, bounded
loops, and sub-workflows.
