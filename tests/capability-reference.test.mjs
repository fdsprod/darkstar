import test from "node:test";
import assert from "node:assert/strict";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { evaluatePostInvocation, loadCatalog, resolveRequirements } from "../scripts/capability-reference.mjs";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const catalog = loadCatalog(resolve(root, "examples/capabilities/capability-scenarios.json"));

test("capability scenarios cover every provenance and failure boundary", () => {
  const coverage = new Set(catalog.cases.flatMap((entry) => entry.covers));
  for (const item of ["guaranteed","registered","inherited","skill","tool","version","namespace","policy_deny","fallback","optional","dependency","missing"])
    assert.ok(coverage.has(item), `missing coverage: ${item}`);
});

test("resolution is deterministic and matches the frozen contract", () => {
  for (const testCase of catalog.cases)
    assert.deepEqual(resolveRequirements(testCase, catalog.registry), testCase.expected, testCase.id);
});

test("registered capability wins over same-name inherited observation", () => {
  const testCase = catalog.cases.find((entry) => entry.id === "registered-precedes-inherited");
  assert.deepEqual(resolveRequirements(testCase, catalog.registry).selected, ["cap:mcp-docs-search"]);
});

test("inherited capability cannot satisfy a required input without opt-in", () => {
  const denied = catalog.cases.find((entry) => entry.id === "inherited-required-denied-by-default");
  const allowed = catalog.cases.find((entry) => entry.id === "inherited-explicit-opt-in");
  assert.equal(resolveRequirements(denied, catalog.registry).code, "CAPABILITY_INHERITED_NOT_ALLOWED");
  assert.equal(resolveRequirements(allowed, catalog.registry).degraded, true);
});

test("post-invocation fallback never duplicates an ambiguous side effect", () => {
  for (const entry of catalog.postInvocation) assert.equal(evaluatePostInvocation(entry), entry.expected, entry.id);
});

test("registry records never contain credential values", () => {
  for (const entry of catalog.registry) {
    const serialized = JSON.stringify(entry).toLowerCase();
    for (const forbidden of ["token","password","secret","authorization"]) assert.ok(!serialized.includes(forbidden), `${entry.id} contains ${forbidden}`);
  }
});
