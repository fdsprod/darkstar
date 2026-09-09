# Grounding and architectural boundaries

Before changing behavior, read the relevant sections of
[the product specification](docs/product/product-specification.md) and the
linked architecture contract. Explain conflicts instead of silently changing
the product model. Preserve existing user capabilities when replacing a view.

## Deterministic execution

- DARKSTAR's daemon owns input binding, scheduling, gates, retries, validation,
  approvals, Git/worktree preparation, delivery, and terminal state transitions.
- Execution agents perform one scoped reasoning or implementation task. Supply
  resolved inputs, task instructions, granted tools, and an output contract.
  Do not supply the workflow graph, unrelated run inputs, all prior outputs, or
  instructions asking the model to orchestrate the workflow.
- A model's success claim is evidence. Runtime validation determines success
  and advancement. Human approval cannot be replaced by model text.
- Workflow authoring chat is a separate capability: it may inspect and edit
  drafts, including creating a version from a read-only workflow. Publishing
  remains human-only. Never conflate draft editing with execution authority.
- Enforce these boundaries with code and behavioral tests, not prompts alone.

## Human interaction

- Artifact review includes version-bound annotations, agent-assisted revision,
  readable diffs, and human decisions. A read-only document viewer does not
  replace that workflow.
- Preserve saved transcripts and artifact revisions. Rendered views are
  projections; raw history remains accessible and unchanged.
- Distinguish daemon-created agent inputs from messages actually sent by a user.

See [execution semantics](docs/architecture/workflow/execution-semantics.md),
[artifact contracts](docs/architecture/artifacts/ARTIFACT_AND_CONTEXT_CONTRACT.md),
and [approval contracts](docs/architecture/security/APPROVAL_AND_PERMISSION_MODEL.md).

## UI composition

Build review UI from focused, controlled components (toolbar, contents, Markdown blocks, annotations, composer, decisions). Keep API calls, routing, selection anchoring, and revision orchestration in wrappers. Pure components accept data and callbacks for isolated previews and behavioral tests.

Markdown authoring follows [Rich Artifacts](skills/builtin/rich-artifacts/SKILL.md). The daemon bundles and loads it for Markdown-producing attempts, including revisions; it does not delegate workflow control to the model.

Execution context contains declared node inputs only, delivered once with their input names. Keep runtime record IDs out of task-content projections. Tool-backed attempts submit outputs through submit_output; the daemon assembles and validates those durable submissions. Do not require a second copy in final assistant JSON. Render document values separately from transport envelopes, for every output name, while preserving raw history.
