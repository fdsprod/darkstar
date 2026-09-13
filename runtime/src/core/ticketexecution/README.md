# Source ticket execution

This service is the daemon-owned boundary between observing a tracker ticket and
authorizing local execution. Browsing and refresh never call admission. Admission
requires a selected binding revision and an exact retained observation; a writer
transaction compares the current observation, records approval, and creates or
reuses one local work record. A different request key cannot duplicate intake.

Work title and details retain their original admission content. Current source
content, approved content, activity, last terminal run outcome, and externally
observed acceptance remain separate read projections. Provider lifecycle names
are observations, not local execution commands. A source change, missing ticket,
lost access, archived ticket, or scope move requires human assessment. Native
cancelled is a defined native business state; arbitrary provider labels do not
imply cancellation. Existing run inputs remain frozen and no source observation
automatically cancels or pauses a run.

New run preparation requires the explicitly approved current source observation,
checked within five minutes. The run-created transaction validates that approval,
lineage and complete adapter pin, then persists the original versioned ticket
snapshot. Start, retry, resume and continuation enforce exclusive work ownership.
A failed run keeps ownership until explicitly settled by cancellation; completed
or cancelled runs release it only after attempts and durable operations settle.

A project source switch changes future intake. Existing work keeps its historical
binding and can refresh that exact old source independently. Explicit rebind
requires settled runs and old-source operations, rejects a target owned by other
work, and appends immutable lineage. Old observations, approvals and per-run
snapshots remain available. Pre-migration native work keeps its compatibility
classification; newly created native work follows explicit source approval.

Source bodies are untrusted task data. Neither this service nor tracker adapters
gain provider writer or workflow scheduling authority from ticket text.
