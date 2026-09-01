# Workflow loading, installation, and run snapshots

Workflow authoring files are discovered from three configured filesystem scopes:

1. shipped defaults;
2. the user `workflows` directory beneath the platform configuration root; and
3. the project `.darkstar/workflows` directory.

Project definitions take precedence over user definitions, which take precedence
over shipped defaults for the same workflow name and semantic version. Defining
the same name/version more than once in one scope is an error. Missing configured
directories are empty scopes; unreadable directories or malformed documents fail
the load.

JSON, YAML, and YML authoring files are accepted. Loading rejects duplicate keys,
multiple YAML documents, aliases, merge keys, custom/non-JSON scalar types,
unknown workflow fields, and documents larger than 1 MiB. Installation converts a
validated document to canonical JSON, sorts object keys at every depth, and uses
the lowercase SHA-256 digest of those bytes as its content identity.

Before canonicalization or installation, static validation resolves every node,
transition, binding, validator, and predicate reference; checks binding and
comparison types; proves entry reachability and the default terminal boundary;
verifies join declarations and directly provable join impossibilities; and removes
bounded transitions before checking that the remaining graph is acyclic. Validation
returns all findings in stable JSON Pointer, code, and detail order. Callers with a
frozen capability view can additionally verify each explicitly required reasoning
skill and tool; skill and tool namespaces are separate, so one cannot silently
satisfy the other.

At execution time, predicates receive one immutable snapshot containing candidate
outputs, bound node inputs, and frozen run inputs. The evaluator supports only the
typed `v1alpha1` expression tree; it distinguishes missing values from present JSON
`null`, performs exact numeric comparisons without floating-point coercion, and
evaluates logical arguments in authored order without short-circuiting error checks.
The first failing operand returns `RUN_PREDICATE_INVALID` at its stable workflow
JSON Pointer.

Installed workflow versions are keyed by `(metadata.name, metadata.version)` and
cannot be updated or deleted. Reinstalling the same canonical bytes is idempotent;
different bytes at the same key fail with a version conflict and require a new
semantic version.

After a run aggregate is created, snapshot creation copies the exact installed
workflow bytes and the fully resolved non-secret configuration into an immutable
run record. The configuration snapshot includes source attribution for every
resolved leaf. Repeating the exact snapshot is idempotent; trying to change either
the workflow or configuration for that run fails instead of mutating history.

## Explicit route creation

Route creation accepts one optional `from` entry and an optional `until` terminal
set. Omitted values use `routeDefaults`; explicit values must name eligible nodes.
Traversal follows every enabled possible outcome and stops at the selected terminal
boundary. A route is rejected if a selected terminal is unreachable, any included
branch cannot reach a selected terminal, or project policy marks an excluded node
as required. Disabled transitions remain outside the route.

The frozen result separates executable nodes from `excludedNodes`, which prevents a
skipped node from acquiring an execution state. Exclusions record whether a node is
before the selected entry, past the terminal boundary, or disconnected from the
selected range. Required bindings may be satisfied by run inputs, accepted prior
outputs, or an included predecessor. Any remaining gaps are returned as ordered
`RUN_INPUT_REQUIRED` requirements so the run can wait for input without fabricating
a value or executing an excluded predecessor.

## Bounded edges and sub-workflow frames

Runtime control-flow state is scoped to an execution frame. Firing a transition
creates an idempotent token keyed by source visit, transition, and join epoch. A
bounded token owns its post-consumption traversal ordinal and declared limit;
replaying the same token does not consume the budget again, while a new firing
after the limit fails visibly with `RUN_LOOP_LIMIT_EXHAUSTED`. Serializable frame
snapshots reconstruct traversal counts from the authoritative tokens and reject
duplicate, conflicting, or discontinuous evidence before replay.

A sub-workflow call creates a child frame pinned to the declared workflow name,
version, and digest. The call must use its declared entry and terminal set, copies
mapped inputs by value, rejects recursive ancestry, and maps only declared,
type-compatible child outputs to the parent candidate. Parent, child, and sibling
frames never share transition tokens or traversal counters, so reusable story and
repair workflows have isolated iteration state.

## Route-patch proposal and authorization

The production route-patch core accepts the existing `RoutePatch` v1alpha1 wire
contract as a closed operation union. A patch may enable or disable only an
authored transition, or replace the future terminal set; it cannot author nodes,
predicates, targets, joins, or traversal budgets. Transition overrides are stored
as a small projection over the immutable workflow and the complete candidate route
is rederived through the same validator used for initial route freezing. Exclusive
nodes cannot gain multiple unconditional outcomes.

Proposal and application are separate types. A proposal records deterministic
added/removed nodes and transitions, the old/new terminal boundaries, a validation
digest, and a canonical authorization-scope digest bound to the run, expected route
revision, reason, operations, and frozen policy. Only `AuthorizeRoutePatch` can
produce the value accepted by application. Automatic authorization requires an
exact matching system-policy scope; a named approval, policy-required change, or
terminal expansion requires an approved `workflow_control` request with the same
run, scope, and policy plus an attributable user or external actor. A provider
permission approval is therefore not type-compatible authority for a route change.

Application performs a second validation and one compare-and-swap revision. It
rejects stale topology or validation evidence, an affected running attempt, a
disabled transition that already emitted a token, and any candidate that would
drop an activated, successful, or terminal-reached node from the route projection.
On success it returns the new route revision and one audit payload containing the
old/new revisions, exact operations, impact, rationale, actor, optional approval
ID, scope digest, and validation digest; persistence writes both atomically without
changing the immutable workflow digest or historical visit/token evidence.
