# DARKSTAR MVP Work and Git Model

> [Documentation index](../../README.md)

**Decision:** DS-004

**Status:** Accepted for MVP

**Scope:** Work identity, repository topology, mutation ownership, publication,
revision, reconciliation, and non-destructive recovery

## 1. Decision

The MVP uses one linear delivery line per work item and repository:

- one work item owns at most one delivery branch and one pull request;
- every write-capable run for that delivery line uses one DARKSTAR-managed
  worktree attached to the owned branch;
- stories and implementation points execute sequentially on that branch;
- each accepted point revision owns one atomic commit;
- the shipped policy keeps commits local until integrated validation, then pushes
  once and creates one final pull request; and
- an optional early-draft policy pushes accepted point commits to the same branch
  and updates the same pull request.

Planning and read-only work may run concurrently. The MVP does not run concurrent
writers for one repository, create story branches, merge point branches, rewrite
published history, or coordinate a work item across repositories.

This model deliberately favors a small, inspectable state space over maximum Git
parallelism. It gives every mutation one durable owner and makes retries
reconcilable from SQLite, Git, and the delivery provider without treating model
memory or process exit as proof of success.

## 2. Normative language and invariants

The words **must**, **must not**, **should**, and **may** are normative.

The following invariants hold for the MVP:

1. A project registration identifies exactly one canonical Git repository.
2. A work item has at most one active delivery line for that repository.
3. A delivery line has exactly one owned local branch and at most one pull
   request.
4. The branch is never attached to more than one worktree at a time.
5. At most one write lease exists for a repository, including its linked
   worktrees.
6. A write-capable attempt may mutate only the worktree named in its immutable
   attempt manifest.
7. A point revision starts from a clean, reconciled owned commit.
8. A point revision creates no successful commit until its validators and any
   configured checkpoint pass.
9. An accepted point revision creates exactly one owned commit. A retry adopts
   that commit rather than creating another one.
10. Publication is fast-forward only. DARKSTAR never force-pushes in the MVP.
11. Published commits are immutable. Corrections append commits.
12. DARKSTAR never discards changes from a user-owned checkout or an unowned
    branch/worktree.
13. An ambiguous external state pauses for reconciliation; it is not guessed
    into success.

## 3. Work hierarchy and stable identity

Runtime IDs are opaque, immutable, locally generated identifiers. Display names,
source keys, branch names, sequence numbers, and provider IDs are attributes, not
primary identity. Every mutable logical object also carries an integer revision;
history is append-only.

```text
Project
└── WorkItem
    ├── Run [0..n]
    │   └── NodeVisit [0..n]
    │       └── Attempt [1..n]
    ├── Story [0..n]
    │   └── ImplementationPoint [0..n]
    │       └── PointRevision [1..n]
    │           └── Attempt [1..n]
    └── DeliveryLine [0..1 per repository]
        ├── WorktreeLease [0..n over time]
        ├── Commit [0..1 per accepted PointRevision]
        ├── Publication [0..n]
        └── PullRequest [0..1]
```

| Object | Stable identity | Parent / scope | Git meaning |
|---|---|---|---|
| Project | `project_id` | Local DARKSTAR installation | Owns repository registration, configuration, validation, and integration settings. |
| Work item | `work_item_id` | Project | Durable requested outcome. Owns the delivery line; source keys such as `DAR-7` are aliases. |
| Run | `run_id` | Work item | One frozen route execution. A retry stays in the run; an explicit rerun creates another run and may reattach to the same delivery line. |
| Node visit | `visit_id` | Run plus route revision/epoch | One logical workflow-node activation. It has no independent Git ownership. |
| Story | `story_id` | Work item | Versioned outcome/acceptance unit derived from an accepted plan. It groups points but does not own a branch in the MVP. |
| Task | no first-class aggregate | — | “Task” is planning or imported-source vocabulary. It must resolve to a work item, story, or implementation point before scheduling. It never owns a branch, worktree, commit, or attempt. |
| Implementation point | `point_id` | Story | Smallest schedulable write outcome and default commit boundary. Ordering and dependencies are explicit. |
| Point revision | (`point_id`, `revision`) | Point | Immutable execution contract for one accepted or superseded result. A corrective revision receives a new revision number. |
| Attempt | `attempt_id` | Node visit or point revision | One executor invocation. It may hold a write lease but never owns a branch, PR, or successful outcome merely by exiting. |
| Delivery line | `delivery_line_id` | Work item plus repository | Durable owner of base, branch, expected tip, publication state, and pull request identity. |
| Mutation operation | `operation_id` | Delivery line | Idempotency identity for branch creation, commit, push, PR create/update, and cleanup. |

