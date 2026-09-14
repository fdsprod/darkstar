# Repository investigations

An investigation collects findings from the repositories in one frozen scope,
then synthesizes their impact. The daemon controls work units, bounded concurrency,
validation, retries, and cancellation. Preparing a collection starts no reasoning
attempt. It preserves the selected scope, task, provider implementation, and
capability fingerprint for later execution.

First prepare a scope with `project scope prepare` as described in
[investigation scopes](investigation-scopes.md). Then create a request file:

```json
{
  "scopeId": "scope_01K00000000000000000000001",
  "task": {
    "kind": "text",
    "text": "Investigate the interfaces and constraints involved in sharing saved searches across these systems."
  },
  "concurrency": 2
}
```

Use the same daemon commands from the CLI or API:

```text
darkstar investigation prepare request.json --idempotency-key feature-investigation-01 --json
darkstar investigation show <investigation-id> --json
darkstar investigation start <investigation-id> --revision <current-revision> --idempotency-key start-investigation-01 --json
darkstar investigation retry <investigation-id> --revision <current-revision> --idempotency-key retry-investigation-01 --json
darkstar investigation cancel <investigation-id> --revision <current-revision> --idempotency-key cancel-investigation-01 --json
```

The prepare request accepts concurrency from 1 to 8. Omission or zero selects 2;
the daemon also enforces its global limit. `show` returns the current collection
revision, retained attempts, every unit's state and reason, and exact result
artifact references. Start, retry, and cancel require that revision. A stale
command fails explicitly; reload the collection before choosing another command.
Replaying the same command requires its original idempotency key and revision.

To investigate an existing feature brief, replace the text task with an exact
reference. The daemon checks the artifact's bytes, schema, project identity, and
repository catalog before freezing the task:

```json
{
  "kind": "feature_brief",
  "featureBrief": {
    "artifactId": "artifact_01K00000000000000000000001",
    "version": 3,
    "sha256": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
  }
}
```

A repository finding includes exact commit, blob, file, and line references,
affected interfaces, reusable patterns, constraints, risks, questions, and
limitations. The daemon validates references against the retained snapshot.
Synthesis cites accepted findings by immutable artifact version and digest and
retains explicit missing evidence. The result artifacts can be consumed by later
feature-planning steps; they do not approve a plan or publish tickets.

Retries preserve successful repository units and prior attempts. They retry
eligible failed or cancelled work and refresh synthesis when its inputs change.
An uncertain provider attempt requires reconciliation before another attempt can
run. Cancellation stops new admissions and retains partial results and history.

Empty repository scopes remain explicit. They never imply a checkout or complete
repository coverage. The daemon produces a synthesis recording
`no_repository_evidence` without starting a provider. Repository reasoning attempts
require a provider that enforces the scoped filesystem contract. Bundled Codex
transports currently report that capability unavailable; selecting read-only mode
or supplying workspace paths does not satisfy it. The collection exposes the
resulting failure instead of broadening access.

The HTTP surface is `POST /api/v1/investigations`, `GET` or `HEAD
/api/v1/investigations/{id}`, and `POST /api/v1/investigations/{id}/start`, `/retry`,
or `/cancel`. Mutations require `Idempotency-Key`; controls also require a quoted
positive `If-Match` revision and an empty JSON object body. The public API exposes
no unit-result submission, arbitrary provider selection, or tool invocation.

Investigation findings and synthesis retain their exact artifact IDs, versions,
and digests. Read these through the versioned artifact contract:

```text
darkstar artifact list-v2 --json
darkstar artifact show-v2 artifact_example@1 --json
darkstar artifact content-v2 artifact_example@1
```

The corresponding endpoints are `GET /api/v1/artifacts-v2`,
`GET /api/v1/artifacts-v2/{artifactId}?version=1`, and
`GET /api/v1/artifacts-v2/{artifactId}/content?version=1` (also supporting HEAD).
These use `artifact-v1alpha3.schema.json` and preserve investigation provenance.
The original artifact list includes only origins supported by its existing
contract; an exact legacy read of an investigation artifact returns an actionable
409 directing the caller to `artifacts-v2`. Editing investigation artifacts is
not exposed by these read routes. The CLI checks the downloaded content digest;
`content-v2 --json` returns base64 content with the exact artifact reference.
