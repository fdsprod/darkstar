# Durable coordination

The SQLite state-store adapter implements the ownership primitives required by
the [crash recovery model](../../docs/architecture/recovery/RECOVERY_MODEL.md).
They remain application-owned contracts in `src/ports/statestore`; SQLite is the
first concrete implementation.

## Leases and fencing

A lease is identified by `lease_id` and protects one typed scope. Repository,
worktree, and attempt scopes are currently supported. Each scope has one durable
monotonic fencing counter, and at most one lease whose state is not `released`.
Acquisition is a compare-and-swap against the last issued token.

The mutation capability is the complete tuple of lease ID, holder attempt,
daemon instance, and fencing token. Heartbeats and mutation validation require
an exact match. An expired lease remains the active owner: expiry causes
validation and heartbeat to fail, but it never permits automatic reclamation.
Recovery must record `reconcile_required` evidence or begin release, inventory
external ownership, and complete release with disposition evidence before a new
token can be issued.

The MVP defaults are a 30-second lease and heartbeat intervals no slower than 10
seconds. Those values control liveness only; fencing and explicit reconciliation
provide correctness.

## Queue ordering

Queue membership has one source of truth: a row in `queue_entries`. Exact repeat
insertion is idempotent and preserves the original enqueue time. Reusing an item
identity with different priority, availability, or payload is a conflict.

Only available rows participate in ordering. The head is selected by:

1. priority, highest first;
2. enqueue time, oldest first; and
3. immutable item ID, ascending, as the deterministic tie-breaker.

## Repository writer lock

Repository write attempts use the `repository_write` queue and a repository
lease. `AcquireRepositoryLock` runs one SQLite transaction that verifies the
requested item is the available queue head, creates the next fenced repository
lease, and removes the queue row. Any lease or fencing conflict rolls the whole
transaction back, so the writer remains queued. A repeated exact acquisition
adopts the durable lease even though its queue row was already removed.
