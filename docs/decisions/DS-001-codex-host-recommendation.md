# Codex host recommendation

> [Documentation index](../README.md)

Status: accepted recommendation for DAR-5 / DS-001.

## Decision

Use a hybrid adapter with Codex App Server over stdio as the primary transport
and `codex exec --json` as a bounded fallback.

App Server passed the complete required Windows path: authenticated handshake,
structured read-only and approved write turns, streaming, command and network
approval, tool-driven user input, skills, local images, usage/rate-limit
signals, graceful interruption, forced process interruption, and fresh-process
resume.

Exec passed structured read-only execution, streaming JSONL, session resume,
forced-process interruption, and resume after that interruption. It did not
provide the tested bidirectional approval/input surface, and a patch release
rejected a command-line flag exposed by the immediately prior build. Therefore
it is allowed only for version-gated nodes whose permission, interaction,
result, cancellation, evidence, and recovery requirements it can fully meet.

## Rejected alternatives

- **App Server only:** rejected because exec remains a useful simpler fallback
  for bounded, non-interactive nodes and supplies recoverable session identity.
- **Exec only:** rejected because it does not meet the proved interactive
  approval and user-input contract required for the primary/write path.
- **Automatic silent fallback:** rejected because retrying after auth,
  permission, exhaustion, or an uncertain write outcome can duplicate cost or
  side effects.

## Required implementation rules

1. Pin and record the canonical executable plus exact `codex-cli` version.
2. Probe App Server capabilities at startup and preserve unknown events.
3. Send and record `thread/unsubscribe` before normal App Server shutdown.
4. On interruption, retain thread/turn IDs and reconcile workspace evidence
   before retrying a write-capable attempt.
5. Treat `already has an active writer` as pause/reconcile, never as permission
   to create a duplicate turn.
6. Gate exec by exact-version command probes as well as event fixtures; do not
   infer CLI compatibility from unchanged App Server schemas.
7. Record which transport was selected and why in every attempt.

## Evidence

The authoritative observed evidence is the redacted corpus under
`probes/codex-host/fixtures/`, summarized by
`probes/codex-host/PROBE_MATRIX.md` and validated by
`probes/codex-host/Test-CodexHostFixtures.ps1`.
