# DARKSTAR Crash Recovery and Idempotency Model

> [Documentation index](../../README.md)

**Decision:** DS-003  
**Status:** Proposed normative contract for MVP  
**Scope:** Durable execution from attempt creation through pull-request publication

## 1. Decision

DARKSTAR uses **at-least-once control with idempotent effects**, not distributed
exactly-once execution. SQLite is the control authority. Before any effect that is
not part of the same SQLite transaction, the daemon commits an operation intent
with a stable idempotency key. After a crash it observes the effect's actual
authority and either adopts an exact effect, safely retries a proven absence,
resumes an owned live operation, or pauses in `reconcile_required`.

The daemon never infers success from a provider message, command exit code, HTTP
response, PID alone, a branch name, or the apparent emptiness of a queue. Durable
success requires the commit point in the catalog below. Ambiguous external state
is a safe stop, not permission to repeat work.

This decision specializes the workflow transaction rules in
[`execution-semantics.md`](../workflow/execution-semantics.md), the delivery
ownership rules in [`WORK_AND_GIT_MODEL.md`](../work/WORK_AND_GIT_MODEL.md), and
the provider recovery boundary in the
[`Codex adapter contract`](../providers/codex/adapter-contract.md).

## 2. Invariants

The words **must**, **must not**, **should**, and **may** are normative.

1. Every durable mutation has one logical owner and one stable operation or action
   key before execution.
2. An internal state transition and its audit event commit in one SQLite
   transaction. Derived queues and projections are rebuildable.
3. An external effect has a committed intent before dispatch and a separately
   committed result only after authority-specific observation proves it.
4. Retrying reuses the operation key. Retrying provider reasoning after a proven
   interrupted attempt creates a new `attempt_id`; it never reuses an old attempt
   as if no work occurred.
5. Lease expiry alone does not prove the writer is dead. No new writer starts
   until process/session and workspace reconciliation proves exclusive ownership.
6. Fencing tokens increase monotonically. A stale holder cannot use daemon-owned
   mutation APIs after a newer token commits.
7. Completed visits, approval decisions, transition tokens, accepted revisions,
   commits, publications, and pull requests are adopted rather than recreated.
8. Compensation moves forward and preserves evidence. DARKSTAR does not erase an
   accepted commit, force-push, delete an ambiguous PR, or discard an unproven
   workspace.
9. Recovery is complete before normal scheduling resumes.

## 3. Durable operation protocol

### 3.1 Internal transaction

State that lives wholly in SQLite uses one transaction:

```text
BEGIN IMMEDIATE
  validate expected aggregate revision and current fencing token
  insert immutable fact using its unique idempotency key
  advance aggregate state/revision
  append audit event
COMMIT
```

The transaction is the commit point. A lost client response is handled by reading
the unique key. `SQLITE_BUSY` is retried with bounded jitter; constraint failures
are read and classified as an exact duplicate or a conflict.

### 3.2 External effect

Effects owned by the filesystem, OS, Git, a remote ref, or a delivery provider use
prepare, execute, observe, record:

```text
SQLite: operation(prepared, operation_id, owner, desired_effect_digest, fence)
External authority: execute using operation_id or an embedded ownership marker
External authority: read back and compare exact desired state
SQLite: operation(committed, observed_identity, evidence_digest) + state/event
```

The daemon may crash in every gap. On restart, `prepared` operations are not
blindly replayed. Their reconciler reads the authority named in the catalog.

An executor may write only inside its leased worktree unless a declared operation
adapter owns the other effect. Provider Git/publication tools are not exposed;
those mutations remain daemon-owned. A command or provider tool with another
durable external effect must declare its owner, stable key, commit observation,
and reconciler using this protocol. Without that declaration it is non-retryable,
and an uncertain dispatch pauses the run. “The command is deterministic” is not
an idempotency strategy.

Operation rows are append-only facts with mutable projection state. A minimum row
contains `operation_id`, `kind`, owner identity/revision, idempotency key,
desired-effect digest, state, lease/fencing token when applicable, dispatch
metadata, observed identity, evidence digest, timestamps, and last error. The
unique constraint is `(kind, idempotency_key)`.

