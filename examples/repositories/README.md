# Golden repository fixtures

`golden-repositories.json` is the source of truth for deterministic Git test
repositories. The materializer creates each scenario under a new destination:

- `clean_checkout` is a clean user checkout on `main`;
- `dirty_checkout` contains one tracked edit and one untracked file;
- `branch_topology` contains distinct owned and colliding unowned branches; and
- `linked_worktree` contains a clean delivery branch in a linked worktree.

The fixture commit uses fixed content, identity, timestamp, branch, and Git
configuration. Absolute paths are normalized out of snapshots, so two
materializations can be compared byte-for-byte at the observation boundary.

Materialize an inspectable copy into a path that does not already exist:

```powershell
node scripts/repository-fixtures.mjs examples/repositories/golden-repositories.json out/golden-repositories
```

The command never cleans, resets, or reuses an existing destination. Tests use
temporary directories and remove only the directories they created.
