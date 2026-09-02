# Core ports and adapter package rules

> [Documentation index](../../README.md)

**Status:** Normative package contract for DS-021
**Scope:** `runtime/src` dependency direction and external-effect interfaces

## Decision

DARKSTAR uses application-owned ports. Deterministic code depends on narrow
interfaces and normalized values under `runtime/src/ports`; concrete provider,
storage, delivery, processing, and operating-system behavior depends inward on
those interfaces. A concrete adapter is never a dependency of core code.

The required external-effect port families are:

| Package | Owns |
|---|---|
| `ports/provider` | Provider health, capabilities, attempt lifecycle, normalized events, interaction, cancellation, result, and recovery metadata. |
| `ports/artifactstore` | Atomic immutable blob storage and opaque-locator read/stat/list operations. |
| `ports/delivery` | Remote branch publication and provider-neutral change-request observation/create/update operations. |
| `ports/contentprocessor` | Bounded, isolated derivation of immutable artifact representations. |
| `ports/platform` | Paths, locks, endpoint publication, process ownership, terminal, atomic-file, and executable-resolution strategy. |
| `ports/executor` | Scheduler-facing start/resume/events/result/cancel lifecycle for a node attempt. |
| `ports/repository` | Canonical repository identity, frozen base revisions, deterministic branch names, worktree inspection, conservative attachment, and non-destructive cleanup. |
| `ports/workflowstore` | Configured workflow discovery plus immutable installed-version and per-run snapshot persistence. |

Ports define capabilities and application vocabulary, not adapter mechanics.
They may use the Go standard library and shared `ports` values only. In
particular, a port must not expose a Codex message, GitHub response, SQL record,
Windows handle, CLI model, or dashboard type.

The normalized provider lifecycle has a machine-readable interchange contract in
[`provider-v1alpha1.schema.json`](../../../schemas/provider-v1alpha1.schema.json).

`core/attemptexecution` is the scheduler-facing orchestration boundary. It
acquires one closed read/write resource plan, freezes one context snapshot,
starts or resumes the selected `ports/executor`, journals its ordered events,
validates candidate output, and commits accepted output plus terminal state in
one idempotent transaction before releasing resources.

`core/agentprofile` owns DS-115's immutable agent profile and deterministic
provider-admission policy. A profile groups role instructions, required and
preferred capabilities, a closed any-compatible/allowlist eligibility choice,
provider preference order, permission ceilings, and bounded context, output,
cost, timeout, and cancellation limits. Selection consumes already-probed
provider health and capability manifests, rejects policy-denied, unhealthy,
malformed, or capability-incompatible candidates, and ranks the survivors by
provider preference, preferred-capability coverage, and stable configured ID.
Workspace-write and interactive permissions imply their corresponding provider
capabilities even when a profile author omits them from the explicit required
set. The result preserves the selected capability fingerprint, unavailable
preferences, and every rejection reason for scheduler audit; provider adapter
construction remains in daemon or command composition.

`adapters/executor/command` implements DS-114's deterministic command boundary.
Workflow commands select a configured executable alias, workspace-relative
working-directory allowlist entry, and allowlisted environment names; the
adapter pins exact executable paths, never invokes a shell, owns the full
process tree, bounds output and time, and records content-addressed evidence.
Terminal outcomes are a closed success/nonzero/timeout/cancel/output-limit/
uncertain union, with an exit code present only for a normal nonzero exit.
Command validators use the same boundary, receive candidate JSON on standard
input, run in declaration order, and return evidence for the attempt's atomic
commit.

## Dependency direction

```text
cmd / daemon composition
          |
          +--------------------------+
          v                          v
        core  --->  ports  <---  adapters/<port>/<implementation>
                     ^
                     |
               platform/<os>
```

The rules are:

1. `core/**` may import the standard library, `core/**`, and `ports/**` only.
2. `ports/**` may import the standard library and `ports/**` only.
3. Concrete adapter code lives under
   `adapters/<port>/<implementation>`; the `adapters` root is documentation-only.
4. Concrete operating-system code lives under `platform/<os>`; the `platform`
   root is documentation-only.
5. Adapter construction and selection belongs in `daemon` or `cmd` composition,
   never in core or a port package.
6. Each concrete implementation declares a compile-time interface assertion in
   its own package, for example `var _ provider.Provider = (*Adapter)(nil)`.
7. Adapters translate provider-specific values and errors at their boundary.
   Core branches only on normalized status and stable failure codes.
8. External mutations carry an application-issued operation/idempotency identity
   before dispatch and expose enough read-back evidence for reconciliation.

Adapters may depend on core domain values when mapping is required, but an
adapter does not call reducers, choose workflow transitions, authorize broader
permissions, or infer success from process exit or provider prose. It reports an
observation; core owns the state transition.

## Interface design rules

- Keep interfaces capability-focused. Add optional behavior as another interface
  instead of weakening an existing method with implementation-specific flags.
- Use `context.Context` for cancellation and deadlines; do not store it.
- Keep durable identities, digests, idempotency keys, and recovery references
  explicit in requests and results.
- Treat locators and external IDs as opaque. Only the owning adapter parses them.
- Streams are ordered and closeable. Closing observation does not imply
  cancellation of the underlying operation.
- Errors crossing a port are safe for logs and use `ports.Failure` when callers
  need a stable classification. Raw sensitive/provider detail remains durable by
  protected evidence reference when policy permits it.
- Interface changes are compatibility changes for every implementation and fake;
  update conformance tests with the contract.

## Enforcement

`runtime/tests/architecture/package_rules_test.go` parses every Go import under
`runtime/src`, verifies all seven required port packages exist, rejects forbidden core/port
dependencies, and reserves concrete package roots. Normal `go test ./...` and
the repository verification script run this check.

Stateful observations use explicit outcome variants. Provider results separate
success, failure, interruption, cancellation, and uncertainty; process and
branch observations carry exit codes or commit SHAs only in the matching exited
or found variant; and cancellation exposes one disposition instead of
independent booleans. Health states are authoritative observations rather than
summaries duplicated by `authenticated`, `canRead`, or `canPublish` flags.
