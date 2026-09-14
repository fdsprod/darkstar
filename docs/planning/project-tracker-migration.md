# Project, repository, and tracker migration plan

> [Documentation index](../README.md) | [DS-230 contract](../architecture/work/PROJECT_REPOSITORY_TRACKER_MODEL.md)

## Scope and baseline

This is the forward migration plan for DS-230 / DAR-153. It defines requirements
for implementation, not a claim that migration has shipped. DS-231 owns repository
membership rollout; DS-246 introduces the built-in tracker adapter; DS-248
separates source ticket identity from execution lifecycle; DS-249 supplies explicit
intake/milestone rules. DS-235 and DS-238 supply planning and adapter contracts.

Today `ProjectProjection` in `runtime/src/ports/statestore/work.go` has project
identity and source hash, while work projections carry execution status. Those
records are not yet the new repository membership or authoritative ticket model.
Existing repository paths/configuration, stored preparation manifests, owned
worktrees, and event history must be inventoried together. A source hash alone
cannot reconstruct a Git repository or an external ticket identity.

## Ordered rollout

1. Add versioned repository, membership, source-binding, source-ticket, and run-scope
   representations through the DS-008 migration runner. Use transactional,
   checksummed forward migrations with backups and an explicit minimum reader
   version. Keep historical events, command responses, artifacts, transcripts,
   approval scopes, external-effect evidence, and IDs byte-preserved.
2. Backfill repository records from verified legacy registration/configuration
   coordinates. Canonicalize common Git directories before deduplication, so two
   projects over one checkout receive one shared repository ID and separate
   memberships. Create durable migration mappings keyed by original project and
   source record/revision; rerunning the migration must not create duplicate IDs.
   Do not merge separate clones using remote URL, display name, or content hash.
3. Preserve each existing project and work ID. Map each unambiguous legacy project
   registration to one active membership. Retain the old single-repository fields
   as a compatibility projection of that membership, never a second writable
   authority. Translate legacy configuration into project defaults and membership
   settings with recorded provenance and identical resolved behavior.
4. Existing native work receives a linked built-in ticket in a native namespace,
   retaining title, details, priority, evidence, and readable native history.
   Keep the existing work record as execution identity. Native business status
   is initialized from an explicitly versioned legacy mapping, recorded as a
   migration observation; no future run outcome automatically mutates it.
   Verified external references retain their source/account/native identity.
   Unresolved legacy references remain preserved migration evidence and block
   source refresh/write until explicitly mapped; never pretend they are verified
   external tickets or silently replace them with a new native ticket.
5. Historical runs retain their original manifests and source snapshots. Append
   compatibility mappings to a single-repository scope only where exact legacy
   evidence proves it. If a base commit or repository cannot be established,
   present `legacy_unresolved` in the migration/read projection (not as an
   executable new-run scope). Do not infer history from today's project settings.
6. Existing active runs retain leases, paths, branches, base SHAs, operations, and
   source lineage. Reconcile them before resuming. Bridge old canonical-directory
   and new repository-ID lease keys atomically so mixed records cannot permit
   two writers. Ambiguous identity blocks preparation; it must not free the old
   lease. Do not run old and new daemon writers simultaneously against one DB.
7. Enable new API/CLI operations only after backfill and projection checks pass.
   New native projects default to a built-in source and zero memberships.
   Source selection and publication configuration remain separate commands.
   Expose incomplete mappings as actionable migration state, retaining read access.

## Compatibility boundary

An old single-repository registration request creates a project and one membership
atomically. A legacy update targets the sole membership under revision checking.
An old read receives the exact sole-repository projection; for zero or multiple
memberships, an old representation request fails with an explicit unsupported
cardinality error instead of returning an arbitrary repository or empty fake path.
The versioned new representation supports all cardinalities. DS-231 must specify
the concrete API version/error codes and update CLI/dashboard consumers together.

For existing single-repository workflows, omitted repository selection resolves
once to the sole active membership with provenance in the new run snapshot.
The declared node capability determines read-only versus one-writer scope; a
legacy write permission must not become a broad project permission. Repository-
free workflows may use `none`. Zero repositories with a required repository, or
multiple repositories without an explicit selection/binding, fail preparation.
Discovery from a repository shared by projects returns candidates and requires
an explicit project choice; it cannot select one by path order.

Source switching does not migrate existing tickets. The separately authorized
native/external migration defined by DS-230 uses per-ticket idempotent operations
and verified mappings. Failed or partially completed migration remains resumable;
source records and successful external tickets are not deleted to simulate rollback.
Rollback of a failed storage migration leaves the prior DB intact before serving
traffic. After successful upgrade, recovery uses backup/forward repair with all
new external effects reconciled; an older binary must refuse the upgraded DB.

## Required implementation acceptance matrix

| Case | Required proof |
|---|---|
| Existing one-repository project | IDs, resolved configuration, branch/worktree ownership, workflow behavior, artifacts, and history remain equivalent. |
| Two projects register one Git common directory through path aliases | One repository ID; independent membership config; one global writer lease. |
| Separate clone with same remote | Separate local identity; no automatic branch/PR adoption. |
| Zero-repository project | Planning/review/publication works; repository-required node blocks before provider invocation. |
| Multi-repository planning | Explicit frozen read set; one or zero writer targets; no coordinated Git mutation authorization. |
| Configuration conflict | Deterministic documented winner with provenance; restrictive ceilings remain effective. |
| Remove/re-add membership during a run | New selection excluded while removed; frozen scope/history retained; no deletion or implicit cancellation; new revision on re-add. |
| Remove shared membership | Other projects and repository leases unaffected. |
| Revoke account/repository access | Pending effects pause/reconcile; no fallback identity/account and no uncertain lease release. |
| Legacy run lacks resolvable repository evidence | Readable original history; explicit unresolved projection; execution blocked until reconciled. |
| Restart halfway through backfill | Stable mappings and no duplicate membership, ticket, or operation IDs. |
| Switch source with active work | New intake uses new binding; old work/operations keep pinned source; no milestone replay to new tracker. |
| Native-to-external migration partly succeeds | Exact linked successes retained, only unresolved operations retried, no title matching or source deletion. |
| External source is read-only | Observation/intake works; creation/transition reports missing capability before mutation. |
| Run succeeds, fails, or is cancelled | Execution outcome changes; ticket business status changes only through an explicit configured rule. |
| Old API sees zero/multiple memberships | Clear compatibility failure; no first-repository approximation. |
| Replay and rebuild | Original events/artifacts unchanged; new projections reconstruct from versioned facts and migration mappings. |

These are downstream behavioral tests, not prompt-only assurances. Architecture
validation for DS-230 checks registered supersession, current-decision preflight,
linked documents, and preservation of the DS-004 historical body.
