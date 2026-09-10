// Keep generation and freshness checks identical for every embedded package.
await import('./build.mjs');
await import('../../plugins/builtin-nodes/build.mjs');
await import('../../plugins/provider-codex/build.mjs');
