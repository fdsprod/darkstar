# Versioned tracker rules

This package evaluates three independent parts of a project mapping: intake
predicates and admission actions, typed milestone outbound intents, and display
groups. It neither schedules agents nor mutates tickets. The daemon owns durable
configuration activation, retained observations, admission transactions, evidence
validation, authorization, and external writer operations.

`Decode` rejects unknown schema versions, duplicate JSON keys, unknown fields and
mixed action variants. `ValidateShape` checks the closed rule schema;
`Validate` additionally requires current daemon-owned provider discovery, exact
workflow pins, and supported readiness policies. Predicate values and transition
operations are stable IDs, never display names. Source namespace, selected
container, adapter pin, project, and binding revision must match the observation.
Sprint membership is a separate optional condition; unsupported sprint concepts
cannot silently become universal admission requirements.

`PreviewIntake` returns a matching action and display projection. `Group` permits
several observed state IDs in one presentation group while retaining each native
state ID/name. Unmapped, unknown and unsupported observations go to the explicit
unknown group. A display group cannot be inverted into a transition or local
execution state. Overlapping intake rules and overlapping rules for the same
workflow milestone are rejected rather than selecting the first match.

`EvaluateAdmission` returns a deterministic proposal or automatic admission
decision. Its cursor is scoped to project plus immutable native ticket identity;
changing a rule revision, source filter, or binding revision cannot reset its
episode count. Repeated native revisions, repeated events, reordered observations
and retained outbound echoes do not start another episode. Reopen requires an
observed departure from eligibility, a later eligible observation, settled prior
execution and a remaining `maxAdmissions` allowance. The bound includes initial
admission and is limited to 1–100; the observation window is limited to 4096 and
fails visibly when full instead of dropping deduplication history.

A manual proposal consumes no event or admission allowance.
`ConfirmManualAdmission` is called after the daemon verifies an explicit human
admission command. The daemon must atomically persist the returned cursor and
admission, retaining exact rule bytes, workflow digest, and readiness policy with
the admitted work/run. Automatic decisions use the same transaction boundary.
Neither admission mode grants tools, bypasses readiness/human checkpoints, or
fabricates running or successful execution from a tracker status.

`PreviewOutbound` accepts a declared milestone from a pinned workflow and calls
the supplied daemon `EvidenceValidator` for every required evidence type. That
validator must resolve retained artifact bytes, their hash/version, and their
actual authority. A nonempty reference or a model's success claim is insufficient.
The resulting effect is checked against exact current writer options, transition
fields and provider guards. The caller still needs durable authorization and an
operation journal before dispatch. Generic `run.completed` produces no handoff
or close effect. Explicit `noop` and report-only actions preserve this separation.

[The Jira-style fixture](testdata/jira-workflow.json) uses type-specific workflow
identity, a selected sprint, required transition resolution, and a handoff proven
by both validated implementation and an approved PR. Its companion tests inspect
the provider guard and reject PR creation as a substitute for PR approval. It is
a design fixture only; no production Jira adapter is enabled. The same tests
exercise built-in, Linear and GitHub Issues capability differences and reject
unsupported transition operations instead of inventing fallback mappings.

The separate closed action variants prevent intake configuration from carrying
writer or execution commands. Display grouping is a projection rather than a
second business-state authority, and cursor identity stays independent of mutable
configuration. Immutable rule/workflow snapshots intentionally duplicate historic
configuration so activation changes cannot rewrite already admitted work.
