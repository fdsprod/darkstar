# Runtime configuration

DARKSTAR resolves configuration in this order, from highest to lowest:

1. command-line overrides;
2. work-item or run overrides;
3. project configuration;
4. user configuration; and
5. shipped defaults.

Mapping values merge recursively. Scalars, arrays, empty mappings, and `null`
replace the lower-precedence value at the same path. Resolution retains the
winning scope and source reference for every leaf using JSON Pointer paths.
Duplicate layers at one scope are rejected instead of relying on call order.

On Windows, the platform adapter obtains the current user's Local AppData Known
Folder and returns these roots:

| Purpose | Location |
|---|---|
| user configuration | `%LOCALAPPDATA%\DARKSTAR\config\config.yaml` |
| user secrets | `%LOCALAPPDATA%\DARKSTAR\config\secrets.yaml` |
| durable data | `%LOCALAPPDATA%\DARKSTAR\data` |
| cache | `%LOCALAPPDATA%\DARKSTAR\cache` |
| logs | `%LOCALAPPDATA%\DARKSTAR\logs` |
| runtime state | `%LOCALAPPDATA%\DARKSTAR\runtime` |

Project configuration is `.darkstar\config.yaml` below the discovered project
root. The normal configuration loader never reads `secrets.yaml` into the
effective configuration tree, and project configuration must not contain
secrets. Configuration files are single-document YAML mappings and are limited
to 1 MiB each.

## Typed mutation

The supported-settings catalog is the authority for editable keys. Each entry
declares its value type, constraints, shipped default, sensitivity, allowed
`user` or `project` scopes, restart impact, and available actions. The initial
catalog supports Codex executable selection, explicit Codex action availability,
and a reference to the separately stored Codex API-key secret. Unsupported keys
cannot be written through the API.

Every scoped state has a SHA-256 revision over the exact file bytes; an absent
file has its own stable sentinel revision. Preview validates a tagged value and
returns effective before/after values with their winning sources without writing.
Apply and restore use that revision as a compare-and-swap guard. Writes preserve
comments and unknown keys, save a bounded exact previous snapshot under the user
data directory, use a same-directory temporary file, and recover an interrupted
replacement on the next read. Project writes accept a registered project ID and
only target the daemon's verified startup project root—never a caller-provided
path.

Secret material uses the user-only `secrets.yaml` command. Ordinary catalog,
state, preview, apply, audit, event, log, and export shapes contain only a secret
reference or secret name. They never contain the secret value or secrets-file
path. Configuration changes emit sanitized accepted or rejected operation events;
successful commands also retain replayable idempotency evidence.
