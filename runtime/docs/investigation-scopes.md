# Repository investigation scopes

Investigation scopes preserve committed repository evidence independently of a
changing checkout. They implement the read-only part of the
[project/repository contract](../../docs/architecture/work/PROJECT_REPOSITORY_TRACKER_MODEL.md).
Preparing a scope does not start an agent or grant delivery authority.

Create a JSON selection file with explicit registered repository IDs and refs:

```json
[
  {"repositoryId": "repository_01K00000000000000000000001", "ref": "refs/heads/main"}
]
```

```text
darkstar project scope prepare <project-id> --repositories-file repositories.json --idempotency-key investigate-feature-001 --json
darkstar project scope show <scope-id> --json
```

Use `[]` for a project with no selected code context. Omitting the list, supplying
`null`, duplicate repositories, or more than 32 repositories is invalid. Each
selection requires a ref; neither the first membership nor the current directory
is selected implicitly. A membership with either role may supply read-only
evidence. Removed memberships cannot be selected for new scopes.

The daemon resolves each ref to an exact commit and freezes canonical repository
coordinates, membership revision, project revision, and effective configuration
with provenance. It durably records these values before exporting files. Replay
the same request and idempotency key to resume preparation of that scope. A new
key prepares new evidence; changing the request while reusing its key is a
conflict. Inspect the returned preparation status: a retained `blocked` scope
contains an actionable reason and is not ready for execution.

The initial content policy is `committed_only`: staged, unstaged, and untracked
working-tree changes are excluded. Export reads Git objects without checking out
files, creating worktrees, running checkout filters, or modifying the user's
index. The evidence manifest records file paths, modes, blob IDs and content
digests. Special entries that cannot be safely materialized are explicit
exclusions. Missing revisions, unsafe paths, and configured size limits fail
preparation rather than substituting another revision. Membership path scope
filters exported content; an explicit empty list selects no paths.

Snapshots live beneath the daemon data directory's `repository-snapshots` folder.
Publication is atomic and retained bytes are checked against the manifest before
use. A retry uses the recorded evidence, including when the original checkout is
unavailable. Corruption is reported; it cannot silently become fresh checkout
content. Snapshot roots contain no writable Git worktree or shared Git metadata.

Attempt bindings are daemon-internal records. Each binds one immutable scope to
an explicit subset and verifies retained evidence. Public API clients can prepare
and read scopes through `POST /api/v1/investigation-scopes` and
`GET /api/v1/investigation-scopes/{scopeId}`; they cannot create attempt bindings.

An explicit scoped provider attempt requires `scoped_read_filesystem` version
`v1`. The provider must enforce the allowed snapshot roots and deny writes; empty
roots grant no filesystem reads. Scope, configuration, and evidence digests are
part of this frozen requirement and survive resume. Capability validation is an
admission check, not a filesystem sandbox implementation.

The bundled Codex native, TypeScript, and exec adapters currently report this
capability as unavailable and reject scoped attempts before starting a provider
process. Their read-only sandbox and workspace-root metadata do not establish
read confinement. Legacy attempts retain their existing access contract. Scope
preparation and evidence inspection remain available independently of provider
support; unsupported access never falls back to broader permissions.
