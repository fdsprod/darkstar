# Startup reconciliation

Daemon startup is a recovery phase. After the daemon lifecycle lock is held and
before the endpoint is published, DARKSTAR opens the durable SQLite database in
WAL mode, applies migrations, rebuilds derived projections, and classifies every
non-terminal lease and outbox operation. Normal scheduling is not admitted until
that pass completes.

## Decisions and evidence

Every authority observer returns exactly one closed outcome:

| Outcome | Durable action |
|---|---|
| `adopt` | Record an exact already-completed effect and do not dispatch it again. |
| `resume` | Transfer the exact live owner to the new daemon while retaining its fencing token or operation identity. |
| `retry` | Record proven absence, release stale ownership, and make the same stable operation available again. |
| `interrupt` | Record a proven terminal owner and release its fenced scope; retry policy may create a new attempt later. |
| `reconcile_required` | Preserve evidence, fence the subject, and pause scheduling. |

The first decision for `(startup_id, subject_kind, subject_id)` is append-only
and includes the canonical subject payload observed during classification. An
exact repeated write is idempotent; a changed subject or decision is a conflict. The
decision and the lease/outbox transition commit in one transaction, so a crash
cannot publish a classification without its corresponding fenced state.

Authorities are selected explicitly (`process`, `lease:<scope>`, or
`operation:<kind>`). If an authority adapter is unavailable or its observation
fails, startup records `reconcile_required`; it never guesses that an effect is
absent. Future provider, repository, delivery, and worktree adapters register at
this boundary without adding flags or alternate decision fields.

## Readiness and observability

The authenticated API root and unauthenticated health response expose only the
classified and reconciliation-required counts. Scheduler admission is derived
from the latter count instead of being stored as a second flag. `darkstar api
status` reports the resulting status. The API remains available when reconciliation is required so
operators can inspect and resolve preserved evidence, while scheduling remains
paused.
