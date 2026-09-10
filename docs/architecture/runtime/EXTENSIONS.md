# Extension registration and execution

Status: implemented TypeScript node, Codex provider, resource/tool process hosts,
and work-item workspace services. Third-party installation and TypeScript
transports for the other extension families remain separate work.

DARKSTAR registers extensions in daemon composition. There are no global init
hooks, workflow-supplied executables, or plugin callbacks for state transitions.
The existing provider, delivery, artifact storage, repository, and platform ports
remain application-owned contracts.

## Identity and catalogs

`ports/extension.Ref` names an implementation with a namespaced ID, exact semantic
version, and SHA-256 implementation digest. A descriptor adds protocol compatibility,
configuration schema, display name, and required capabilities. Descriptors are data;
they cannot install code or grant permissions.

`core/extensions.Catalog[T]` is immutable after construction and typed by capability
family. Registration rejects duplicate ID/version pairs, incompatible protocols,
invalid identities, nil implementations, and capabilities the host has not granted.
Resolution requires the exact digest and never falls back to another version.
The host supplies the verified implementation digest; this registry is not itself
a package signature or binary verification mechanism.

Provider factories use `core/runexecution.ProviderCatalog` to preserve the existing
normalized lifecycle and provider name on attempts. Built-in providers retain
their legacy identities. Versioned provider registrations additionally expose exact
pins; new runs persist their selected provider's pins in their execution context.
The SQLite store makes those pins immutable and includes them in the context
integrity digest. Recovery rejects unavailable or replaced pinned providers.

## Custom nodes and validators

`v1alpha3` adds an `extension` node. Its executor contains `ref` and object-valued
`configuration`, alongside the ordinary declared inputs, outputs, checkpoints,
and transition contracts. The workflow digest therefore pins custom node behavior.
Reusable reasoning nodes continue to use agent profiles and instructions; those
do not require custom executable implementations.

`core/extensions.NewNodeCatalog` validates configuration through the value-schema
port before exposing an implementation. `ports/nodeextension.Executor` receives
only copied declared input values and configuration. Its candidate output map goes
through ordinary daemon output validation and attempt completion. It receives no
workflow graph, run inputs, storage handle, approval operation, or transition API.

An extension validator is declared as `{ "extension": { "ref": ..., "configuration": ... } }`.
The daemon validates output contracts first, then calls declared extension validators
in order. Each receives independent copies of inputs and candidate outputs. A failed
check stops completion. Accepted evidence records the exact validator and candidate
digest in the durable result event. Artifact revisions and final reviewed candidates
also pass this boundary; human approval cannot substitute for validation.

## Other capability families

- `artifactderive.ProcessorCatalog` centralizes discovery and derivation selection.
  Configured order is explicit precedence; quarantine stops selection. The pinned
  constructor supports exact processor selection without fallback. Processor
  digests are persisted with representations and included in their stable identity.
  Existing artifact safety limits, immutable originals, and durable sinks remain in
  control of the daemon.
- Source catalogs expose the read-only `ports/worksource.Source` contract. Import
  and refresh return a versioned item, an unchanged revision, or a missing source.
  The host rejects observations for a different reference. Source adapters cannot
  create runs or publish updates.
- Delivery catalogs resolve the existing `ports/delivery.Connector`. Observation,
  branch publication, and change-request operations remain narrow interfaces.
  Selection does not authorize a side effect; daemon approval, operation IDs, and
  reconciliation still apply.
- Artifact stores and platform strategies retain their existing narrow ports.
  They do not need new lifecycle hooks to participate in future package loading.

## Authoring and presentation

The authoring catalog can expose host-registered node descriptors. The extension
editor matches the entire reference before using its configuration schema. Simple
schemas produce controlled fields; other configurations retain a JSON editor that
preserves nested and unknown values. Missing specialized UI never erases config.

Representation renderers are registered as host components by media type. Duplicate
renderers fail at construction; absent renderers use escaped text. Review wrappers
continue to own disclosure policy, exact revision binding, annotations, revisions,
diffs, and human decisions. Artifact content cannot register a renderer.

## TypeScript tools and resource contributions

