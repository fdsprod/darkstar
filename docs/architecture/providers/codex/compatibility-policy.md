# Codex compatibility policy

> [Documentation index](../../../README.md)

Status: decision-ready policy for DAR-5 / DS-001.

## Executable selection

DARKSTAR resolves a concrete `codex.exe`, records its canonical path, runs that
same path for health and attempts, and records the exact `codex-cli` version.
It must not probe one executable and execute another. This is necessary on
Windows because npm and desktop installations can both be present on `PATH`.

## Version support

- Support is declared for tested exact versions, then optionally widened to a
  semver range only after fixtures prove wire compatibility.
- App Server is experimental; every untested version is treated as unknown,
  not presumed compatible.
- Startup performs `initialize`, verifies required methods/capabilities, and
  fails closed when a required operation or semantic field is absent.
- Additive unknown notifications and fields are preserved and tolerated.
- Missing required fields, changed request identity, reordered lifecycle
  boundaries, or altered approval/cancellation semantics are breaking changes.
- Command-line flags are a separate compatibility surface from App Server
  schemas and observed JSONL events. Validate the exact commands used by the
  adapter even when generated schemas are unchanged.

## Fixtures and conformance

Fixtures live under `probes/codex-host/fixtures/<exact-version>/`. Each scenario
contains a redacted JSONL transcript and manifest. Generated protocol schemas
may be stored under `probes/codex-host/generated/<exact-version>/` as supporting
evidence, but observed wire fixtures are authoritative for behavior.

CI runs Fake, App Server, and exec through the shared provider conformance
suite, replays every supported App Server fixture through normalization, and
validates the full versioned Windows fixture corpus. A new Codex version is
admitted only after Windows probes cover its declared scenarios, the adapter's
exact CLI arguments are tested, and the fixture diff is reviewed.

The `.7.1` and `.7.2` sample generated 413 App Server schema files each with no
added, removed, or changed SHA-256 hashes. In the same patch transition,
`codex exec` rejected the previously exposed `--ask-for-approval` option. This
is recorded as an expected `cli-compatibility-drift` fixture and proves that a
schema-only gate is insufficient.

## Degradation and fallback

Capability detection is per operation. Optional missing capabilities disable
only dependent features. A bounded node may fall back to `codex exec --json`
only when policy allows it and the fallback can satisfy the node's output,
permission, cancellation, evidence, and recovery contract. Fallback is visible
in the attempt record; it is never silent.

The implemented exec gate is exact-version only. The default allowlist contains
`0.151.0-alpha.7.2`, whose Windows fixtures cover read-only structured output,
fresh-process resume, forced process interruption, and the rejected legacy
approval flag. Adding a version requires reviewing the corresponding exec
fixtures and updating the allowlist; a successful `--version` response alone is
not sufficient. Unreviewed versions report degraded health and cannot launch an
exec fallback attempt.

Authentication, permission denial, usage exhaustion, and an unsafe/unknown
write recovery state do not trigger automatic fallback to a new provider turn.
Doing so could duplicate cost or side effects.

App Server shutdown and recovery are also versioned semantics. Normal ownership
release requires a successful `thread/unsubscribe`. An `already has an active
writer` resume error is classified as an unsafe recovery state, not as an empty
thread or permission to start a duplicate attempt.

## Support states

Each installation is reported as one of:

- `supported`: exact/ranged version passed required conformance scenarios;
- `degraded`: core scenarios pass but named optional capabilities are absent;
- `unknown`: executable responds but version/fixture is unreviewed;
- `incompatible`: required handshake or semantics fail; or
- `unavailable`: executable, auth, or provider endpoint is unavailable.