### 3.3 Reconciliation outcomes

| Outcome | Meaning | Scheduler behavior |
|---|---|---|
| `adopt` | Exact effect exists and ownership plus desired state match. | Record the result and continue without dispatch. |
| `resume` | The exact owned process/session is live and resumable. | Reattach under the same attempt and fence. |
| `retry` | Authority proves the intended effect is absent or safely behind. | Dispatch the same operation key again. |
| `interrupt` | An owned provider attempt is proven terminal without a committed result. | Mark it interrupted; retry policy may create a new attempt ID. |
| `reconcile_required` | Evidence is missing, conflicting, divergent, or multiply owned. | Fence the scope and require explicit resolution. |

## 4. Identity, leases, and fencing

### 4.1 Daemon and process identity

Every daemon start creates a random `daemon_instance_id` and records a
`host_boot_id`. On Windows the boot identity is derived from the OS boot time plus
a locally persisted boot nonce so PID reuse across boots cannot appear live.

An owned process is identified by the tuple:

```text
(host_id, host_boot_id, pid, process_start_time, executable_digest,
 command_digest, owner_nonce, attempt_id)
```

PID alone is never identity. The daemon passes the random owner nonce through a
private launch channel, creates the process in a DARKSTAR-owned job/process tree,
and records the provider thread/session ID as soon as it becomes available. A
process is live only when OS identity, launch metadata, and attempt ownership all
match. A provider session is resumable only when the adapter proves the exact
recorded thread and active-writer state. An `already has an active writer` result
is ambiguous and blocks; it does not authorize a replacement turn.

### 4.2 Lease record

A lease contains:

- `lease_id`, scope kind and scope ID;
- holder attempt and daemon instance IDs;
- monotonically increasing `fencing_token` per scope;
- acquired, heartbeat, and expiry timestamps from the daemon clock;
- host boot ID and owned process identity when one exists; and
- state: `held`, `releasing`, `released`, or `reconcile_required`.

Acquisition is a compare-and-swap transaction: no unexpired holder, expected
aggregate revision, and next fencing token. Heartbeats update only the matching
lease ID, holder, and token. Repository-manager and worktree mutation APIs reject
stale tokens.

The MVP uses a 30-second lease, a heartbeat no slower than 10 seconds, and does
not reclaim until at least one full lease interval after the last heartbeat.
Those are defaults, not correctness assumptions: reclaim additionally requires
proof that the owned process tree is absent or terminated, no provider active
writer is possible, and the workspace/ref matches a recoverable state. Clock
regression, sleep/resume uncertainty, inaccessible process information, or a live
old writer produces `reconcile_required`.

Release is also transactional. The daemon first stops or detaches the exact owned
process, inventories workspace state, records the disposition, and only then
marks the lease released. A crash while releasing is reconciled like acquisition;
expiry never silently finishes release.

### 4.3 Idempotency-key construction

Keys are opaque strings stored before use. They are built from immutable IDs, not
names, timestamps, generated prose, or content that may be revised.

| Scope | Key |
|---|---|
| Attempt | `(visit_id, attempt_ordinal)`; the resulting `attempt_id` is stored and reused for start/resume. |
| Provider turn | `attempt_id`; a replacement after proven interruption has a new attempt ID. |
| Candidate/result | `(attempt_id, result_revision)` plus content digest. |
| Artifact content/version | SHA-256 content key; `(artifact_id, version)` registration key. |
| Approval decision | `(approval_request_id, client_action_key)`; the checkpoint ID, provider request ID, control operation, or delivery operation is the typed request subject. |
| Visit success | `visit_id`. |
| Transition token | `(source_visit_id, transition_id, join_epoch)`. |
| Branch/worktree/commit | Stored `operation_id`; commits embed it in `Darkstar-Operation`. |
| Push | `(delivery_line_id, intended_tip)` publication operation. |
| PR create | `(delivery_line_id, head_repo, head_ref, base_repo, base_ref)` plus an owned marker. |
| PR update | `(pull_request_id, owned_body_revision)`. |

## 5. Side-effect ownership and commit-point catalog