`packages/plugin-sdk` implements the `darkstar.plugin/v1` JSON-lines process
protocol. The Go `adapters/plugin/process` host supports resource, tool, and node
contributions. Providers use the persistent `adapters/provider/typescript` bridge
with the SDK's provider protocol. Processor, storage, and source families still
use their Go ports.

One package may contribute nodes, providers, tools, resource types, processors,
and storage adapters. Each contribution uses its family's contract and the
package's exact implementation identity. Built-ins must use the same public
contracts as installed packages. A package is a deployment unit, not a new owner
of workflow execution authority.

A tool contribution declares an identity within its pinned package, description, input and result
schemas, required capabilities, and invocation handler. The host resolves and pins
the contribution, binds configuration and permitted resource handles, validates
arguments and results, and records invocation evidence. Mutation calls carry a
host-scoped operation identity for retry reconciliation. A provider connector
translates these definitions and call/results to its native protocol; it does not
implement the tool's business behavior. Unsupported schemas or capabilities must
fail explicitly rather than silently weaken validation or grants.

Resource contributions are separate from tools. An open-items resource defines
its entry schema and add/resolve/defer operations; a decision-log resource defines
record/supersede operations. Each may contribute tools, node input/output schemas,
and presentation descriptors. This allows a custom issue register or research
notebook to expose the same integrations without adding resource-kind switches to
provider adapters. Optional UI uses the separate presentation boundary; server
plugin code never executes directly in the dashboard.

The daemon remains responsible for resource ownership, durable operation history,
idempotency, and revision concurrency. Plugins receive handles only for resources
bound to their invocation, never a database path. A decision-log entry records a
decision; it does not grant a human approval. `submit_output` may participate in
the common tool catalog, but its implementation delegates to the host's scoped
output submission API. Plugins cannot replace output validation or advance runs.

`workflowtools.Session` dispatches registered `ports/tool.Tool` objects. The
TypeScript built-in resource package supplies open-item and decision-log operation
definitions; a scoped Go callback retains SQLite persistence and host validation.
Existing tool names, resource bindings, operation keys, and raw history are
preserved. Journals remain run-scoped in the private workflow-tools database.
Resource bindings cannot alias host output-evidence namespaces.

New plugin mutations supply the revision returned by a journal read. The daemon
compares it inside the same transaction as the append, after checking for an
identical idempotent replay. Stale edits fail explicitly; replay returns the
original receipt and its revision. Legacy runs without a plugin pin retain the
Go compatibility path and its original optional-revision behavior.

The bundled JavaScript includes the SDK and resource code in one generated file,
embedded in the Go binary. Normal daemon composition selects this exact digest;
new run execution contexts pin it, and recovery rejects a changed implementation.
Node.js is required to execute these tools. If unavailable, daemon inspection
remains available but plugin-backed attempts fail with an explicit error.

## TypeScript nodes and providers

`plugins/builtin-nodes` implements reasoning, implementation, point-execution,
command, workspace-prepare, and workspace-validate behavior. The node bridge
translates daemon-selected requests into task descriptions, output schemas, or
deterministic tool calls. New runs pin `darkstar/builtin-nodes`; legacy runs without
that pin retain the Go implementation. The daemon still checks executor permissions,
declared input scope, repository ownership, configured commands, and required host
operation receipts before accepting plugin results. Git operations, writer leases,
implementation baselines, scheduling, gates, and advancement remain host-owned.

`plugins/provider-codex` implements Codex App Server translation in TypeScript:
health and capabilities, start/resume, normalized events, dynamic tools, permission
responses, cancellation, and results. The persistent bridge binds host callbacks to
an attempt and its granted tool names, records evidence, and validates candidate
outputs. Workflow execution retains the durable provider name `codex`; its exact
provider pin selects the TypeScript implementation for new runs. Runs without a
provider pin use the legacy Go adapter. Workflow authoring chat remains the separate
Go capability and does not acquire execution authority through this migration.

Each built-in package has its own generated bundle and digest. SDK checks typecheck
the source and verify generated bundles. Node.js is required for plugin-backed
execution. This is trusted local code, not an installer or an OS sandbox. On Windows,
the provider bridge owns a kill-on-close job containing Node and its Codex child.
Host callbacks expose no approval or workflow-transition API.

