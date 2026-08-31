# DARKSTAR Windows platform contract

> [Documentation index](../../README.md)

**Status:** Proposed normative contract for DS-009  
**Supported baseline:** 64-bit Windows 11 and Windows Server 2022 or newer  
**Evidence host:** Windows build 26200, PowerShell 7.6.4

## 1. Decision

DARKSTAR runs one per-user daemon in the foreground or under a user-scoped
launcher. A Windows adapter owns paths, single-instance locking, endpoint state,
process identity, executable resolution, graceful signaling, Job Objects, and
optional ConPTY. Core code depends on `PlatformStrategy`; it contains no Windows
conditionals and never discovers or kills processes by name.

Every spawned provider/validator/command process is assigned to a dedicated Job
Object configured with `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE` before it may spawn
children. Descendants inherit the job. Normal shutdown first uses the transport's
graceful operation, waits a bounded interval, then terminates the owned job. If
the daemon crashes or its last job handle closes, Windows terminates the owned
tree. Processes intentionally detached from the job are unsupported in the MVP.

## 2. PlatformStrategy boundary

The Windows adapter implements these provider-neutral operations:

| Operation | Contract |
|---|---|
| `resolvePaths()` | Return canonical config/data/cache/log/runtime paths rooted below known Windows folders. |
| `acquireDaemonLock()` | Take an exclusive, held-open lock and return existing-owner diagnostics on sharing violation. |
| `publishEndpoint()` | Atomically replace `{schemaVersion,pid,processStartTime,port,token,createdAt}` after binding loopback. |
| `inspectProcess(identity)` | Match PID plus process creation time and executable identity; PID alone is never ownership proof. |
| `startOwnedProcess(request)` | Resolve an exact executable, create a kill-on-close job, assign the suspended process, then resume it. |
| `requestGracefulStop(owner)` | Send a protocol cancel/shutdown or service stop notification; never synthesize workflow success. |
| `terminateOwnedTree(owner)` | Close/terminate only the recorded job; report exit observations. |
| `openTerminal(request)` | Use ordinary redirected stdio unless an interactive TTY is explicitly required; then use ConPTY. |
| `atomicReplace(path, bytes)` | Flush a same-directory temporary file and replace/rename; preserve prior state on sharing failure. |
| `resolveExecutable(spec)` | Return canonical path, file identity, version, and source; execute that same path. |

The implementation should use suspended `CreateProcessW` plus
`AssignProcessToJobObject` to eliminate the start/assign race. The probe uses a
gate before child creation to demonstrate inheritance with scriptable tooling;
production code must not depend on that gate.

## 3. Paths and files

Per-user defaults are:

| Data | Default |
|---|---|
| configuration | `%LOCALAPPDATA%\DARKSTAR\config` |
| database/artifacts | `%LOCALAPPDATA%\DARKSTAR\data` |
| cache | `%LOCALAPPDATA%\DARKSTAR\cache` |
| logs | `%LOCALAPPDATA%\DARKSTAR\logs` |
| runtime lock/state | `%LOCALAPPDATA%\DARKSTAR\runtime` |

