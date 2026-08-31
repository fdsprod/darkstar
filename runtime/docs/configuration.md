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