### 3.1 Identity across planning revisions

Accepted plan artifacts must carry DARKSTAR object IDs after first ingestion.
Later revisions must explicitly retain an existing `story_id` or `point_id` to
revise that logical object. DARKSTAR does not reuse identity by comparing titles or
model-generated prose.

- A retained ID creates a new immutable object revision.
- An omitted former object becomes `retired`; its history and commits remain.
- A new object receives a new ID even when its title resembles a retired object.
- Duplicate IDs in one accepted plan are invalid.
- A point cannot move to another story while retaining identity in the MVP;
  retire it and create a new point.

## 4. Default Git topology

```text
base repository/ref
        │ freeze base_commit
        ▼
work-item branch ── point A/r1 ── point B/r1 ── point A/r2 ── final validation
        │                 │              │            │
        │                 └ atomic commits; A/r2 supersedes A/r1
        ▼
DARKSTAR-managed worktree (one attached instance, leased by attempts)
        │
        ├── default: one fast-forward push after final validation
        └── optional: fast-forward push after each accepted point revision
                              │
                              ▼
                  one final or incrementally updated PR
```

The delivery line freezes these coordinates before its first write:

- canonical repository identity and common Git directory;
- base remote, base repository identity, base ref, and resolved `base_commit`;
- push remote and push repository identity;
- deterministic branch name;
- canonical worktree path when attached;
- expected local and remote tips; and
- delivery provider and target base branch.

The default branch name is derived once from a sanitized source key or work-item
slug plus a stable short work-item ID. The stored value is authoritative. A rename
requires an explicit operation before publication and is not inferred from a later
title change.

### 4.1 Stories and concurrency

Dependency-ready stories may perform read-only research concurrently, but all
write-capable story points join one deterministic queue on the delivery line. The
queue is ordered by accepted story order, explicit point dependencies, then stable
ID as a tie-breaker. Only the queue head can acquire the repository write lease.

This intentionally serializes independent stories. Parallel story branches and an
integration branch are deferred until DARKSTAR has explicit merge/conflict
semantics.

### 4.2 Worktree ownership

The branch belongs to the work item's delivery line. A worktree is a disposable
execution projection attached for one active write-capable run. Attempts borrow it
through leases; they do not create private branches.

Creating a worktree must not change the user's current branch, index, working tree,
or untracked files. DARKSTAR may mutate shared Git administrative metadata only
while holding the repository manager lock.

On normal completion DARKSTAR may detach an owned, clean worktree after recording
its branch and tip. Cleanup never deletes the owned branch or remote ref. A dirty,
missing, locked, or externally moved worktree enters reconciliation instead of
automatic removal.

## 5. Mutation ownership and commit protocol

