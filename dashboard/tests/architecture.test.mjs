import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const read = (path) => readFile(new URL(path, import.meta.url), "utf8");

test("application entrypoint installs a safe top-level error boundary", async () => {
  const [entrypoint, boundary] = await Promise.all([
    read("../src/main.tsx"),
    read("../src/app/AppErrorBoundary.tsx"),
  ]);
  assert.match(entrypoint, /<AppErrorBoundary>/);
  assert.match(entrypoint, /import "\.\/api\/bootstrap"/);
  assert.match(boundary, /window\.location\.reload/);
  assert.doesNotMatch(boundary, /this\.state\.error\.message/);
  assert.doesNotMatch(boundary, /console\.error\([^\n]*error/);
});

test("router owns all top-level dashboard destinations", async () => {
  const source = await read("../src/app/router.tsx");
  for (const route of ["/board", "/work/:workId", "/checkpoints", "/agents", "/workflows", "/settings", "/artifacts/:artifactId"]) {
    assert.ok(source.includes(route), `missing route ${route}`);
  }
  assert.match(source, /popstate/);
  assert.match(source, /pushState/);
});

test("API client consumes in-memory auth and uses the versioned same-origin contract", async () => {
  const [client, bootstrap, generated] = await Promise.all([
    read("../src/api/client.ts"),
    read("../src/api/bootstrap.ts"),
    read("../src/api/schema.generated.ts"),
  ]);
  assert.match(client, /operationDefinitions/);
  assert.match(generated, /\/api\/v1\/health/);
  assert.match(client, /credentials: "same-origin"/);
  assert.match(client, /Idempotency-Key/);
  assert.match(client, /If-Match/);
  assert.match(bootstrap, /delete window\.__DARKSTAR_BOOTSTRAP__/);
  assert.doesNotMatch(bootstrap, /localStorage|sessionStorage/);
});

test("API type surface is reproducibly generated from OpenAPI", async () => {
  const [generator, generated] = await Promise.all([
    read("../scripts/generate-api-client.mjs"),
    read("../src/api/schema.generated.ts"),
  ]);
  assert.match(generator, /openapi-v1alpha1\.json/);
  assert.match(generator, /--check/);
  assert.match(generated, /Do not edit by hand/);
  assert.match(generated, /interface ApiOperations/);
});

test("shell has keyboard and screen-reader navigation affordances", async () => {
  const [shell, styles] = await Promise.all([
    read("../src/components/AppShell.tsx"),
    read("../src/styles.css"),
  ]);
  assert.match(shell, /event\.key\.toLowerCase\(\) === "k"/);
  assert.match(styles, /\.skip-link:focus/);
  assert.match(styles, /prefers-reduced-motion/);
});
