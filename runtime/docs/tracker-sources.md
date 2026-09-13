# Tracker sources

Ticket business data and execution have different owners. The configured tracker
owns title, description, labels, assignees and business status. DARKSTAR retains
observations and execution evidence. Reading or refreshing a ticket does not
prepare, queue or launch a workflow; finishing a run does not close its ticket.

## Built-in tickets

Existing native work IDs remain readable as built-in ticket IDs. The forward
storage migration retains original events and initializes business status using
the recorded `legacy-business-state/v1` mapping. Later execution outcomes do not
update that status. Ambiguous old imports remain unresolved migration evidence.

Open **Board → Tickets** to browse a project's native business tickets, inspect
retained revisions, edit supported fields and choose advertised status actions.
The execution board and existing history remain available separately.

```text
darkstar ticket list <project-id>
darkstar ticket show <project-id> <ticket-id>
darkstar ticket edit <project-id> <ticket-id> --revision <revision> --idempotency-key <key> --title <title>
darkstar ticket transition <project-id> <ticket-id> --revision <revision> --idempotency-key <key> --transition <advertised-id>
```

Inspect the ticket before editing. A stale revision is rejected. After an
uncertain response, retry the same request and key so the daemon can reconcile
the retained operation. Ticket revisions, native operation evidence, execution
events, transcripts and artifacts remain independent.

## Connections and credentials

Connection configuration is versioned independently of project source selection
and code repositories. A Linear connection identifies an account and workspace;
a GitHub connection identifies an account and host. Destination discovery returns
stable team/project or repository IDs. An issues-only GitHub repository can be
selected without adding it to the project's code repositories.

The same setup is available from the CLI. Supply secrets through standard input;
connection commands accept only their stored references.

```text
darkstar tracker credential store <credential-ref> --stdin
darkstar tracker connection add-linear <connection-id> --revision <revision> --credential-ref <credential-ref> --authentication personal_api_key
darkstar tracker connection add-github-token <connection-id> --revision <revision> --host github.com --credential-ref <credential-ref>
darkstar tracker connection add-github-cli <connection-id> --revision <revision> --host github.com --login <login>
darkstar tracker connection list
darkstar tracker connection health <connection-id> --revision <revision>
darkstar tracker connection destinations <connection-id> --revision <revision> --page-size 50
```

Use `oauth` for a Linear OAuth access token. Discovery can return a continuation
cursor; pass it with `--cursor` to retrieve the next bounded page.

The local authenticated API exposes:

| Operation | Endpoint |
| --- | --- |
| Store or rotate a protected credential reference | `POST /api/v1/tracker/credentials/{credentialRef}` |
| Register a Linear connection | `POST /api/v1/tracker/connections/linear` |
| Register a GitHub token connection | `POST /api/v1/tracker/connections/github-token` |
| Register a GitHub CLI connection | `POST /api/v1/tracker/connections/github-cli` |
| List retained connection revisions | `GET /api/v1/tracker/connections` |
| Read an exact connection revision | `GET /api/v1/tracker/connections/{connectionId}/revisions/{revision}` |
| Check the pinned account | `GET /api/v1/tracker/connections/{connectionId}/revisions/{revision}/health` |
| Discover accessible source scopes | `GET /api/v1/tracker/connections/{connectionId}/revisions/{revision}/destinations` |

Credential input has `schemaVersion: 1` and `secret`. It is passed directly to
protected storage, omitted from command history and never echoed. Windows uses
the current user's DPAPI encryption and protected file permissions. Other hosts
require private credential file permissions. Ordinary connection records contain
credential references only. GitHub CLI authentication explicitly selects its
host and login; changing the authenticated native account fails the identity
check instead of silently changing the connection.

Connection setup uses an explicit `connectionId` and immutable `revision`, each
up to 80 letters, digits, underscores or hyphens and beginning with a letter or
digit. Setup probes the provider before retaining the native account identity.
Use a new revision to change non-secret configuration. Rotating a secret under
the same reference preserves existing pins only while its authority still matches.

## Provider observations

Each project selects one versioned backlog source. Existing projects initially
select their built-in namespace. Changing the selection requires its current
binding revision; a concurrent change is rejected. A failed external refresh
retains that external selection and its cached content.

Open **Board → Tickets** to inspect the configured backlog. **Change source**
lists registered connection revisions and discovers accessible teams or issue
repositories. **Refresh source** starts or continues a bounded scan; **Reload
backlog** reads the retained cache. The view identifies tickets returned by the
current scan, earlier observations and previous source selections. Built-in
history and editing remain available below the configured backlog.

```text
darkstar backlog source <project-id>
darkstar backlog select <project-id> --revision <current-binding-revision> --source-file <source.json>
darkstar backlog select-native <project-id> --revision <current-binding-revision>
darkstar backlog refresh <project-id> --revision <current-binding-revision> --search <text> --page-size 50
darkstar backlog list <project-id> --limit 50 --include-previous true
darkstar backlog refresh-ticket <project-id> --revision <current-binding-revision> --ref-file <ticket-ref.json>
```