## Work-item workspace

`workmanagement.NewWithWorkspaces` provisions storage before create/import/replay
commands succeed; daemon startup reconciles existing work items. The folder
adapter uses `<daemon-data>/workspaces`, with hashed work-item and package identities
and distinct attempt directories. The shared port exposes no backing paths.

Every work item owns a durable logical workspace, independent of whether it runs
coding tasks. Its identity derives from the work item; a host-owned location
resolver determines its physical backing. Workflows and plugin configuration do
not persist machine-specific absolute workspace paths. Provisioning is idempotent
and reconciled after creation or restart, including existing work items, before
any consumer receives a handle.

The logical workspace provides these distinct areas:

| Area | Ownership and lifetime |
|---|---|
| Plugin state | Private to a package identity within the work item; survives runs; schema versions and explicit migration govern upgrades. |
| Attempt scratch and staged outputs | Private to an invocation; retries have separate areas; eligible for cleanup after durable completion. |
| Artifact references | Exact immutable artifact revisions associated with the work item; bytes live behind the selected artifact-store adapter. |
| Repository checkout | Optional existing managed Git worktree; subject to its existing writer lease and delivery lifecycle. |

Plugins currently write through scoped workspace APIs; private mounts are not
granted by this implementation.
The host binds the work item and plugin identity rather than accepting arbitrary
owners from plugin arguments. It enforces relative-path containment, symlink and
Windows junction handling, quotas, and cancellation. A separate process alone is
not filesystem isolation. Shared durable resource mutations use revision-checked
host APIs. File reads return SHA-256 digests. Writes create exclusively unless an
expected digest requests a checked atomic replacement; stale replacements fail.
The folder adapter serializes these checks across its handles and applies per-file
and per-area quotas. No automatic historical-data cleanup is introduced.

Writing a file stages mutable content. Publishing an artifact asks the daemon to
snapshot, hash, store, and register an immutable revision with producer provenance.
Subsequent workspace edits cannot alter that revision. The existing storage connector owns
physical layout and opaque locators; the daemon owns metadata, bindings, retention,
and review. A folder-backed store can be the default and another plugin can provide
another backend without teaching node or tool implementations backend paths.

Workspace access is granted per invocation, not implicitly to every plugin in the
installation. Work-item deletion follows the existing retention contract for runs,
transcripts, revisions, and blobs; deleting scratch or plugin state cannot erase
committed evidence. Artifact bytes remain retained while historical references exist.

## Migration progress

1. Implemented: versioned tool/resource protocol, TypeScript SDK, and Go process
   host with explicit grants and scoped callbacks.
2. Implemented: work-item provisioning, file operations, staging, and publication
   through the existing immutable artifact service and work bindings.
3. Implemented: open items and decision logs as built-in TypeScript contributions,
   journal concurrency checks, and real Node/Go behavioral tests.
4. Implemented: built-in node/task behavior and the Codex execution connector on
   the SDK, with exact run pins and real process fixture tests. Another provider
   can implement the same normalized provider and tool contracts.
5. Remaining: TypeScript transports for processors, storage, and other integration
   adapters. Third-party installation is a separate layer.

These shapes separate deployment, resource semantics, tool transport, mutable
workspace data, and immutable evidence. Host-bound handles avoid duplicated owner
fields and prevent callers from selecting another work item's storage. Explicit
resource revisions and artifact versions prevent concurrent plugin writes from
silently changing an input or an already reviewed output.

## Packaging boundary

These interfaces support built-in or explicitly composed implementations today.
They do not discover, install, sandbox, or hot-load third-party packages. The tool
host verifies the entrypoint digest, protocol, schemas, and callback grants. It
bounds messages, retained stderr, callback count, and process lifetime. It strips
inherited environment variables except Windows SystemRoot and terminates the
immediate child on cancellation. This is a trusted-code process boundary, not
filesystem isolation or process-tree containment. Arbitrary package dependencies
and OS permission enforcement require a separate installation/sandbox layer.
Plugin callbacks never expose daemon orchestration authority.

Workflow examples are retained as test/reference fixtures. Normal builds and packages
no longer auto-install them. Project and user workflow directories remain supported;
existing archived versions remain available to historical runs.
