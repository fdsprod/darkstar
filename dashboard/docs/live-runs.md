# Live runs

Run pages open on the terminal transcript. Turns groups the same persisted
history by observable model action (agent messages and tool calls), and Artifacts shows submitted Markdown, repository
Markdown snapshots, journal decisions and open items, structured results, and
attached artifacts. Old evidence links open Artifacts.

The timeline shows colored event types across the whole run. Drag across it to
select a time range, drag either handle to resize, or drag the selected window
to move it. Arrow keys adjust a focused handle. The From/To inputs provide exact
bounds. Reset range or Follow live returns to the full history. Scrolling away
from the latest output stops automatic scrolling. Search and event filters
apply to the selected range. Long transcripts render in batches; Show more
exposes the next batch without losing persisted history.

The authenticated transcript API pages committed events by global position;
status refreshes avoid loading provider payloads. The viewer coalesces item
deltas and completion snapshots. Input/output/reasoning token counts use the
latest provider totals per thread. Unreported counts are shown as unknown.
Only provider-exposed reasoning summaries are rendered in the readable view.
Raw events remain inspectable and the loaded complete history can be exported
as NDJSON. Older digest-only events are recovered from immutable provider
observations where available; missing observations are explicitly identified.

Markdown snapshots are taken after completed file changes and shell commands
against the connected workspace. Capture excludes unchanged baseline files,
ignored files, paths outside the workspace, non-text files, and files larger
than 1 MiB. Snapshots and journal history live in daemon-owned SQLite storage
and survive worktree edits or removal. Existing runs retain their submitted
outputs; intermediate file revisions predating snapshot capture cannot be
reconstructed.

Markdown checkpoint outputs are ingested with their producing attempt and exact
run/node identity, attached to the run, and extracted for version-bound annotation.
Artifacts embeds the existing review workspace, feedback sets, revision history,
and diffs. Feedback launches a read-only document revision task with only the
candidate, human feedback, and a document output contract. Each new candidate
returns to human review, without a revision cap or automatic approval. All
documents from the visit must have their current version explicitly approved
by a user before their contents replace the output snapshot and execution advances.
Repository file snapshots remain historical captures; revising a document does
not execute its implementation plan or edit a repository file.

Recovery idempotently registers older paused outputs and finishes a saved
revision response without another model call. The coarse legacy approval is
retired only after exact review requests exist. Stop cancels pending reviews.

Non-document workflow checkpoints create an approval in the same transaction as waiting.
Inline actions and the global queue share the same decision handler. Approval
commits the decision, advances the visit, and either schedules the successor
or completes the run and work item. Resume cannot bypass a checkpoint, and Stop
closes its pending approval. Startup repairs missing approvals from older
waiting visits without approving them or launching work.

Human messages are committed before active-turn steering. Successful delivery
means the provider accepted the message for that turn. Unacknowledged delivery
is retained as unconfirmed and is never automatically resent. Messaging requires
an active agent; waiting reviews and questions use their inline controls. Stop
uses the existing provider cancellation and reconciliation path.

Suggested workflow-chat answers submit directly. Workflow publishing remains
human only.

The Turns count includes distinct agent messages and tool invocations, not user
exchanges. Deltas, completion snapshots, and results do not increase it. This is
an observable activity count; provider-internal inference boundaries are not
available and are not inferred as exact model request counts.

## Tool-owned execution results

For tool-backed attempts, final assistant prose is a human summary. It is not the result contract. The daemon reads the latest validated `submit_output` for each declared output, rejects missing required outputs, and validates the assembled result against the retained output schema. No final JSON schema is sent to the model for these attempts. Non-tool-backed providers retain their structured-response contract.

Inputs are filtered against the node's declared bindings. Each appears once as a named context item; `read_input` exposes the same scoped values. Runtime work/project IDs remain in canonical records but are excluded from task-content projections. Revision attempts explicitly declare candidate and feedback inputs, and feedback contains the instruction and annotations rather than the review lifecycle record.

Formatted transcripts decode complete JSON envelopes into named document values generically. They do not perform blind escape replacement. Raw mode and saved events retain the original response; terminal command output remains a terminal block.
