# Workflow authoring chat

The Workflows page has an **Edit with chat** panel beside the live canvas. Send
a request to create a workflow or edit the selected draft or published version.
The agent infers intent from the request and selection, choosing ordinary defaults
and asking only when a material ambiguity remains. Editing a published version first creates a user
draft with its base version retained. No installed version changes.

The agent can inspect the selected document and authoring catalog, create a
draft, replace its document, validate it, and ask a question with suggested
answers. Questions also support a freely written response. References come from
the catalog. The runtime treats `reasoning.agent` as a descriptive role label,
so an unavailable agent-reference catalog does not require agent configuration;
reasoning work uses `general-purpose`. A single-shot request can use the
provided task-input / implementation-node / changeset-output example directly. Actual
missing dependencies and conflicting requirements can produce a question.
Each successful save streams the
complete draft revision to the canvas. Existing layout is retained.

Publishing remains the editor's explicit human confirmation flow. The agent's
store interface has no publish, install, archive, or execution methods. Its
ephemeral Codex thread uses an isolated temporary working directory, read-only
sandbox, disabled shell/patch/web/app/plugin capabilities, disabled inherited MCP
servers, and rejects all requests other than the five authoring dynamic tools.
The local API bearer token is never included in the provider prompt.

The client disables manual editing and publishing during an active chat turn.
Every agent save still compares the expected server revision, protecting against
another browser or API client. A conflict preserves the server's newer revision
and stops further agent edits for that turn. Use the existing conflict controls
to choose the revision to continue from, then send the reconciliation request.
Validation evidence is tied to the exact saved document and becomes stale after
an edit.

`POST /api/v1/workflows/chat` is an authenticated NDJSON stream defined in the
OpenAPI contract. The tagged target is a new workflow, exact draft revision, or
exact installed name/version. Events are status, text, draft, validation,
question, conflict, error, and done. The browser carries up to 100 conversation
messages; chat history is currently held in page memory. Drafts are durable.
Stopping, disconnecting, or reaching the ten-minute turn timeout cancels remaining
work without undoing saved revisions. Review the draft before retrying after a
connection failure. Chat uses the daemon's configured Codex executable and local
provider authentication; provider errors appear in the panel.

The composer includes Send, model, and reasoning-effort controls. Enter sends;
Shift+Enter inserts a newline. `GET /api/v1/workflows/chat/models` reads the
configured provider's model catalog and supported efforts. Model changes reset
effort to that model's default. An explicit model/effort pair is checked against
the current provider catalog and passed to the authoring turn. Omission uses
provider defaults. These controls govern the authoring conversation; they do not
change workflow execution configuration.

Model and effort preferences persist in a host-scoped browser cookie, including
across local daemon port changes. The provider catalog validates restored
choices. Canvas layout recalculates when its topology or card dimensions change;
manual positions remain until the next such change. Execution wires stay behind
cards. Green Start and Done endpoints show the default route boundaries.
Done is derived from `routeDefaults.terminals`, not another executor. A run's
frozen route can select other boundaries; successful terminal completion emits
`run.completed` and `work.completed` automatically.

Verification includes Go tool-boundary and fake App Server protocol tests,
stream-decoder tests, and a browser test covering automatic forking, live canvas
updates, question replies, and human-only publishing. An opt-in
`TestLiveWorkflowChat` uses `DARKSTAR_CHAT_LIVE_CODEX` to exercise the authenticated
provider against a temporary database; it consumes model usage and is skipped
by default.

`TestLiveSingleShotIntent` reproduces the plain-language single-shot request
against an existing selected draft and checks for one valid implementation node,
a changeset output, no question, and no publishing. It is also opt-in.

The target and event contracts are closed unions, so a draft revision cannot be
confused with an immutable version and a question cannot masquerade as a save.
The workflow store remains the authority for draft revisions and validation;
the canvas consumes confirmed saves instead of maintaining a second agent-owned
workflow document.

## Implementation nodes

The v1alpha3 `implementation` node performs work in the run's Git workspace.
It requires a task input and declares `process.run` and `workspace.write`.
Supporting Markdown files can be connected as additional inputs; a point plan
is optional. Its instructions direct the provider to modify actual files, run
appropriate checks, preserve unrelated work, and avoid committing or publishing.

The required `changeset` output contains `disposition` (`changed`, `unchanged`,
or `blocked`), `summary`, `files`, and `validation`. The daemon retains a file
baseline per attempt across reconnects. `inspect_workspace_changes` exposes
the added, modified, and deleted paths. Submission and final completion both
check the reported file list against disk. A blocked result fails completion.

Evidence covers tracked and nonignored untracked files in a Git repository;
preexisting changes are part of the baseline. This verifies file differences,
not semantic correctness or the provider's reported checks. Concurrent outside
edits can also appear in the differences. Review the resulting changeset.

## Explicit workspaces and required checks

Add **Prepare workspace** and connect its required `repository` input to the
run's repository resource. Choose `current_checkout` or `new_worktree`. The
latter requires a base ref and new branch; `{runId}` in the branch name expands
to the run identity. The daemon freezes the base commit, creates a managed
worktree, and retains its record across restarts. It never substitutes main or
master, overwrites an existing branch, or discards edits when reusing a workspace.
Worktrees and branches are retained for review; automatic cleanup is not included.

Connect the typed `workspace` output to Implementation's `workspaceInput`, and
wire execution from Prepare workspace to Implementation. New draft publications
require this connection. Existing installed versions retain legacy checkout
execution until explicitly revised. Repository inputs identify the authorized
work-item project; arbitrary filesystem paths do not authorize another repository.

Add **Validate workspace** after Implementation and connect the same workspace.
Configure checks as argument arrays, for example `[["git","diff","--check"]]`.
This example checks whitespace only; configure project tests when required.
Each command runs in the prepared checkout, with a two-minute limit and bounded
captured output. A nonzero exit or timeout fails the node. Make it the route's
terminal to require passing checks before Done. Published checks are executable
commands, so review them as part of the human publishing step.

Connector requirements and component instructions are defined in
`runtime/src/core/workflow/component_requirements.json`, shared by the editor,
core validation, and chat inspection. Canvas findings appear after edits, chat
saves automatically emit validation, and publication enforces required bindings.
Runtime checks repository identity, branch/head consistency, and run ownership
again before using a prepared workspace. Workspace reference payload paths are
informational; daemon-owned records are authoritative.
