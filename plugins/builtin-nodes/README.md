# Built-in node plugin

The `darkstar/builtin-nodes` TypeScript package implements reasoning,
implementation, point-execution, command, workspace-prepare, and
workspace-validate contributions through the public node invocation contract.

Each node lives in its own module under `src/`: `reasoning.ts`,
`implementation.ts`, `point-execution.ts`, `command.ts`, `workspace-prepare.ts`,
and `workspace-validate.ts`. `index.ts` only registers the contributions;
`shared.ts` contains the invocation contract, descriptor helper, and shared schemas.

Agent nodes construct instructions, requirements, and output schema refinements.
Deterministic nodes translate their configuration and bound inputs into scoped
host calls and produce candidate output values. They receive no workflow graph,
run input collection, approval API, or scheduler state.

The daemon authorizes each host operation against the original node configuration.
It retains Git ownership/preparation, implementation baselines, mandatory checks,
output validation, approvals, retries, and state transitions. Gates also remain
daemon-owned. Approval, routing, and subworkflow nodes retain their existing
explicit unsupported execution behavior.

`workspace.prepare` accepts only the connected repository and declared checkout
plan. `workspace.resolve` accepts only the connected workspace. `process.run`
accepts only the next declared validation command with its host-enforced timeout;
the historical command-node allowlist is unchanged. Successful plugin narration
cannot substitute for completed mandatory host operations.

Generate the embedded single-file JavaScript bundle with
`node plugins/builtin-nodes/build.mjs`. Use `--check` to verify freshness. The bundle
includes the SDK, and the daemon pins and verifies its SHA-256 digest.

Run real-process integration tests with
`go -C runtime test ./src/adapters/nodeextension/typescript` and typecheck using
`node packages/plugin-sdk/node_modules/typescript/bin/tsc -p plugins/builtin-nodes/tsconfig.json`.
