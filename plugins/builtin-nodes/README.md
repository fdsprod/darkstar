# Built-in node plugin

The `darkstar/builtin-nodes` TypeScript package implements reasoning,
implementation, point-execution, command, workspace-prepare, and
workspace-validate contributions through the public node invocation contract.

Each node lives in its own module under `src/`: `reasoning.ts`,
`implementation.ts`, `point-execution.ts`, `command.ts`, `workspace-prepare.ts`,
and `workspace-validate.ts`. `index.ts` only registers the contributions;
`shared.ts` contains the invocation contract, descriptor helper, and shared schemas.

Agent nodes construct instructions, requirements, and output schema refinements.
Deterministic nodes translate their configuration and bound inputs into scoped
host calls and produce candidate output values. They receive no workflow graph,
run input collection, approval API, or scheduler state.

The daemon authorizes each host operation against the original node configuration.
It retains Git ownership/preparation, implementation baselines, mandatory checks,
output validation, approvals, retries, and state transitions. Gates also remain
daemon-owned. Approval, routing, and subworkflow nodes retain their existing
explicit unsupported execution behavior.

`workspace.prepare` accepts only the connected repository and declared checkout
plan. `workspace.resolve` accepts only the connected workspace. `process.run`
accepts only the next declared validation command with its host-enforced timeout;
the historical command-node allowlist is unchanged. Successful plugin narration
cannot substitute for completed mandatory host operations.

Generate the embedded single-file JavaScript bundle with
`node plugins/builtin-nodes/build.mjs`. Use `--check` to verify freshness. The bundle
includes the SDK, and the daemon pins and verifies its SHA-256 digest.

Run real-process integration tests with
`go -C runtime test ./src/adapters/nodeextension/typescript` and typecheck using
`node packages/plugin-sdk/node_modules/typescript/bin/tsc -p plugins/builtin-nodes/tsconfig.json`.

## Typed workflow contracts and execution kind

Every executable node contribution declares `executionKind: 'llm' | 'deterministic'`.
The SDK rejects missing declarations, and the daemon rejects calls that dispatch
an operation through the wrong execution interface. The canvas uses the shared
built-in execution metadata for its badges. Resource cards display value types.

New worktrees may use the reserved `baseRef: "project_default"` selector. The daemon
resolves the project's `workspace.baseRef` setting (default `origin/HEAD`) and
persists the resolved ref and commit before creating the worktree. An explicit
full Git ref remains available, including `refs/heads/project_default` if a
repository actually uses that branch name. Retrying preparation reuses the
persisted base even if project settings have changed.

The built-in nominal value registry is
`runtime/src/core/workflow/value_schemas.json`. New drafts must use nominal
structured types, and a type identity cannot be redefined with a different schema.
Historical installed versions retain their legacy transport contracts. Built-in
schemas are applied both to model output constraints and daemon validation.

Implementation emits `schema:changeset_v1`; the daemon adds a content snapshot
digest to its durable submission. The `delivery-text` reasoning role selects
`delivery-text.ts`, which consumes the connected changeset summary and recorded
checks and returns `schema:delivery_text_v1` without rereading the full repository.
Its `changesetSnapshot` must match the connected changeset. This is descriptive
content only; it grants no commit, push, or pull-request authority.


## Standalone delivery

`git_commit` consumes Workspace, Changeset, and DeliveryText and emits Commit.
`git_push` consumes Workspace and Commit and emits PublishedBranch.
`create_pr` consumes Workspace, PublishedBranch, and DeliveryText and emits
PullRequest. These deterministic nodes impose no validation or approval steps.
Authors add those explicitly. The PR target defaults to `remote_default` and its
`draft` choice is explicit. The current connector supports GitHub remotes.

The daemon pins each visit's mutation intent before executing it. Commit retries
reconcile exact tree, parent, and operation trailers; push uses the recorded remote
expectation; PR creation reconciles exact coordinates and owned content. A stale
changeset or changed delivery text cannot silently alter an existing intent.
