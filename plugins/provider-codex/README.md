# Codex provider plugin

This TypeScript plugin translates DARKSTAR's provider lifecycle into Codex App
Server RPC. It owns the initialize handshake, thread/turn mapping, normalized
events, dynamic-tool translation, permission-response translation, health
observations, resume, timeout, and cancellation behavior.

The Go provider bridge starts the digest-pinned bundle and, on Windows, owns its
process tree through a kill-on-close job.
It supplies the executable and environment, scoped attempt requests, and a small
set of host callbacks. The plugin cannot add tools or approve an interaction by
calling a daemon host service. Native permission requests become checkpoint
events; the daemon sends the corresponding human decision back to the plugin.

Host callbacks are bound to an attempt:

- `tool.call`: invokes a tool already granted to that attempt.
- `outputs.resolve`: assembles durable tool submissions.
- `markdown.capture`: snapshots workspace changes after file/command events.
- `evidence.record`: persists raw native events before exposing normalized events.

Provider results are evidence; the Go runtime independently validates output
contracts and advances the workflow. Tool-backed attempts do not ask the model
to repeat its submitted values in final JSON. Unknown native notifications remain
available as raw evidence and `unknown.provider_event` projections.

The supported App Server versions match the existing reviewed Go adapter:
`0.151.0-alpha.7.1`, `0.151.0-alpha.7.2`, and `0.153.4`. Unreviewed versions fail
closed. Network policy remains `denied`, matching the existing execution
adapter. Workflow-authoring chat remains a separate capability.

Build and check the self-contained bundle:

```sh
node plugins/provider-codex/build.mjs
node plugins/provider-codex/build.mjs --check
node --test plugins/provider-codex/tests/provider.test.mjs
go -C runtime test ./src/adapters/provider/typescript
```

Tests launch a local App Server fixture, never a signed-in Codex session. They
exercise the TypeScript implementation directly and through the Go bridge.