| Mutation / effect | Durable owner | Preconditions | Commit point | Retry / recovery proof |
|---|---|---|---|---|
| Create local branch | Delivery line + operation | Base and name frozen; name absent | Ref exists at `base_commit` and operation is recorded | Stored ref and expected SHA match. An unowned collision blocks. |
| Attach worktree | Run attachment operation | Owned branch is not attached elsewhere; canonical path unused | Git reports the branch at the recorded path and metadata is recorded | Enumerate worktrees and compare branch, path, and tip. |
| Modify files | Active attempt lease | Owned worktree clean at expected tip; attempt manifest permits write | No durable success yet; changes are a candidate | Reconcile process/session and exact workspace fingerprint. Never start a second writer when uncertain. |
| Accept candidate | Point revision | Provider finished; diff captured; validators and checkpoint passed | Candidate manifest and validation evidence committed in SQLite | Matching accepted revision exists; continue to commit reconciliation. |
| Create commit | Point revision + operation | Candidate accepted; parent equals expected tip; scoped diff matches candidate | New HEAD contains the operation and point trailers; expected tip advances transactionally | Search the expected first-parent position for the operation trailer and verify tree, parent, and trailers. Adopt exact match; otherwise block. |
| Push | Publication operation | Local expected tip known; fetched remote is absent or its tip is an ancestor | Remote ref equals intended tip | Fetch and compare. Equal is success; ancestor permits retry; divergence blocks. |
| Create PR | Delivery line + operation | Published head known; base/head repositories and refs frozen | Exactly one provider PR identity is recorded | Query exact head/base. Adopt only an exact owned-marker match; ambiguity or unowned PR blocks. |
| Update PR | PR update operation | Recorded PR still targets owned head/base | Owned body section/checklist reflects desired revision | Read back provider state; patch only DARKSTAR-owned markers and preserve human text. |
| Detach worktree | Run attachment operation | Lease released; worktree clean and reconciled | Worktree no longer registered; cleanup event recorded | Absence is success. Dirty or unexpected path state blocks. |

### 5.1 Candidate and commit boundary

Before a point starts, DARKSTAR records the expected parent commit, tracked-tree
hash, status, and permitted path scope. The implementer may change the owned
worktree but may not commit, push, switch branches, alter remotes, or create a pull
request. Those are daemon-owned operations.

After the provider returns, DARKSTAR:

1. inventories tracked, untracked, ignored, submodule, and conflict state;
2. rejects changes outside the permitted workspace/path policy;
3. captures the candidate diff and file manifest as immutable evidence;
4. runs the configured deterministic validation;
5. waits for the point checkpoint when configured;
6. stages exactly the candidate manifest, rejecting extra or missing changes; and
7. creates the atomic commit with an argument-array Git invocation.

Every owned commit includes machine-readable trailers:

```text
Darkstar-Work-Item: <work_item_id>
Darkstar-Run: <run_id>
Darkstar-Story: <story_id>
Darkstar-Point: <point_id>
Darkstar-Point-Revision: <revision>
Darkstar-Operation: <operation_id>
```

Timestamps and the resulting SHA are recorded after creation, not used as
idempotency keys.

### 5.2 Failure and rejection before commit

A failed, cancelled, or rejected candidate never becomes a successful commit.
DARKSTAR preserves its diff, untracked-file bundle, logs, and validation evidence,
then may restore only its own isolated worktree to the recorded parent after the
write lease is released. Restoration requires proof that:

- the path is the recorded DARKSTAR-owned worktree;
- the branch and HEAD equal the recorded pre-point state;
- no other lease or live owned process exists; and
- the current changes match the captured candidate manifest.

If any proof fails, the worktree becomes `reconcile_required`. DARKSTAR does not
clean it automatically. User checkout changes are never candidates for this
restoration protocol.

`request_changes` creates another attempt for the same point revision while the
candidate is unpublished and uncommitted. `reject` retires the candidate and
returns the point to a user-directed route decision; it does not silently skip a
required point.

## 6. Revision rules

### 6.1 Before commit

Requested changes replace the uncommitted candidate for the same point revision.
All candidate versions remain auditable, but only the accepted candidate can be
committed.

### 6.2 After commit but before push

An upstream artifact change or review finding creates a new point revision. The
correction is a new atomic commit that supersedes the earlier revision. The MVP
does not amend, squash, reset, rebase, or reorder even unpublished owned commits
automatically. This costs some history neatness in exchange for one recovery rule.

### 6.3 After push or PR creation

Published history is append-only. A correction creates a new point revision and
commit on the same branch, runs affected point and integrated validation, then
fast-forwards the same remote branch and updates the same PR.

The PR checklist presents only the latest accepted revision as current and marks
the earlier revision superseded. Both commits remain visible. A rejected review
finding routes to the smallest affected point or upstream artifact; no untracked
“quick fix” commit is allowed.

### 6.4 After merge or terminal closure

