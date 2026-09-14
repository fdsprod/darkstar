# Feature brief and story backlog contract

> [Documentation index](../../README.md) | [Project/repository/tracker model](../work/PROJECT_REPOSITORY_TRACKER_MODEL.md)

**Status:** Versioned contract and executable reference semantics; downstream workflow, review UI, and publication integration are not implemented by this contract.
**Issue:** DS-235 / [DAR-157](https://linear.app/darkstar-dev/issue/DAR-157)

## Content and identity

[`feature-planning-v1alpha1.schema.json`](../../../schemas/feature-planning-v1alpha1.schema.json)
is a closed union of `feature_brief`, `story_backlog`, `story_links`, and
`handoff_observation`. Unknown fields, future discriminators, mixed sibling
payloads, and unsupported schema versions fail validation. The first two are
canonical planning content; links and observations are separate immutable records.
All remain untrusted artifact data under the [artifact contract](ARTIFACT_AND_CONTEXT_CONTRACT.md).

An artifact registry owns its immutable version, bytes, digest, creator, and
approval bindings. Content does not duplicate that version counter. `schemaVersion`
is the format version, not the artifact revision. An exact reference carries
`artifactId`, positive `version`, and `sha256`; IDs and digests are not interchangeable.
`projectId` and `featureKey` scope the feature; `(projectId, featureKey, story.key)`
is stable story identity across title changes, ordering, revisions, publication,
and tracker moves. No story carries a tracker ID, execution status, business
status, or approval boolean. A drafted story is an artifact, not a native ticket.

| Record | Meaning |
|---|---|
| Feature brief | Problem, users, outcomes, included/excluded scope, keyed requirements and acceptance criteria, repository catalog, evidence, and decisions. |
| Story backlog | Exact brief revision plus keyed stories with outcome, acceptance criteria, scope, requirement references, repository impact, dependencies, evidence references, decision references, and planning lifecycle. |
| Story links | Exact backlog revision, one publication destination, its binding revision, and observed native ticket links with retained read-back evidence. |
| Handoff observation | Exact backlog/story scope, one typed milestone, observation authority, timestamp, and nonempty exact evidence references. |

Repository impact is exactly `unknown` with a reason, `none` with a rationale,
or `repositories` with a nonempty unique ID list. Empty lists cannot masquerade
as investigated repository-free work. The brief catalog pins shared repository
IDs and membership revisions. The daemon verifies those memberships when freezing
context; the backlog may reference only that exact brief's catalog. A planned
affected-repository list grants no access or Git write authority. Zero-repository
briefs and stories work without guessing a checkout.

Keys are unique within their named catalog; a requirement key and an evidence key
are distinct namespaces. Evidence pins an immutable artifact and a locator/finding.
Decisions have one state: `open` with a blocking flag, `resolved` with resolution
and nonempty evidence keys, or `withdrawn` with a reason. Resolved decisions cannot
retain a stale blocking flag. State and supporting evidence are reviewable planning
claims; they do not grant execution authority.

## Validation and revision lineage

Acceptance requires JSON Schema validation first, followed by
[`validatePlanningSemantics`](../../../scripts/feature-planning.mjs) against
already validated, digest-verified reference records. The reference implementation
checks structural relationships; it is not the runtime registry or an approval gate.
It never loads latest versions, follows URLs, starts providers, or mutates tickets.

The registry must separately verify reference availability and hashes, current
project/feature ownership, monotonically increasing versions, and compare-and-swap
against the exact latest predecessor before accepting a revision. A revision of an
artifact keeps its artifact ID and increments the registry version; the feature
identity and artifact type must match the predecessor. `initial` is allowed only
for a newly allocated artifact identity. Forking into a new feature is an explicit
copy with new feature/story identities and retained source evidence, not a revision.

Semantic checks reject duplicate catalog keys, missing requirement/repository/
evidence/decision references, missing or inactive dependencies, self-dependencies,
dependency cycles, invalid split references, split cycles, and wrong type, project,
feature, version, or digest on contextual joins. Backlogs use their own evidence
and decision catalogs; copying a brief finding into that catalog must retain its
exact source reference. Consumers validate each referenced artifact before joins.

Every backlog revision is a full snapshot. An existing story keeps its key when
its same intended outcome is refined. Replacing its business meaning requires
retiring or splitting it and allocating fresh keys; semantic identity judgment
belongs to human review, not title comparison. The registry retains allocated keys
across the feature's history and must never reuse them for unrelated meaning.

| Change | Required representation |
|---|---|
| Edit/reorder | Same story key; new immutable backlog revision. |
| Retire | Preserve the last story body and set lifecycle to `retired` with reason. |
| Split | Preserve the parent body; `split` carries reason and at least two new active child keys. A new child has exactly one split parent. |
| Later retirement/split of child | Retain both parent and child tombstones; historical split references still resolve. |
| Previously inactive story | Immutable tombstone; cannot resurrect, disappear, or change. |
| Changed requirements, evidence, decisions | A new snapshot may remove unused entries; current references cannot dangle. Prior versions retain all old contents, and removed keys cannot be reassigned to unrelated meaning. |

Active dependencies on a newly retired/split parent must be explicitly revised to
appropriate active children or removed with the revised scope. No automatic
dependency expansion occurs. Tombstone content references its historical brief and
catalogs; do not revalidate that preserved body against current catalogs. Initial
or migrated snapshots cannot invent inactive stories without a predecessor.

Unknown impact and open blocking decisions are valid draft states. Schema validity
does not imply readiness or publication approval. Publication policy must surface
these states and require an attributable decision bound to the exact backlog and
destination; it must not silently treat unknown as none. Plan approval is invalidated
for a changed revision, and retained prior decisions remain readable.

## External links and handoffs

One `story_links` record names one Linear or GitHub Issues destination, never both.
Linear provenance includes connector installation/account, workspace and team;
GitHub Issues provenance includes installation/account, host and native repository
ID. Destination configuration revision is separate from identity. Native ticket IDs
are scoped by their native system/account/namespace, not display titles or URLs.
A connector implementation replacement, team move, or configuration revision does
not rename the canonical story. A native move that changes namespace/identity needs
an explicit observed old-to-new link, retaining earlier records.

Each link is read-back evidence for a native ticket ID and revision; it stores no
editable copy of ticket title, body, or business state. External observation and
publication writers use the tracker authority and reconciliation contract (DS-238).
Retired stories may retain historical links without reopening or deleting their
external tickets. A destination change creates a separately reviewed publication
request; it cannot retarget a pending effect or reinterpret old links.

Handoff milestones are a closed set: `plan_approved`, `implementation_validated`,
`tests_passed`, `pull_request_created`, `pull_request_accepted`, and `deployed`.
These are independent observations, not ordered workflow states. Implementation
and test milestones name repository, exact commit and validation profile; PR
milestones name repository, PR identity and commit; deployment names environment,
release and commit. Plan approval names the exact backlog and a recorded daemon
approval decision. No `completed` milestone or business-state field is accepted.

The daemon verifies cited evidence and the reporting authority before accepting
the observation: actual human approval for plan approval; validator results for
implementation/tests; reporting Git host or authorized human review evidence for
PR acceptance; deployment authority for deployment. Model text alone is not such
evidence. An observation's existence does not prove that verification happened;
runtime ingestion must enforce it. No milestone implies any sibling milestone,
closes a ticket, or changes the route. DS-249 rules may use verified observations
with pinned policy, capability checks, and authorization to request a transition.

## Compatibility and migration

Existing `planning-artifact-v1alpha1` schemas and all ten default templates remain
unchanged. Existing workflows keep their declared output contracts. The new family
is opt-in and does not widen or silently replace `product_brief` or `delivery_plan`.
Existing delivery-evidence outputs likewise remain valid; conversion to a handoff
observation requires typed facts and verified evidence, never generic run success.

Migration creates a new artifact with `lineage.kind = migration`, exact source,
source schema family, key mapping, and reviewable notes. It never edits old bytes,
approvals, transcripts, annotations, artifact bindings, or published links.
Supported source pairs are `product_brief` to `feature_brief` and `delivery_plan`
to `story_backlog`. The source must independently pass its legacy JSON Schema.

For a brief, retain all old problem/users/outcomes/scope and preserve assumptions,
constraints, risks, and success measures in cited source evidence. Derive new
requirements and decisions explicitly for review; never invent evidence. For a
delivery plan, use a total one-to-one mapping from each unique legacy story ID to
a stable new key; preserve outcome, scope, acceptance criteria and mapped
dependencies, and retain required context, release slices, integration strategy,
dependency strategy, and shared validation as source evidence with migration notes.
Duplicate legacy IDs block migration pending an explicit reviewed disambiguation.
Missing requirement/repository knowledge stays an open decision or unknown impact.
No automatic migration utility or guessed provider IDs are introduced here.

Migration approval is new and revision-bound. Reverse compatibility is an explicit
lossy projection with diagnostics: old formats cannot express unknown impact,
retirement/splits, native links, or typed handoffs. Do not round-trip such projections
as canonical data. Legacy workflows may consume an intentionally prepared legacy
artifact retaining source provenance; new backlog publication consumes the new
contract only.

## Markdown and executable evidence

[`planningMarkdown`](../../../scripts/feature-planning.mjs) deterministically renders
human headings, stories, acceptance criteria, scope, impact, dependencies, decisions,
evidence, lifecycle, and provenance from canonical structured content. Untrusted
text is escaped. Markdown is a representation, never a second writable backlog;
raw JSON and original history remain available. Editing a projection is a proposed
revision that must be parsed, reviewed and revalidated, not an implicit content write.
Projection processor version/digest belongs in the artifact representation record.

The [examples](../../../examples/feature-planning/) and
[shipped projections](../../../templates/feature-planning/) illustrate all four
records. The semantic tests cover uniqueness, references, cycles, revisions,
retirement/splits, migrations, distinct repository-impact states and observation
bindings. Real draft-2020-12 compiler tests validate fixtures and reject unknown
nested fields, contradictory states and invalid evidence. Run:

```text
go -C runtime test ./tests/planningartifacts
node --test tests/feature-planning.test.mjs
node scripts/schema-tool.mjs check
```

Closed impact/lifecycle/decision unions prevent contradictory flags. Separate
content, external links and observations keep one authority for each concern.
Registry snapshots and Markdown intentionally repeat historical information with
explicit provenance; neither is another mutable business backlog.
