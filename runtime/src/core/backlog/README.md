# Source-backed backlog cache

The daemon supplies a `Resolver` for each immutable source binding. `Service`
controls source discovery, one bounded refresh tick, exact reconciliation and
durable checkpoints. `View` reads retained observations only. No operation in the
engine or its storage port creates work, runs, queue entries or provider effects.

`SelectSource` compares the current binding revision and appends a new selection.
Existing and new projects default to their native namespace. External selections
pin connection identity/revision and native scope independently of code repository
membership or publication destination. Switching source preserves every prior
binding, checkpoint and source observation; failures never select a fallback.

`Refresh` performs at most the configured page/ticket bounds under one overall
context deadline. Each page's observations and opaque continuation commit in the
same transaction, with both selected-binding and checkpoint revision checks.
Restart or interruption resumes the last committed cursor. Repeated observations
of a native ticket revision share one immutable record; different content under
that revision is protocol drift. Source switch or concurrent advancement prevents
an in-flight response from committing against a new selection.

Both providers initially use bounded full native sweeps. Query changes advance
the scan generation and reset the cursor; older cached tickets remain accessible.
`CurrentQueryMatch` means the row appeared in the current generation, not that a
missing row was deleted. A filtered/partial/full list absence never marks missing.
`ReadRefresh` can mark one retained ticket missing only from exact adapter evidence
and retains its previous source content. Metadata reports archival and placement
separately from accessibility, freshness and business status.

`Poll` respects the persisted next-attempt time. Local retry backoff is capped;
provider Retry-After/reset minimums can extend that cap and are never shortened.
Successful completion records the last successful scan time. A stale cache remains
readable during failures, and the public view distinguishes cached, fresh,
incomplete, missing, inaccessible, archived and out-of-scope observations.

The data design keeps binding history, immutable normalized observations, cache
membership and scan progress under separate owners. Source selection uses a
closed native/external union; strict versioned codecs reject unknown, mixed and
invalid knowledge variants. Phase-specific checkpoint validation and atomic
storage checks prevent contradictory cursor outcomes and cross-source commits,
while observed freshness/evidence locations do not duplicate business revisions.

Controller behavior is tested with SQLite and conformant source feeds in
`runtime/tests/backlog`; provider integration fixtures live in
`runtime/tests/trackerbacklog`. Storage tests cover migration rollback/backfill,
rebuild preservation, immutable observations, forged content, strict checkpoints
and stale revision conflicts.