A merged delivery line is immutable and complete. Further source changes require a
new work item and delivery line. A closed-but-unmerged PR pauses for an explicit
reopen, abandon, or replacement-work-item decision; DARKSTAR does not silently
create PR number two for the same work item.

## 7. State transition model

### 7.1 Delivery line

| From | Event | To | Required durable fact |
|---|---|---|---|
| `uninitialized` | Base and topology frozen | `prepared` | Repository, remotes, base ref/SHA, and branch name recorded. |
| `prepared` | Owned branch and worktree reconciled | `local` | Local ref and attached path match expected tip. |
| `local` | Accepted point commit | `local` | Expected tip advances; point revision maps to one commit. |
| `local` | Final validation passes | `validated` | Validation snapshot names the exact tip. |
| `local` / `validated` | Publish succeeds | `published` | Remote ref equals expected local tip. |
| `published` | PR create/adopt succeeds | `pr_open` | Exact provider PR ID, URL, head, base, and marker recorded. |
| `pr_open` | Corrective point accepted | `local` | Local tip advances; PR remains associated but is behind until republished. |
| `pr_open` | Remote branch catches up | `pr_open` | PR head SHA equals expected tip and owned content is reconciled. |
| `pr_open` | PR merged | `merged` | Provider merge identity/SHA recorded. |
| `pr_open` | PR closed unmerged | `closed` | Provider closed state recorded; user decision required. |
| Any nonterminal | State cannot be proved | `reconcile_required` | Conflicting observations and last safe expected state recorded. |
| `reconcile_required` | Explicit reconciliation succeeds | Prior legal state | Resolution actor, evidence, and chosen state recorded. |
| Any nonterminal | Explicit abandon | `abandoned` | Branch/worktree preservation or cleanup disposition recorded. |

Terminal states are `merged` and `abandoned`. `closed` is not terminal until the
user reopens or abandons it.

### 7.2 Point revision

| From | Event | To |
|---|---|---|
| `planned` | Dependencies satisfied and lease available | `ready` |
| `ready` | Attempt starts from reconciled parent | `running` |
| `running` | Provider produces candidate | `validating` |
| `validating` | Validation fails | `failed` or declared repair attempt |
| `validating` | Validation passes; checkpoint required | `awaiting_approval` |
| `validating` | Validation passes; no checkpoint | `accepted` |
| `awaiting_approval` | Request changes | `running` with a new attempt |
| `awaiting_approval` | Reject | `rejected` |
| `awaiting_approval` | Approve | `accepted` |
| `accepted` | Owned commit reconciled | `committed` |
| `committed` | Required publication reaches exact commit | `published` |
| `committed` / `published` | Later revision supersedes it | `superseded` |
| Any active state | Ambiguous writer/workspace state | `reconcile_required` |

Attempts remain append-only execution history. Retrying a provider or Git
operation never rewinds an attempt or point state; it creates a new attempt or
reuses the same mutation operation ID as appropriate.

## 8. Repository and delivery edge cases

### 8.1 Dirty repositories

The user's primary checkout may be dirty. Because DARKSTAR creates a linked
worktree directly from the frozen base commit, that dirt does not enter the
delivery line and is not a reason to clean, stash, reset, or switch the user's
checkout.

DARKSTAR must still pause when:

- the target branch is checked out in the user checkout or another worktree;
- the DARKSTAR-owned worktree is dirty before a new point and the dirt cannot be
  attributed exactly to the last recorded attempt;
- an in-progress Git operation, conflicted index, unsafe submodule state, or Git
  lock affects the owned worktree; or
- canonical repository/worktree paths no longer match their records.

Automatic stashing is rejected. A stash is repository-global, hard to attribute,
and easy to strand or apply to the wrong checkout.

### 8.2 Existing local or remote branch

Branch existence is not ownership proof.

- If the branch is recorded for this delivery line and its local/remote tips match
  a recoverable expected state, DARKSTAR reattaches and reconciles it.
- If the name exists without matching durable ownership and commit trailers,
  creation fails with a branch-collision error.
- If an owned remote branch contains unexpected commits or diverges, publication
  pauses. DARKSTAR does not merge, rebase, reset, delete, or force-push it.
