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

Installed workflow versions are keyed by `(metadata.name, metadata.version)` and
cannot be updated or deleted. Reinstalling the same canonical bytes is idempotent;
different bytes at the same key fail with a version conflict and require a new
semantic version.

After a run aggregate is created, snapshot creation copies the exact installed
workflow bytes and the fully resolved non-secret configuration into an immutable
run record. The configuration snapshot includes source attribution for every
resolved leaf. Repeating the exact snapshot is idempotent; trying to change either
the workflow or configuration for that run fails instead of mutating history.
