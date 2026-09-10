# Project toolchains

The repository's `.go-version`, `.node-version`, and `.npm-version` files are
the default install versions and minimum supported versions. Checks accept stable
versions greater than or equal to these values, including newer major versions.
CI still installs the stated versions for reproducibility. Newer versions must
still pass the normal build and tests. The host's PATH is not a reliable project environment:
desktop apps can inherit old versions, and roaming npm shims can point at a
different or removed installation.

Run `./scripts/Setup-Toolchain.ps1` once. It installs portable tools beneath
`.darkstar/toolchains`, verifies downloaded archives against the official
[Go release checksums](https://go.dev/dl/) and
[Node release checksums](https://nodejs.org/dist/v22.12.0/SHASUMS256.txt), checks
all four executable entrypoints, and atomically saves their absolute roots in
`.darkstar/toolchains.json`. Existing verified installations can be registered
with `-GoRoot <directory-containing-bin> -NodeRoot <directory-containing-node.exe>`.
Use `-Reinstall` to replace an incomplete portable installation from verified
archives. The binding is local state; it must not be committed.

`Bootstrap.ps1` performs initial setup when needed. Build, test, lint, and format
scripts load the saved binding before asserting minimum versions. npm and npx
come from that same Node distribution. Worktrees inherit the binding from their
daemon or discover the owning checkout through Git's common directory.

Use `./scripts/Start-Dev.ps1` to build and restart the development daemon.
Ordinary daemon starts also load and verify the project's binding. The daemon
sets its own tool PATH and supplies the verified environment to provider
processes and workspace checks. Machine/user PATH settings remain unchanged.
Raising a minimum above the installed version requires rerunning setup and
restarting the daemon; missing, older, or broken tools fail admission before an execution agent is launched. Projects
without version pins keep their existing environment.

Go build/module caches and npm's cache live beneath the project's `.darkstar/cache`
instead of shared profile directories, avoiding stale permissions from other
processes. Worktree scripts use caches inside their own writable checkout.

## Worktree access and recovery

Editable worktrees live in `<project>/.darkstar/worktrees`, outside the daemon's
private database directory. A retry of a legacy worktree verifies its repository,
run ownership record, branch, and HEAD before using `git worktree move` to relocate
it. Edits, the index, branch, commits, and old transcript snapshots are preserved.
The current workspace record supplies the relocated path to the new attempt.
A move completed before a daemon interruption is reconciled on retry. Conflicting
destinations and changed branches or HEADs stop recovery instead of overwriting
files. No extra read or write access to daemon databases is granted to agents.