- Adopting an unowned branch requires an explicit future/import operation; it is
  not an MVP automatic recovery path.

### 8.3 Existing pull request

Before creation, the delivery connector queries the exact head repository/ref and
base repository/ref.

- An already recorded PR with matching coordinates is reconciled and reused.
- An unrecorded PR may be adopted automatically only when it contains the exact
  DARKSTAR ownership marker and exactly one candidate exists.
- A PR without the marker, multiple candidates, mismatched base, or mismatched
  head blocks for user reconciliation.
- DARKSTAR updates only a delimited owned section of the PR body and preserves
  human-authored content outside it.

### 8.4 Forks

Fork delivery is supported only as an explicitly resolved topology, not as an
automatic fork-creation feature. The delivery line records separate base and push
repository identities. The connector must prove that the push repository is in
the same provider network, the user can push the head branch, and a PR can target
the configured base.

The local branch name remains the delivery-line branch. The PR head is fully
qualified by push owner/repository plus branch, so a same-named upstream branch
cannot be mistaken for the owned head. If provider identity, permissions, or fork
relationship changes, publication pauses before mutation.

### 8.5 Base movement

The base branch may advance after the delivery line freezes `base_commit`. That
does not silently change the run baseline. Final validation and PR creation report
the current merge-base and whether the provider considers the branch mergeable.
Updating the base through merge or rebase is an explicit user/policy operation and
is outside the MVP automatic path.

## 9. Recovery and reconciliation

Recovery compares three authorities:

- SQLite for intended ownership, operation IDs, state, and expected SHAs;
- Git for refs, commits, trees, worktrees, and commit trailers; and
- the delivery provider for remote refs and pull-request identity/state.

The daemon must record intent before starting a Git or provider mutation and record
the observed result afterward. On restart it reconciles in this order:

1. acquire the repository reconciliation lock;
2. enumerate canonical repository and worktree state without mutation;
3. fetch only when remote observation is required and policy permits network use;
4. compare current state with the operation's precondition and intended result;
5. classify the operation as `not_started`, `committed`, `safe_to_retry`, or
   `ambiguous`; and
6. adopt, retry with the same operation ID, or enter `reconcile_required`.

The recovery algorithm never uses a provider's “done” message, a command exit code
alone, branch-name similarity, commit-message subject, or PR title as proof.

Examples:

- Crash after commit creation but before SQLite result: find the operation trailer
  at the expected first-parent position, verify parent/tree/trailers, and adopt it.
- Crash during push: fetch the remote; equality means success, an ancestor permits
  the same fast-forward retry, and any other relationship is ambiguous.
- Crash during PR creation: query exact head/base plus ownership marker; reuse the
  single match or pause.
- Crash with an active provider: preserve the lease until provider/process
  reconciliation proves it stopped; do not start a second writer.

DS-003 defines the transaction/outbox mechanics and fault-injection boundaries.
This document defines the Git-specific ownership facts that those mechanics must
persist and reconcile.

## 10. Rejected alternatives and tradeoffs

| Alternative | MVP decision and tradeoff |
|---|---|
| Branch and PR per run | Rejected. Reruns would fragment one requested outcome and make PR deduplication ambiguous. Runs attach to the work-item delivery line instead. |
| Branch per story plus integration branch | Rejected for MVP. It enables write parallelism but requires merge ordering, conflict ownership, partial-story delivery, and multi-branch recovery semantics. |
| Branch or commit per attempt | Rejected. Attempts are retries, not user-visible outcomes; promoting attempt identity into Git would publish failures and duplicate work. |
| Treat tasks as a mandatory runtime level | Rejected. It duplicates stories/points and adds ceremony. External or planning “tasks” must resolve to an existing schedulable level. |
| Let the provider commit, push, or open PRs | Rejected. Provider retries and conversational state cannot provide deterministic side-effect ownership. The daemon owns Git and delivery mutations. |
| Commit a point before its approval | Rejected. Rejection would require history rewriting or a compensating commit for a result that was never accepted. Keep an auditable candidate until approval. |
| Amend/squash unpublished commits automatically | Rejected for MVP. Cleaner history is not worth a separate recovery path and the risk of misclassifying publication. Corrections append. |
| Rebase or force-push after publication | Rejected. It invalidates recorded SHAs and review anchors and can overwrite external work. Publication is fast-forward only. |
| Automatically adopt a same-named branch or PR | Rejected. Names and titles are not ownership proof. Exact stored identity, coordinates, markers, and commit trailers are required. |
| Stash or clean a dirty user checkout | Rejected. It mutates state outside DARKSTAR ownership and creates difficult recovery cases. Linked worktrees isolate delivery work. |
| One long-lived project worktree | Rejected. State can leak between work items and cleanup ownership becomes ambiguous. Worktrees are run attachments to a work-item branch. |
| Multi-repository work item | Deferred. It needs coordinated changesets, partial publication policy, and multiple PR/rollback semantics. The MVP project/work item targets one repository. |