The operation IDs in this table are also the executable scenario catalog in
[`recovery-reference.mjs`](../../../scripts/recovery-reference.mjs).

| Operation | Durable owner | Commit point | Recovery proof and idempotency strategy | Compensation / conflict behavior |
|---|---|---|---|---|
| `attempt_record` | Node visit | Attempt row, immutable input references, state event, and ordinal commit in one SQLite transaction | Unique `(visit_id, ordinal)`; read exact row | Conflicting ordinal blocks the visit. |
| `context_manifest` | Attempt | Content-addressed manifest blob and attempt binding are both durable | SHA-256 bytes and exact manifest digest; adopt blob, retry missing binding transaction | Unreferenced exact blobs are retained for grace-period GC. |
| `lease_acquire` | Attempt for scope | Lease CAS transaction with next fencing token | Matching held lease resumes; expired/dead holder may be fenced and reacquired | Expired/live or uncertain holder fences scope and blocks. |
| `provider_process` | Attempt | Exact OS process identity and launch evidence recorded; there is no atomic spawn/record boundary | Owner nonce plus full process tuple; live exact process resumes, proven dead process interrupts | Unknown identity blocks; terminate only an exact owned tree. |
| `provider_turn` | Attempt | Provider terminal result is captured and bound to the attempt | `attempt_id`, recorded thread/session ID, adapter read-back; resume exact active or adopt exact terminal result | Proven absence/death interrupts; active-writer ambiguity blocks. Cost is never “undone.” |
| `candidate_blob` | Attempt result revision | Immutable bytes fsync/close successfully under their content hash, then exact binding transaction | Hash and size; adopt identical content, retry absent binding | Mismatched bytes quarantine and block. |
| `artifact_registration` | Artifact version produced by attempt | Artifact version, provenance, dependencies, content hash, and event commit together | Unique `(artifact_id, version)` and content hash | Orphan content is GC-eligible; a registration conflict blocks. |
| `approval_decision` | Typed approval request | Class-specific action, actor, action key, immutable scope/policy digests, resulting state, and event commit together | Unique `(approval_request_id, client_action_key)` returns prior result | A different action under the same key is a conflict; no downstream provider, workflow, transition, or delivery effect is emitted twice. |
| `visit_success` | Node visit | Validated output snapshot, successful visit state, and event commit together | Unique `visit_id`; exact output digest is adopted | Different output for successful visit is invariant violation. |
| `transition_token` | Source visit and join epoch | Token, bounded-edge counter when applicable, target activation fact, and events commit together | Unique `(source_visit_id, transition_id, join_epoch)` | Duplicate returns prior token; conflicting epoch blocks. |
| `branch_create` | Delivery line | Git ref exists at frozen base and exact observation is recorded | Stored operation ID, ref name, expected SHA, repository identity | Unowned collision or unexpected SHA blocks; never delete/reset it automatically. |
| `worktree_attach` | Run attachment operation | Git reports owned branch at canonical path and exact metadata commits | Enumerate porcelain worktree data; compare gitdir, branch, path, and tip | Unexpected path, attachment, lock, or dirt blocks. |
| `candidate_accept` | Point revision | Candidate manifest, validators, checkpoint evidence, expected parent, and accepted state commit together | Unique point revision and exact candidate digest | Later correction is a new revision; acceptance is not overwritten. |
| `commit_create` | Accepted point revision | Git HEAD/ref contains a commit with exact parent, tree, and operation trailers; observation then advances expected tip | Find `Darkstar-Operation`, verify parent/tree/all trailers, adopt exact commit | Missing commit retries; any mismatch or multiple match blocks. Never amend/reset accepted history. |
| `push` | Publication operation | Fetched remote ref equals the intended local tip and observation commits | Equal adopts; absent/ancestor retries same intended tip; divergence blocks | Never force-push or delete an unexpected remote ref. |
| `pr_create` | Delivery line | Exactly one PR ID with frozen coordinates and exact DARKSTAR ownership marker is observed and recorded | Query exact head/base; unique owned match adopts, absence retries | Unowned or multiple matches block; never auto-close an ambiguous PR. |
| `pr_update` | Pull request owned section revision | Read-back shows desired owned revision while coordinates and human text outside markers remain intact | PR ID plus owned body revision; exact adopts, older retries patch | Coordinate change, lost marker, or conflicting owned revision blocks. |