All paths are absolute, canonical, Unicode strings. The manifest enables
long-path-aware behavior and the adapter uses wide-character APIs; it does not
prepend `\\?\` to untrusted input as a substitute for canonicalization. Runtime
files are user-only by inherited ACL plus an explicit post-create ACL check. A
machine service and shared multi-user daemon are outside MVP scope.

`daemon.lock` is held with no sharing for daemon lifetime. `endpoint.json`
contains a cryptographically random 256-bit bearer token, loopback port, PID,
process creation time, schema version, and creation time. The token is never
written to logs, command lines, events, or crash diagnostics. The listener binds
`127.0.0.1` and `::1` where available; clients validate identity and authenticate
every request. Stale endpoint state is ignored and replaced only after the lock
is acquired and the recorded process identity fails to match.

Durable writes use a temporary file in the destination directory, restrictive
ACL, `FlushFileBuffers`, and atomic rename/replace. Sharing violations caused by
antivirus/indexers use bounded jittered retry. Exhaustion preserves the old file,
records a diagnostic, and fails the operation; it never truncates in place.

## 4. Startup, discovery, and shutdown

Startup order is deterministic:

1. resolve and validate directories and their ACLs;
2. acquire `daemon.lock`;
3. read endpoint state and classify it as absent, current, or stale using PID,
   creation time, and executable identity;
4. reconcile stale attempt/job/provider state per DS-003;
5. bind loopback on an OS-assigned or configured port;
6. create a fresh 256-bit token and atomically publish endpoint state; and
7. accept authenticated requests.

Ctrl-C, console close, service stop, app shutdown, and explicit API stop converge
on one idempotent shutdown coordinator. It stops accepting work, durably records
shutdown intent, requests graceful provider cancellation, waits policy grace,
closes owned jobs, checkpoints SQLite, removes endpoint state, and releases the
lock. A second signal shortens grace but cannot broaden the kill target.

After an unclean exit, Windows normally closes job handles and kills descendants.
Restart still treats every recorded active attempt as uncertain until process,
provider, and workspace reconciliation proves its outcome. A surviving process
with no matching identity is unowned and is never terminated automatically.

## 5. ConPTY and executable discovery

App Server and bounded `codex exec --json` use redirected binary-safe stdio and do
not require ConPTY. ConPTY is requested only by a capability that explicitly
requires terminal semantics. Its absence degrades that capability, not daemon
startup. Terminal resize, encoding, Ctrl-C, output bounds, and handle closure are
part of the later terminal-adapter conformance suite.

Executable resolution accepts an explicit configured path first, then a bounded
allowlisted search. It records canonical path, file ID when available, SHA-256 or
package identity, signer status where policy requires it, and exact version. A
`.cmd`/`.bat` shim is launched through an explicit command interpreter; shell
association is never inferred. Health and execution use the same resolved path.

## 6. Supported behavior matrix

| Behavior | Windows 11 x64 | Server 2022+ x64 | Failure/degradation |
|---|---|---|---|
| per-user known-folder paths | Required, probed | Required | startup fails on unsafe ACL/path |
| exclusive daemon lock | Required, probed | Required | existing daemon or stale-state reconciliation |
| atomic state replacement | Required, probed | Required | bounded retry; old state preserved |
| loopback port + random token | Required, probed | Required | no endpoint published before both exist |
| PID/start-time stale detection | Required, probed | Required | ambiguous identity fails closed |
| kill-on-close Job Object tree | Required, probed | Required | daemon cannot start work if unavailable |
| graceful stop then tree kill | Required, probed | Required | uncertain attempt enters reconciliation |
| long Unicode paths | Required, probed | Required | actionable path error, no fallback truncation |
| exact executable discovery | Required, probed | Required | capability unavailable |
| redirected stdio | Required, Codex probe | Required | provider unavailable |
| ConPTY | Optional | Optional with Desktop Experience | interactive capability disabled |
| antivirus sharing interference | Required retry semantics, probed synthetically | Required | preserve old file and diagnose |

“Probed” means the executable probe ran on the evidence host. Server support is a
contract baseline pending the same probe in CI; it is not claimed as live evidence
from this workstation.

## 7. Probe and acceptance

[`Invoke-WindowsProcessProbe.ps1`](../../../probes/windows-process-control/Invoke-WindowsProcessProbe.ps1)
checks known-folder roots, exclusive locks, endpoint/token state, atomic replace
and sharing interference, process identity, Job Object descendant termination,
long paths, and exact executable discovery. Its companion test fails unless every
required scenario passes.

Production acceptance additionally runs the probe on each supported Windows CI
image and injects crashes at the DS-003 boundaries. The implementation is not
accepted if parent termination leaves an owned descendant running, stale state is
treated as current, or core code needs OS conditionals.
