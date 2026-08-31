# Codex adapter contract

Status: decision-ready contract for DAR-5 / DS-001.

## Decision boundary

DARKSTAR core depends on this contract, never directly on Codex App Server or
`codex exec --json` types. A transport adapter may expose more raw evidence,
but it must implement the same normalized lifecycle and error semantics.

The selected architecture is a hybrid: App Server over stdio is the primary
transport and `codex exec --json` is a bounded, visible fallback. App Server is
the only tested transport that satisfies the full interactive Windows contract;
exec is suitable only when a node does not require provider approvals, user
input, or an uncertain write recovery decision.

## Provider operations

| Operation | Required behavior |
|---|---|
| `ProbeHealth` | Report executable identity, exact version, platform, authentication state, reachability, capabilities, and actionable failures without exposing credentials. |
| `StartAttempt` | Start a read-only or write-capable attempt in one declared workspace with an immutable attempt ID and requested capability/permission manifest. |
| `ResumeAttempt` | Resume by provider thread/session ID when supported; otherwise return an explicit unsupported or unrecoverable result. |
| `StreamEvents` | Preserve event order, provider IDs, raw event references, and unknown events while producing normalized DARKSTAR events. |
| `Respond` | Answer exactly one provider approval, permission, tool, or user-input request using its opaque request identity. |
| `CancelAttempt` | Interrupt the active turn, then terminate only the owned process tree if graceful interruption does not complete within policy. |
| `GetResult` | Return terminal status, structured result or validation failure, usage, workspace evidence, and recovery metadata. |

## Attempt request

A provider-neutral attempt request contains:

- DARKSTAR attempt, run, node, and idempotency IDs;
- absolute canonical workspace and additional allowed roots;
- access class: `read_only` or `workspace_write`;
- network policy independent of filesystem policy;
- command/file/network/tool approval policy;
- model and reasoning preferences as optional hints;
- prompt plus typed text, image, skill, and artifact inputs;
- optional JSON output schema;
- timeout, cancellation grace period, and usage limits; and
- provider-thread ID only for an explicit resume.

The adapter must not infer broader permissions from the prompt, inherited Codex
configuration, a workflow approval, or a previous attempt.

## Normalized event envelope

Every event has:

```json
{
  "schemaVersion": 1,
  "attemptId": "opaque-darkstar-id",
  "sequence": 1,
  "occurredAt": "provider timestamp or host receive time",
  "kind": "attempt.started",
  "provider": "codex",
  "providerVersion": "exact codex-cli version",
  "providerThreadId": "optional opaque id",
  "providerTurnId": "optional opaque id",
  "providerItemId": "optional opaque id",
  "payload": {},
  "rawFixtureRef": "optional durable evidence reference"
}
```

Required event kinds are:

- attempt lifecycle: `attempt.started`, `attempt.waiting`,
  `attempt.completed`, `attempt.failed`, `attempt.cancelled`;
- turn lifecycle: `turn.started`, `turn.completed`, `turn.interrupted`;
- output: `message.delta`, `message.completed`, `plan.updated`,
  `structured_output.completed`;
- tools and changes: `command.started`, `command.output`,
  `command.completed`, `file_change.started`, `file_change.completed`,
  `tool.started`, `tool.completed`;
- interaction: `permission.requested`, `user_input.requested`, and matching
  response-recorded events; and
- accounting/diagnostics: `usage.updated`, `warning`, `error`,
  `unknown.provider_event`.

Unknown provider messages are retained and surfaced as
`unknown.provider_event`; they are never silently discarded.

## Interaction invariants

Workflow checkpoints and provider interactions are separate types. A provider
command/file/network approval cannot approve a DARKSTAR artifact or delivery
checkpoint. Each provider request uses the provider's opaque request ID plus a
DARKSTAR idempotency key and accepts at most one recorded response.

Denial, expiry, cancellation, and host disconnection are distinct outcomes.
Session-scoped grants must be explicit, bounded to the owning attempt/thread,
audited, and never reconstructed from free-form text.

## Completion and recovery

An attempt is successful only when the provider turn completes, the requested
structured result validates, and required workspace evidence is captured.
Process exit alone is not success.

Persist provider thread/turn IDs and the last durable event sequence before
acknowledging completion. After host restart:

1. inspect the owned process identity and durable provider IDs;
2. reconnect/resume when the transport proves that this is safe;
3. otherwise classify the outcome as interrupted/unknown and pause or start a
   new explicit attempt according to policy; and
4. never assume an unobserved write-capable turn failed without reconciling the
   workspace.

For a normal App Server shutdown, send `thread/unsubscribe` and record its
success before closing stdio. A completed thread released this way resumed in a
fresh process. Threads also resumed after graceful `turn/interrupt` and after
the owning App Server process was forcibly killed once the turn had started.

A legacy thread whose owner exited without `thread/unsubscribe` failed resume
with `already has an active writer`. Treat this as an unsafe recovery state:
pause, retain the provider IDs and workspace evidence, and require reconciliation
or an explicit retry decision. Do not create a replacement write turn
automatically.

Exec session IDs survived a fresh-process resume and a forced-process kill in
the bounded read-only probe. Exec does not supply the tested bidirectional
approval/user-input bridge, so this recovery result does not widen its policy
scope to interactive or write-capable nodes.

## Error classes

Adapters map provider details into: unavailable, unauthenticated, usage limit,
rate limit, invalid request/schema, permission denied, tool/process failure,
timeout, interruption, cancellation, protocol drift, and internal adapter
failure. The original safe provider error is retained for diagnosis.

## Proven boundary and deferred tests

The Windows corpus proves structured read-only and approved write attempts,
fresh-process resume, command/network approval, tool-driven user input, image
and skill inputs, graceful interruption, forced process interruption, token and
rate-limit signals, and the bounded exec fallback.

Live account exhaustion was not induced because it would consume or disrupt the
configured account. Exhaustion classification remains a deterministic synthetic
adapter/conformance test. It is not required to choose the host transport, and
it must never trigger automatic fallback after a possibly side-effecting turn.