## 6. Interruption matrix

“Pause” below means commit evidence and enter `reconcile_required`; it never means
discarding the current state.

| Boundary | Possible durable observation after restart | Recovery rule | Duplicate/compensating behavior |
|---|---|---|---|
| Before attempt transaction commits | No attempt row | Create the planned ordinal once | No provider work was authorized. |
| Attempt commits before scheduler sees it | `created` attempt exists | Enqueue from durable state | Queue projection is rebuildable; do not create attempt 2. |
| Manifest blob closes before binding commits | Exact hash exists; binding absent | Bind exact blob or retry content write | Identical put is safe; orphan may later be GC'd. |
| Lease acquisition response is lost | Matching held lease and fence exist | Adopt lease | Never allocate a second token for the same acquisition. |
| Lease expires while owned process is live | Stale heartbeat; exact process still live | Resume/reconcile old holder; no reclaim | New writer remains fenced. |
| Lease expires and process is proven dead | No live identity/session; workspace reconcilable | Mark old lease released, increment fence, then retry policy | Old token can no longer call daemon mutations. |
| Spawn dispatched before process identity commits | Matching owner nonce/process found, none found, or identity unknowable | Record/resume exact; interrupt if proven absent; otherwise pause | Never spawn a second process while identity is uncertain. |
| Daemon dies with recorded provider process | Exact process and optional session are live | Reattach/resume same attempt | Preserve lease and attempt ID. |
| Provider start sent before thread/session ID is recorded | Adapter may report active writer or no queryable identity | Adopt only exact owner evidence; otherwise pause | Do not start a replacement turn merely because response was lost. |
| Recorded provider session is active | Exact thread resumes | Resume same turn/attempt | Replayed notifications dedupe by provider event key/sequence. |
| Provider process dies without terminal result | Process absence and no external result are proven | Mark attempt `interrupted`; bounded retry creates new attempt | Candidate workspace is preserved and reconciled first. |
| Provider returns before result commits | Terminal session result, output log, or exact response evidence exists | Fetch/adopt exact result; otherwise preserve workspace and pause/interrupt as provable | Never report success from exit code alone. |
| Candidate bytes close before candidate binding | Exact content hash exists | Adopt and bind | Orphan bytes are safe and immutable. |
| Artifact registration response is lost | Exact version row exists | Return existing artifact handle | No second artifact version. |
| Approval request arrives before decision commits | No action key row | Client retries same key | Different action under key conflicts. |
| Approval decision commits before response | Exact action/result exists | Return prior result | Provider response, visit/transition, workflow control, and delivery dispatch remain separate and idempotent. |
| Visit success commits before transition | Successful visit, no token | Deterministically evaluate committed outputs and insert token | Successful executor is not rerun. |
| Token commits before target enqueue | Exact token/activation exists | Rebuild ready queue | Unique key prevents duplicate join input. |
| Branch ref created before operation result commits | Exact owned ref at base exists | Adopt | Unexpected ref is preserved and blocks. |
| Worktree attaches before metadata commits | Porcelain data exactly matches desired attachment | Adopt | Unexpected path/branch/dirt is preserved and blocks. |
| Provider leaves uncommitted file changes | Dirty owned worktree and attempt evidence exist | Resume exact writer or capture candidate; otherwise pause | No second writer, clean, stash, or reset without full ownership proof. |
| Candidate acceptance commits before Git commit | Accepted revision, expected parent, no matching commit | Create/retry stored commit operation | Stage exact manifest only. |
| Commit ref advances before SQLite acknowledgment | Exact operation trailer, parent, and tree match | Adopt commit and advance expected tip | Mismatch/multiple match pauses; no duplicate commit. |
| Commit command fails before ref update | No matching commit; HEAD still expected parent | Retry same operation | Captured candidate and index proof must still match. |
| Push request times out | Remote equals intended tip, is ancestor/absent, or diverges | Adopt equal; retry ancestor/absent; pause divergence | Fast-forward only; no inverse push. |
| Remote advances before publication result commits | Fetched remote equals intended tip | Adopt publication | No second logical publication. |
| PR create request times out | Unique exact owned PR, absence, unowned/multiple match | Adopt unique; retry absence; pause otherwise | Never create PR 2 under ambiguity. |
| PR exists before its ID commits | Exact coordinates and ownership marker match | Record/adopt PR ID | Preserve all human/provider state. |
| PR owned-body update times out | Desired revision, older revision, or marker/coordinate conflict | Adopt desired; retry older; pause conflict | Patch only delimited owned section. |
| Daemon dies while releasing lease/worktree | Lease `releasing`, process/worktree disposition partial | Re-run observation sequence before final release | Never make expiry stand in for cleanup proof. |
| Crash after PR publication commits | PR operation and delivery-line state committed | Resume workflow after publication | Startup projection rebuild emits no duplicate external action. |

