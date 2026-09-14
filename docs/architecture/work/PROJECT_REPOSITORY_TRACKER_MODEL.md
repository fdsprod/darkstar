# DS-230 — Separate projects, repositories, and ticket trackers

> [Documentation index](../../README.md) | [Decision register](../../decisions/decision-register.json)

**Status:** Accepted, 2026-09-12  
**Decision issue:** [DAR-153](https://linear.app/darkstar-dev/issue/DAR-153)  
**Affected issues:** DS-025, DS-110, DS-112, DS-113, DS-118, DS-150, DS-231,
DS-235, DS-238, DS-246, DS-247, DS-248, DS-249  
**Risks:** RSK-001, RSK-002, RSK-005

## 1. Context and supersession

[DS-004](WORK_AND_GIT_MODEL.md) accepted a project registration identifying
exactly one Git repository. Feature planning must also support a product with no
code yet and a product spanning several repositories. Tracker source scope and
the destination of an approved backlog need not match those repositories.

DS-230 replaces DS-004 as the current decision. DS-004's original outcome remains
recorded unchanged for history. This decision replaces its project cardinality
invariant, project identity definition, and deferral of multi-repository planning.
It carries forward its work/story/point identities, single-repository delivery
line, repository write lease, ownership, revision, and recovery rules. The
standalone delivery-node clarification in [execution semantics](../workflow/execution-semantics.md)
and [approval contracts](../security/APPROVAL_AND_PERMISSION_MODEL.md) continues
to govern those nodes; this replacement does not insert new workflow gates.

This is an architecture contract. Runtime/API/storage rollout belongs to DS-231,
DS-246, and DS-248 under the [migration plan](../../planning/project-tracker-migration.md).
Names below describe the target domain, not fields already supported by the API.

## 2. Identities and relationships

| Record | Identity and authority | Cardinality and purpose |
|---|---|---|
| Project | Immutable `project_id`; project event stream | Product/planning boundary with configuration and workflows; zero or more repository memberships and exactly one selected ticket-source binding revision. |
| Repository | Immutable `repository_id`; repository registry | Shared local Git identity, independent of any project. Linked worktrees resolving to the same canonical common Git directory share this record and write-ownership key. |
| Project repository membership | Stable `(project_id, repository_id)` pair; versioned membership events | Many-to-many relation with project-local label and repository configuration overrides. A repository may serve multiple projects without copying its identity. |
| Ticket-source binding revision | Immutable binding identity/revision; project selects one revision | One built-in namespace or one external adapter installation/account and source scope. The installation may configure many independent accounts/adapters. |
| Publication destination revision | Immutable binding identity/revision | Optional project default; an approved publication selects exactly one Linear or GitHub Issues destination. It is independent of ticket-source and code repository membership. |
| Source ticket reference | Provider/adapter namespace, stable account/tenant identity, native namespace, and native immutable ticket ID | Business ticket identity, independent of binding/filter revision and project. Display keys, titles, URLs, and human-readable repository names are attributes, never identity. |
| Work item | Existing immutable `work_item_id`, parent project | Execution/intake record related to a source ticket; not the authoritative business-ticket record. Existing work history and delivery ownership remain stable. |
| Run | Existing immutable `run_id`, parent work item | Pins ticket observation, binding revision, repository scope, workflow, policy, and configuration. Later project edits do not rewrite it. |

Repository remote URLs are locators, not identity: forks and separate clones are
not merged because they share a URL or content. A physical local repository's
canonical common Git directory must be unique across registry records, including
path aliases and Windows path casing. A move or clone replacement requires an
explicit verified relocation/rebind; missing paths never cause silent adoption.
Remote provider repository IDs, local roots, and canonical common directories
are recorded as typed coordinates, not overloaded into `project_id`.

Source-binding revisions record selection and authority provenance, not a new
ticket identity. Credential rotation, source-filter changes, or adapter version
upgrades do not duplicate a ticket. Different projects observing the same native
ticket retain one source identity with distinct project execution-admission
records. The built-in equivalent is installation/native namespace plus immutable
native ticket ID. Runs and operations pin the binding revision separately from
that stable ticket reference and the exact observed ticket revision.

Membership lifecycle is a closed `active` or `removed` record; the latter includes
removal actor, time, and revision. Removing membership does not remove the shared
repository record, delete files, branches, worktrees, artifacts, or source tickets,
or affect another project's membership. Removed memberships remain addressable
by historical snapshots; re-adding the same pair appends a new active revision.

## 3. Repository configuration and removal

Project configuration covers product defaults, workflow bindings, artifact root,
and tracker binding selection. Repository registry configuration covers verified
local locations and repository-wide safety ceilings. Membership configuration
covers this project's worktree base, validation profiles, path scope, and Git
delivery defaults for that repository. Credentials stay in installation/account
configuration; committed configuration holds references only.

For an overridable repository setting, precedence is highest first:

1. Explicit run-creation CLI/API value naming that repository.
2. Work-item/run override naming that repository.
3. Active project-repository membership override.
4. Project default for that setting.
5. Shared repository default for that setting.
6. User configuration.
7. System configuration.
8. Shipped default.

For project-only settings, omit membership and repository levels. Each field has
one declared scope; repository settings cannot overwrite tracker selection or
project identity. Map leaves resolve independently; lists and tagged choices
replace as units, never concatenate. Absence inherits, and an empty value is
valid only when that field's schema permits it. Paths resolve relative to the
configuration's recorded root, not the daemon's working directory. Resolved
values retain provenance and content digests in the run snapshot. Permission
ceilings are intersected and explicit denies win; value precedence cannot grant
access forbidden by installation, repository, project, or attempt policy.

Membership removal blocks its selection for new runs. Existing runs retain their
frozen scope and may finish or retry within it under still-valid authorization;
removal alone is not a cancellation or security revocation. Explicit revocation
blocks new attempts/effects and reconciles any active writer before releasing its
lease. A run must never silently shrink its scope or substitute another repository.
Missing/unavailable repositories block required work with an actionable reason;
history remains readable. Releasing membership does not release write ownership.

## 4. Immutable run repository scope

The daemon resolves one closed scope at run creation:

| Scope choice | Required contents | Permitted behavior |
|---|---|---|
| `none` | No repository entries | Briefs, evidence, planning, review, and authorized tracker publication that require no code context. |
| `read_only` | Nonempty unique repository snapshots | Research across explicitly selected member repositories. No Git writes. |
| `single_writer` | Exactly one target repository snapshot; zero or more distinct read-only repository snapshots | Implementation/delivery only in the target's owned workspace. Other selected repositories remain read-only. |

Each repository snapshot records repository ID, membership revision, canonical
coordinates, selected ref and resolved commit, and resolved configuration digest.
Mutable checkout content is usable only as explicitly captured immutable evidence
with its own digest; it must not be mislabeled as committed source. Artifact-only
repository investigation can cite retained evidence without granting live access.
Unavailable required revisions fail preparation; the daemon does not pick HEAD.

The selected repository IDs, roles, and base commits cannot change during retries,
restart, or a route extension. Adding a repository or promoting read-only scope to
write requires a new run. The daemon supplies each agent only the declared node
inputs and permitted repository slices, once with their input names, rather than
the workflow graph or unrelated repository contents. Agents cannot expand scope.

Zero-repository projects can create work, author and review briefs/stories, and
publish an approved backlog. A required repository input blocks before provider
execution. Membership does not automatically expose all repositories to every
run. Single-repository compatibility selection is described in the migration plan;
multiple repositories require explicit selection or a versioned deterministic
binding, never first-list-entry or current-directory guesswork.

One work item may investigate many repositories, but in this phase may bind at
most one implementation repository and delivery line. Later write runs on that
work item must use that same repository; changing it requires a new work item.
The daemon serializes writers by shared `repository_id` across projects, work
items, runs, and linked worktrees. Existing canonical-Git-directory locking also
remains in force during migration. Independent clones do not confer ownership of
each other's remote branches; exact frozen refs and remote reconciliation still
apply. Multi-repository planning, a story's affected-repository list, and tracker
publication never authorize coordinated Git writes or multiple delivery lines.

## 5. Ticket sources, publication, and authority

The ticket-source choice is a closed union: `built_in` carries a native namespace;
`external` carries an exact adapter installation/account and native source scope.
An external read failure does not switch to built-in. A publication destination
is separately `unconfigured`, `linear` with its account/team scope, or
`github_issues` with its account/repository scope. A GitHub Issues destination can
be outside code membership; a Linear source can feed a GitHub Issues publication.
Publishing there does not change the source binding or imply source-ticket linkage.

| Field or lifecycle | Authority | DARKSTAR behavior |
|---|---|---|
| Ticket title, description, priority, assignee, labels, relationships, business status | Selected native or external tracker | Read revisioned observations; request mutations only through supported capabilities and configured rules. Local cache is an observation, not a second master. |
| Workflow, route, attempt, execution outcome, cancellation | DARKSTAR daemon | Durable execution state; a completed or cancelled run does not close/cancel its source ticket. |
| Original source snapshots, artifact revisions, approvals, execution evidence | DARKSTAR | Immutable audit evidence, preserving source identity and observed revision independently of current ticket contents. |
| Sync/publication operation intent, idempotency, observations and reconciliation | DARKSTAR | Record each external effect and its actual result; uncertain outcomes remain uncertain. |
| PR/review, CI, acceptance, deployment facts | Reporting external system (or explicitly configured native evidence authority) | Store observations with origin and revision/time; do not fabricate facts from model claims or workflow completion. |

Ticket creation and later edits require the destination's advertised capability,
account permissions, and an authorized operation. Read/observe support is enough
to run a tracker-backed factory; DARKSTAR need not create, own, or deliver every
ticket. A natively created ticket belongs to the built-in source. An externally
created or adopted ticket remains authoritative in that tracker, even when
DARKSTAR owns a bounded publication marker or managed body section.

Intake, progress reports, and business transitions are explicit versioned rules.
Only a rule naming its trigger, target source binding, required evidence, and
supported transition may request a business-state mutation. Generic completion
has no external-ticket closing effect. Review, CI, acceptance, and deployment
can happen outside DARKSTAR and be observed independently of run outcome.
Detailed adapter contracts are DS-238; no production Jira adapter is implied.

## 6. Source switching and native migration

A source switch is an explicit revision-checked project command selecting a new
binding revision for future intake. It does not copy business data automatically,
retarget a work item, rewrite snapshots, or replay old milestones against the new
source. Existing active work retains its source binding/reference and pinned
rule versions, including pending operations. Those operations can proceed only
while the old account remains authorized; revocation pauses/reconciles them.
Historical runs always show the original source, with optional later links shown
separately. A new run for existing work keeps that work's source lineage.

Moving built-in tickets to an external source (or the reverse) is a distinct,
explicit migration with a reviewed source-to-destination mapping. It requires
creation or exact-ID adoption capability, durable per-ticket operations, and
read-back confirmation before recording links. No title matching, destructive
source deletion, implicit close, or rollback by deleting published tickets is
allowed. Partial migration records successes and unresolved items for safe retry.
Native IDs, comments/history, artifact bindings, work IDs, and old source snapshots
remain retained. Native user-visible ticket creation, editing, and history must
remain available through the built-in adapter after implementation.

An existing work item may be explicitly rebound only after its active runs and
old-source operations have settled; record a new lineage binding and retain all
prior bindings. Future runs can then pin the new source. During active work, the
new tracker ticket is only a migration link; it cannot become a second active
authority for that work. Changing the publication destination similarly cannot
retarget an approved or pending publication: create a separately reviewed request.

## 7. Consequences, alternatives, and evidence

Separate memberships and versioned bindings prevent a repository removal from
deleting project identity or switching ticket authority. Closed run-scope choices
prevent multiple write targets, and shared repository identity prevents projects
from acquiring independent leases for one physical repository. Immutable snapshots
are deliberate historical copies; current configuration and source business data
each retain one authority.

Retaining project-as-repository would exclude zero-repository planning. Copying a
repository record into every project would fragment locks. Inferring tracker scope
from code membership would prevent cross-tracker publication and risk writing to
the wrong account. Bidirectional field mirroring is rejected because field and
transition authority must be explicit.

Coordinated multi-repository implementation, simultaneous publication to both
trackers, production Jira, GitHub Projects boards/custom fields, and unrestricted
bidirectional mirroring remain deferred. The feature-planning workflow ends with
an approved backlog published to one selected Linear or GitHub Issues destination.

Evidence is the [DAR-153 acceptance contract](https://linear.app/darkstar-dev/issue/DAR-153),
the existing DS-004 ownership/recovery model, and the migration acceptance matrix.
Governance preflight must pass for DS-230 and reject DS-004 with its replacement.
Implementation must satisfy the matrix before exposing the new capabilities.