An external `source.json` contains `kind: "external"`, `connectionId`,
`connectionRevision`, and `scope`. Copy `scope.namespace` (`provider`, `host`,
`tenantId`, `scopeId`) and `scope.containerId` from destination discovery.
An exact ticket reference contains `namespace` and its immutable `id`; take it
from the JSON backlog result. Optional `--query-file` accepts `text`,
`predicates` and `pageSize` for supported provider filters. It replaces the query
flags. Cache pagination uses the returned cursor with `backlog list --cursor`.

Refresh is a bounded daemon operation. Each successful page commits its ticket
observations together with its continuation checkpoint. Restart resumes the
same source and query, and repeated native identities do not create duplicate
tickets. Changing the source or filter starts a separate scan. Both providers
use bounded full scans; provider pagination and retry hints remain internal to
the daemon.

An observation retains its original source identity, content revision and
evidence. The backlog separately records when it was last checked and whether
it was returned by the current scan. An incomplete scan or a changed filter
does not prove deletion. Only an exact lookup can record a positive missing
result, while retaining the last successful content. Source access failures,
archived tickets and tickets outside the selected scope remain distinguishable.

Backlog browsing and refresh create no work items or runs. Previously selected
source observations remain available as history after a source switch.

Linear supports personal API keys and OAuth access tokens, scoped issue filtering,
native UUID lookup, updated-time filtering and cursor pagination. Team placement
is observed separately from workspace-scoped issue identity. GraphQL partial
responses are rejected rather than presented as complete observations.

GitHub uses native repository and issue node IDs. Its search is a literal,
case-insensitive substring match over title and body within bounded issue pages;
an empty filtered page can still have a continuation. It excludes pull requests
and supports issue state/reason filters. Refresh traverses bounded full pages;
incremental updated-time filtering is not advertised. Unread or truncated
metadata is marked unknown. Sprint, priority and issue archival concepts are
unsupported rather than synthesized from labels, milestones or Projects fields.

Both adapters retain original response bytes with provider provenance and digests,
plus normalized observations. Changed content creates new evidence without
overwriting prior observations or approved artifacts. Error messages omit tokens
and raw provider responses. Rate-limit hints cross the boundary as normalized
retry times; the daemon owns retry scheduling. Neither source adapter contains
workflow execution or external ticket mutation authority.

## Ticket admission and execution

In the backlog, **Approve this version for work** records approval of the exact
retained observation and creates or reuses its local execution record. Repeated
admission of the same project and source ticket reuses that work identity.
Opening or refreshing a ticket does not admit it. **Open work and prepare a run**
opens the work context, where route assessment remains a separate action.

```text
darkstar ticket-execution admit <project-id> --revision <binding-revision> --observation <observation-id> --idempotency-key <key>
darkstar ticket-execution show <work-id>
darkstar ticket-execution list --project <project-id>
darkstar ticket-execution refresh <work-id>
darkstar ticket-execution approve <work-id> --observation <observation-id> --idempotency-key <key>
darkstar run prepare <work-id> --source-observation <approved-observation-id>
```

CLI help lists the optional workflow and route overrides. Source-backed
preparation requires the explicitly approved observation
ID. Each run retains its own source reference, binding and connection pins,
approved content, original evidence and approval time. Run details expose the
ticket version actually used. Retries restore that retained input. Later runs
can use a newly approved version of the same ticket without duplicating intake
or rewriting earlier runs.

The board derives current tracker content separately from local execution
activity. The work context shows tracker status, local activity, the latest
observed run outcome and external acceptance independently. Run completion is
not evidence of external acceptance and never closes the source ticket.

The default source-change policy requests human assessment. Changed content is
shown beside the approved version. Source cancellation, archival, deletion or
loss of access retains ticket and run history and requires attention before
new preparation; it does not claim that an existing run has been cancelled.
Cancellation remains a daemon command. An uncertain provider cancellation keeps
execution ownership until reconciliation establishes a settled outcome.

A project source switch affects future intake. Existing work remains attached
to its original source; **Check original source** reads that pinned lineage.
Changing an existing work record's source is a separate revision-checked rebind:

```text
darkstar ticket-execution rebind <work-id> --lineage-revision <current-lineage-revision> --revision <selected-binding-revision> --observation <observation-id> --idempotency-key <key>
```

Rebinding requires settled runs and old-source operations and retains all prior
lineage and run snapshots. It neither migrates nor deletes business tickets.
Native work keeps its original identity and history; unresolved legacy imports
remain explicitly unresolved rather than being assigned a guessed external
identity.

New native work uses the same approval boundary. Open its work context, select
**Check original source**, then **Approve current version for next run** before
preparing a route. Native work present before the ticket/execution migration
retains its historical preparation path until explicitly admitted. The migration
records that compatibility boundary; newly created work cannot inherit it.
