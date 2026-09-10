# Node execution code

Start with `<type>_node.go` to inspect a node's behavior. Each implemented node
has deterministic tests beside it. `registry.go` is the executable support
registry; schema declarations alone do not make a type executable.

| File | Responsibility |
| --- | --- |
| `reasoning_node.go` | Read-only task instructions and capability requirements. |
| `implementation_node.go` | Task instructions, changeset schema, prepared-workspace binding, baseline capture, and changeset evidence validation. |
| `point_execution_node.go` | Existing agent-backed point instructions and progress schema. |
| `workspace_prepare_node.go` | Checkout selection, frozen base, durable workspace reuse, and worktree attachment through injected services. |
| `workspace_validate_node.go` | Required checks in the resolved workspace, timeout policy, failure propagation, and evidence. |
| `gate_node.go` | Deterministic predicate evaluation and gate evidence. |
| `command_node.go` | Existing restricted `darkstar/story-execution` validation compatibility behavior. |
| `approval_node.go` | Explicit unsupported standalone execution boundary; artifact checkpoints remain supported by the daemon. |
| `subworkflow_node.go` | Explicit unsupported production dispatch boundary. |
| `routing_node.go` | Explicit unsupported production dispatch boundary. |

## Execution boundaries

Agent handlers construct a scoped task and specialize its output schema.
Deterministic handlers execute a bounded operation through supplied services.
These are separate interfaces, not one executor with optional agent/process
fields. `AgentTask.Access` is a requirement; the daemon remains responsible for
granting access and creating the provider request.

The daemon resolves declared inputs, authorizes the project/workspace, persists
attempts and tool submissions, validates results, manages approvals and retries,
and fires transitions. Node handlers cannot update run state. Agent tasks contain
no workflow graph or scheduler context. Workspace identity fields are daemon
metadata used for durable ownership, not agent instructions.

The Go node configuration types and serialization remain in `core/workflow`.
The CLI composition layer wires storage, Git, and process services. Workspace
authorization, SQLite records, and legacy relocation remain in `cli/workspaces.go`;
bounded process execution is in `adapters/executor/nodeprocess`. Codex protocol
handling remains in its provider adapter. `workflowtools` retains durable output
submission and baseline storage and calls the implementation validator here.

## Compatibility limits

This extraction preserves existing prompts, output shapes, workspace record
formats, command allowlisting, validation timeouts, and checkpoint behavior.
It does not install build dependencies, add delivery authority, or implement the
unsupported control-node lifecycles. Unsupported nodes fail before provider
invocation. The restricted command node still reports the legacy acceptance
evidence; changing that behavior requires a separate change.

Use `workspace_validate` for configured repository checks. Workspace preparation
creates/reuses a checkout; it does not provision dependencies. The current
point-execution handler constructs an agent task and is not a replacement for
daemon enforcement of a full per-point delivery policy.