## 7. Startup reconciliation

Startup is a recovery phase, not ordinary scheduling:

1. Acquire the installation recovery lock and open SQLite in WAL mode. Run quick
   integrity and migration checks; failure prevents writes.
2. Create the daemon instance/host-boot identity. Mark prior daemon heartbeats
   stale without releasing their leases.
3. Rebuild aggregate projections and ready queues from authoritative rows/events.
4. Reconcile repository-manager locks, leases, owned process trees, provider
   sessions, worktrees, refs, remote refs, and PRs in that order. No new
   write-capable attempt starts during this pass.
5. Reconcile every `prepared` external operation using its catalog rule. Record
   `adopt`, `resume`, `retry`, `interrupt`, or `reconcile_required` with evidence.
6. For attempts left `starting`, `running`, or `validating`, resume an exact live
   owner, finish validation from a captured immutable candidate, or mark a proven
   dead attempt interrupted. Preserve ambiguous workspaces.
7. Recompute visit readiness, joins, loop counters, checkpoint waits, and terminal
   closure from durable facts. Repair only derived projections.
8. Increment/reacquire fences only for scopes proven safe, then release the
   recovery lock and admit scheduler work.

Reconciliation is repeatable. Crashing during reconciliation leaves either the
old evidence or another idempotently committed fact, so the next start runs the
same algorithm.

## 8. Compensation and operator resolution

Automatic compensation is deliberately narrow:

- immutable unreferenced blobs may be deleted only by grace-period garbage
  collection after a reachability scan;
- an exact owned dead process may be terminated and its lease released;
- a failed/rejected uncommitted candidate may restore only the recorded owned
  worktree when every proof in the work/Git model matches;
- an absent/ancestor remote ref may be fast-forward retried; and
- an older DARKSTAR-owned PR body section may be patched forward.

Everything else is preserved. Operator resolution records the actor, observations,
chosen authority, and resulting state as a new fact. Supported resolutions are
adopt an exact effect, retry after proving absence, terminate an exact owned
process, rebind an exact artifact, preserve and abandon the delivery line, or
declare an externally repaired state with evidence. Destructive branch/ref/PR
cleanup and history rewriting are outside automatic MVP recovery.

## 9. Executable failure-injection contract

[`recovery-scenarios.json`](../../../examples/recovery/recovery-scenarios.json)
enumerates crash observations and expected decisions for every catalog operation.
The dependency-free reference runner validates catalog coverage and derives the
decision from the authority-specific strategy:

```powershell
node scripts/recovery-reference.mjs examples/recovery/recovery-scenarios.json
node --test tests/recovery-reference.test.mjs
```

The suite must fail if an operation loses scenario coverage, an observation would
retry an ambiguous/exact effect, a decision differs from the normative strategy,
or a scenario omits its crash boundary. Production fault injection will reuse
these stable operation IDs and crash windows.

## 10. Acceptance mapping

| DS-003 requirement | Evidence |
|---|---|
| Leases and process identity | Section 4 plus lease/process executable scenarios |
| Interruption matrix for attempt through PR | Section 6 |
| Idempotency keys and compensating behavior | Sections 4.3, 5, and 8 |
| Side-effect owner and commit point for every durable effect | Section 5 |
| Executable failure injection | Section 9, scenario suite, reference runner, and tests |
