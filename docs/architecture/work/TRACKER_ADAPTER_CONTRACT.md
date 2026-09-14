# Ticket sources and deterministic writers

> [Documentation index](../../README.md) | [Project model](PROJECT_REPOSITORY_TRACKER_MODEL.md) | [Feature planning](../artifacts/FEATURE_PLANNING_CONTRACT.md)

**Status:** Versioned port contract and executable reference fixtures.  
**Issue:** DS-238 / [DAR-160](https://linear.app/darkstar-dev/issue/DAR-160)  
**Contract:** `darkstar.tracker/v1alpha1`

## Boundary and implementation status

The [shared vocabulary](../../../runtime/src/ports/tracker/tracker.go),
[source interfaces](../../../runtime/src/ports/worksource/tracker.go), and
[writer interfaces](../../../runtime/src/ports/ticketwriter/writer.go) are
application-owned Go ports. Existing `worksource.Source.Fetch` is unchanged;
legacy imports do not acquire tracker browsing or writer authority. This version
is an in-process contract, not a new HTTP endpoint or persisted JSON encoding.
An out-of-process codec must explicitly encode the closed variants and reject
unknown versions, nil variants and contradictory payloads before use.

`TrackerSourceV1` discovers capabilities and reads exact ticket observations.
Optional `TrackerBrowserV1` lists/searches/filters/pages them. Neither interface
contains publication, workflow selection, callbacks into the daemon, or run
creation. `ticketwriter` separately exposes capability observation, inspection
of operation requirements, one desired effect, and read-only reconciliation.
Adapters receive resolved operations; the daemon owns rules, authorization,
ordering, durable intent, retry scheduling and outcome decisions under the
[execution](../workflow/execution-semantics.md),
[approval](../security/APPROVAL_AND_PERMISSION_MODEL.md), and
[recovery](../recovery/RECOVERY_MODEL.md) contracts.

Built-in is a first-class source/writer using the same identities, capabilities,
freshness, effects and receipts. Direct native ticket creation carries a retained
user-request artifact; planned creation carries canonical story identity and an
exact backlog reference. General progress/clarification reports carry retained
evidence; milestone reports reference a typed handoff observation. Neither requires
inventing a story or milestone to fit an unrelated operation.

DS-246 implements the production native tracker; DS-239/240 implement provider
connections; DS-241 implements the durable publication coordinator; DS-249 and
DS-252 implement rules and progress synchronization. This change implements none
of those controllers or service integrations. Jira is a workflow stress fixture,
not an enabled production adapter. GitHub Projects boards/custom fields remain
excluded. Reviewed-backlog publication selects exactly one Linear or GitHub
Issues `Scope`; built-in writing is supported outside that publication workflow.

## Identity, configuration and observations

`Namespace` identifies provider kind, host, tenant/account authority and stable
native namespace. `TicketRef` adds an immutable native ticket ID. `Scope` adds a
binding container: a Linear team within its workspace namespace, or a GitHub
repository selection. Linear team moves change observed placement and binding
selection without changing workspace-scoped issue identity. Display keys, issue
numbers, titles, URLs, filters and implementation versions are never ticket
identity. GitHub repository transfers/issue moves that change native namespace
require an explicit old-to-new observation, retaining prior references.

Bootstrap discovery takes `AdapterConfigPin`: exact contract/implementation,
installation/account, binding revision and non-secret configuration digest.
Discovery returns the completed `Pin` with a capability fingerprint. Runs and
operations retain that full pin; later discovery must match it exactly. Secrets,
OAuth tokens, API keys, provider query syntax and provider-specific configuration
stay behind adapters. Credential rotation may preserve the pin only when it
preserves the selected authority and non-secret configuration; changed access is
still rechecked before effects. A changed capability/configuration/implementation
blocks with visible `protocol_drift`, never silently rebinding an existing run.

Normalized tickets contain title/body, stable named IDs for business state,
state reason, type, assignees, labels and priority, separately observed sprint or
cycle membership, relationships, native revision and retained evidence.
`Known`, `Unknown(reason)` and `Unsupported(reason)` distinguish observed data
from incomplete or unavailable concepts. An observed empty list means none;
it must not stand for an unavailable response. Freshness is `Fresh` with revision
and time, `Stale` with last observation and reason, or `NeverObserved`. Missing
and unchanged reads are distinct results. A revision may be an adapter-defined
content digest when a provider has no atomic revision token; it does not promise
provider-side compare-and-swap.

List capability is separate from search/filter/page/refresh support. Filters use
advertised stable field IDs and explicit operators; unsupported fields/operators
are rejected, never dropped. A page has `End` or an opaque `More` cursor bound to
the complete query, scope and pin. Search results are not an authoritative absence
proof, and multiple pages need not form a transactional provider snapshot. Keep
per-ticket freshness and expose stale/incomplete results rather than showing a
partial refresh as a complete empty board.

## Writer discovery and independent states

An inspected `WriteOptions` is time-bounded and pins either a creation issue type
or an exact ticket revision, issue type, workflow revision, business state and
sprint observation. Field definitions have stable IDs, operation-specific
requiredness and one sealed text, number or IDs constraint. Only the IDs variant
carries an explicit unrestricted/allowed/unknown choice constraint; an empty
allowed set means no choices. Text/number fields cannot carry ID constraints.
Edits advertise only editable fields; state changes cannot bypass transitions via
an arbitrary field map. Partial edits omit unchanged fields; clearing a value uses
an advertised empty value and never an implicit omission-to-delete rule.

Available transitions carry stable IDs, readable names, target state IDs,
required fields and observed guards with evidence. The adapter discovers what is
available for this ticket/type, including permissions and provider restrictions;
it does not choose the action. Unknown/false guards, missing required fields,
unknown transition IDs, duplicate IDs or changed status/type/workflow/sprint
block before a write. Providers without transition endpoints may expose a
versioned adapter-owned operation ID, such as GitHub `close:completed` or Linear
`set-state:<state UUID>`, bound to actual advertised provider semantics. English
names are presentation only. Provider guards are rechecked during mutation;
preflight is not an atomic guarantee that remote state cannot change.

| State authority | Representation and rule |
|---|---|
| Tracker business state | `Ticket.BusinessState` and separate reason/type/sprint observations. Owned by the tracker. |
| DARKSTAR execution | Existing run/attempt/workflow records remain authoritative. No duplicate execution lifecycle is introduced in the ticket port. |
| DARKSTAR synchronization | `Pending`, `Reconciling`, `Synchronized(receipt)`, or `Blocked(code, reason)` for one operation. A successful run does not advance this state. |

Versioned rule sets must separate intake predicates, milestone-to-transition
intents and display grouping. Intake names source binding, observed predicates,
target workflow and evidence requirements; transition rules name their milestone,
exact source binding, target operation ID and required evidence; grouping maps
observed IDs to presentation groups with an explicit unknown bucket. Multiple
statuses may group together without being equivalent. Sprint eligibility is a
separate predicate from business status. No inverse mapping or generic
completion-to-close rule is implied. Actual rule schemas/evaluation are DS-249.

## Canonical relationships and effect reconciliation

Each `Intent` freezes operation identity, desired-content digest, one destination,
full adapter pin, authorization reference and exactly one effect: create, edit,
progress report, transition, or relationship. The daemon verifies actual durable
authorization and exact artifact bytes/hashes; a nonempty reference or model claim
does not grant authority. Planning publication approval binds exact backlog and
destination. The coordinator compares all approved active stories and directed
relationships with the effect manifest, so an adapter cannot silently omit a
relationship simply by omitting a link effect.

`LinkStories` retains both canonical story identities, exact backlog and resolved
native endpoints. A dependency has directed `depends_on` semantics; a hierarchy
has `child_of` semantics. A planning split is retained lineage, not automatically
a native parent-child relationship. An adapter must use a supported native
representation, a selected versioned visible managed-body fallback retaining the
direction/story keys/native links, or reject publication. Fallback receipts prove
that representation; they never claim native dependency enforcement. A fallback
cannot be selected silently after a provider error. Existing human body content
is preserved outside the explicitly owned section. Cross-feature and
cross-destination relationships are rejected by this contract.

The daemon journals intent before dispatch. An `Applied` receipt requires actual
read-back evidence for operation ID, desired digest, destination, exact adapter
pin, ticket ID and observed revision. Provider acceptance or HTTP success alone
is not completion. A connection loss after a possible write returns `Uncertain`
and a recovery reference. `Reconcile` takes the same immutable intent and performs
no mutation. It returns the matching receipt, positively proven `NotApplied`, or
continued uncertainty. A negative search or missing response is not proof of
absence. `NotApplied` evidence is also bound to the exact operation/digest/pin/
destination; unrelated evidence cannot authorize retry consideration. A matching
receipt settles the effect without duplicating it. Conflicting/duplicate markers
or changed owned content remain blocked for reconciliation; the adapter never
overwrites arbitrary user edits to manufacture a match.

Errors cross the boundary as safe `ports.Failure`: `unauthenticated`,
`permission_denied`, `unsupported`, `protocol_drift`, `invalid_request`, `conflict`,
`not_found`, `resource_exhausted`, `unavailable`, or `uncertain`, as appropriate.
Rate-limit details may include normalized `retry_after_seconds` or `retry_at_utc`
and a protected evidence reference, never credentials or raw provider bodies.
`Retryable` is scheduling information, never permission to blindly repeat a
potential write. The adapter normalizes provider errors; daemon code does not
branch on English error messages or invent a fixed retry delay.

## Provider capability and mapping matrix

Checked against official documentation on **2026-09-12**. This is documentation
verification plus local contract fixtures, not an authenticated tenant capability
probe or live mutation test. Every production installation must discover actual
permissions, API version, feature availability and configuration; the table does
not grant capabilities. Provider mutation idempotency is not assumed: the
operation-marker/read-back protocol above is DARKSTAR's required adapter policy.

| Dimension | Linear mapping | GitHub Issues mapping |
|---|---|---|
| Destination and identity | Installation/account, workspace, selected team; use issue UUID and discovered team/state IDs. [GraphQL API](https://linear.app/developers/graphql) | Installation/account, host, immutable repository ID; ticket native ID, with number/owner/name retained as routing attributes. Repository REST issue endpoints take owner/repo and issue number. [Issue API](https://docs.github.com/en/rest/issues/issues) |
| Creation/edit fields | `issueCreate` and `issueUpdate`, title/description, team and discovered state IDs; discover supported fields rather than assuming every team field. [GraphQL API](https://linear.app/developers/graphql) | Issue creation/update support title/body, assignees, labels and milestone; supported fields depend on access. No Projects custom fields. Normalize state and state reason separately. [Issue API](https://docs.github.com/en/rest/issues/issues) |
| Hierarchy | Parent/sub-issues are native. Team settings may automate related status changes, which must be observed independently. [Parent and sub-issues](https://linear.app/docs/parent-and-sub-issues) | Native sub-issue list/add/remove/reprioritize endpoints exist; verify host/version/permissions before claiming support. [Sub-issues API](https://docs.github.com/en/rest/issues/sub-issues) |
| Dependencies | Native blocked/blocking relationships; resolved blockers may move to related, so retain canonical planning lineage separately. [Issue relations](https://linear.app/docs/issue-relations) | Native blocking/blocked-by list/add/remove endpoints exist. GitHub dependency support must not be assumed absent or replaced silently by text. [Dependencies API](https://docs.github.com/en/rest/issues/issue-dependencies) |
| Listing, filtering, paging | GraphQL connections/cursors and explicit `pageInfo`; filter issue queries and account for archived-item inclusion. [Pagination](https://linear.app/developers/pagination), [Filtering](https://linear.app/developers/filtering) | Issue-list endpoints support filters and pagination. Issue responses can include pull requests: adapters must exclude them. Search must constrain issue type and account for incomplete/capped results. [Issue API](https://docs.github.com/en/rest/issues/issues), [Search API](https://docs.github.com/en/rest/search/search#search-issues-and-pull-requests) |
| Freshness and lookup | Lookup by stable UUID; timestamps/webhooks or ordered bounded refresh inform retained revisions. Inspect GraphQL errors even for HTTP 200/partial data. [GraphQL API](https://linear.app/developers/graphql) | Exact issue lookup and conditional requests support read-back/refresh; ETags and timestamps are observations, not general mutation CAS. [REST best practices](https://docs.github.com/en/rest/using-the-rest-api/best-practices-for-using-the-rest-api) |
| Business workflow | Team workflow states expose stable IDs; a configured rule selects a state update. Cycles remain separate from business status. [GraphQL API](https://linear.app/developers/graphql), [Cycles](https://linear.app/docs/use-cycles) | Issue open/closed state plus reason; expose bounded operations for these values, not Jira-style arbitrary transition workflows or sprint fields. [Issue API](https://docs.github.com/en/rest/issues/issues) |
| Progress | Dedicated comment operations can carry bounded progress; discovery must verify account permissions. [Linear SDK data mutation](https://linear.app/developers/sdk-fetching-and-modifying-data) | Issue comments can carry progress independent of closing an issue. [Comments API](https://docs.github.com/en/rest/issues/comments) |
| Rate limits | Read request, endpoint and complexity budgets/reset headers; normalize GraphQL `RATELIMITED` responses and avoid blind polling. [Rate limits](https://linear.app/developers/rate-limiting) | Respect primary/secondary budgets and `retry-after`/reset guidance; budgets vary by authentication. [Rate limits](https://docs.github.com/en/rest/using-the-rest-api/rate-limits-for-the-rest-api), [REST best practices](https://docs.github.com/en/rest/using-the-rest-api/best-practices-for-using-the-rest-api) |
| Retry reconciliation | DARKSTAR policy: retained operation marker, exact lookup and read-back before any repeat; partial GraphQL success is uncertain until each effect is observed. [GraphQL error behavior](https://linear.app/developers/graphql) | DARKSTAR policy: operation marker and exact issue/body/relationship read-back, including uncertain create responses; search absence cannot prove that a write failed. [Issue API](https://docs.github.com/en/rest/issues/issues), [Search API](https://docs.github.com/en/rest/search/search#search-issues-and-pull-requests) |

## Executable evidence and design justification

[Contract tests](../../../runtime/src/core/trackercontract/validate_test.go) use
read-only source and separate writer fakes for built-in, Linear, GitHub Issues and
Jira-style custom workflows. Provider-specific observations distinguish GitHub's
bounded issue states/reasons and unsupported sprint, Linear team state IDs/cycles,
and Jira type-specific transitions with required resolution fields and sprint
guards. Shared scenarios verify scoped paging, refresh, uncertain writes,
receipt reconciliation and duplicate-operation rejection. Negative cases reject
configuration/status/type/workflow/sprint drift, unknown capabilities, unknown or
duplicate transitions, missing/invalid fields, unknown guards, relationship loss,
unconfigured fallback and mismatched receipts/absence proofs.

The design uses sealed variants for knowledge, effect, provenance, freshness and
sync outcomes, while keeping provider business state and existing execution state
under separate owners. Stable ticket identity is separated from binding scope and
the exact adapter pin so team moves and configuration changes cannot accidentally
duplicate tickets or retarget pending effects. A sealed field-constraint union
prevents text/number descriptors from carrying unrelated ID restrictions. Unknown
or nil constraints fail preflight; a future codec must reject unknown variants
and mixed sibling payloads before constructing this in-process contract.