The primary cost of the chosen model is serialized write throughput and a commit
history that retains corrective revisions. The benefit is that every mutable ref,
workspace, commit, push, and PR has one stable owner and one conservative recovery
rule.

## 11. Required implementation records

DS-110, DS-112, DS-118, and DS-150 may choose storage layout, but their public
contracts must preserve at least:

- the stable identities and relationships in section 3;
- canonical repository/common-Git-dir/worktree paths;
- base and push repository identities, remotes, refs, and frozen SHAs;
- delivery-line branch, expected local tip, expected remote tip, and PR identity;
- worktree attachment and repository write-lease ownership;
- immutable point-revision inputs, candidate manifest, validators, and evidence;
- point-to-commit mapping and the commit parent/tree/trailers;
- mutation operation ID, precondition, intended result, observation, and state;
- supersession relationships for corrective revisions; and
- explicit cleanup, abandonment, adoption, and reconciliation decisions.

## 12. Contract-test matrix

The downstream suites must cover at least:

| Scenario | Expected result |
|---|---|
| Four points under default policy | Four sequential atomic commits, one final push, one final PR. |
| Early draft policy | Same branch and PR; each accepted point fast-forwards the remote and updates the checklist. |
| Provider retry before acceptance | New attempt, no duplicate commit. |
| Crash after commit before DB acknowledgment | Existing matching commit is adopted. |
| Crash/timeout during push | Remote equality adopts; ancestor retries; divergence blocks. |
| Crash during PR creation | Exact owned PR is adopted; no duplicate PR. |
| Request changes before point approval | Candidate retained as evidence, new attempt, no commit until approval. |
| Reject point | No successful commit or publication; route waits for explicit decision. |
| Revise committed unpublished point | New superseding commit; earlier commit retained. |
| Revise after push/PR | New superseding commit fast-forwards the same branch and updates the same PR. |
| Dirty user checkout | User state unchanged; isolated worktree is created from frozen base. |
| Dirty owned worktree of known attempt | Reconcile/resume or preserve candidate; never start a second writer. |
| Dirty owned worktree of unknown origin | `reconcile_required`; no reset or cleanup. |
| Unowned branch-name collision | Fail before mutation. |
| Owned existing branch | Reattach only when tips and ownership evidence reconcile. |
| Existing unowned PR | Block; do not adopt or create a duplicate. |
| Configured fork | Push qualified fork head and create one PR to frozen upstream base. |
| Remote branch diverges | Block; no merge, rebase, reset, deletion, or force push. |
| Base advances | Keep frozen base; report mergeability and require explicit update policy. |
| PR merged, then more work requested | Require a new work item; do not reopen the completed delivery line. |

These cases are acceptance inputs for DS-112, DS-118, DS-152, DS-153, DS-157,
DS-194, and DS-195.

[`examples/repositories/golden-repositories.json`](../../../examples/repositories/golden-repositories.json)
and [`scripts/repository-fixtures.mjs`](../../../scripts/repository-fixtures.mjs)
materialize clean, dirty, branched, and linked-worktree repositories from fixed
bytes and commit metadata. Normalized observations exclude absolute paths, the
materializer refuses an existing destination, and the contract suite compares
independent materializations to detect nondeterminism.
