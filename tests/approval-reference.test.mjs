import test from "node:test";
import assert from "node:assert/strict";
import { fileURLToPath } from "node:url";
import { dirname, resolve } from "node:path";
import { loadCatalog, runCatalog } from "../scripts/approval-reference.mjs";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const catalog = loadCatalog(resolve(root, "examples/approvals/approval-scenarios.json"));
const results = runCatalog(catalog);

test("approval catalog has required coverage", () => {
  const coverage = new Set(catalog.cases.flatMap((entry) => entry.covers));
  for (const requirement of [
    "workflow_checkpoint",
    "workflow_control",
    "provider_permission",
    "external_delivery",
    "cross_class_negative",
    "actor_negative",
    "scope_negative",
    "policy_negative",
    "expiry",
    "offline",
    "idempotent_replay",
    "idempotency_conflict",
    "session_grant",
    "restart_reconciliation",
  ]) assert.ok(coverage.has(requirement), `missing coverage: ${requirement}`);
});

test("approval cases produce their documented deterministic outcome", () => {
  const failures = results.filter((entry) => !entry.pass);
  assert.deepEqual(failures, []);
});

test("every approval class has a successful class-specific decision", () => {
  const successful = new Set(
    catalog.cases
      .filter((entry, index) => entry.kind === "decision" && results[index].actual.code === "OK")
      .map((entry) => entry.request.class),
  );
  assert.deepEqual([...successful].sort(), [
    "external_delivery",
    "provider_permission",
    "workflow_checkpoint",
    "workflow_control",
  ]);
});

test("no negative case emits an authorization or commit effect", () => {
  catalog.cases.forEach((entry, index) => {
    if (!entry.covers.some((item) => item.endsWith("_negative") || item === "expiry" || item === "offline")) return;
    assert.equal(results[index].actual.effect ?? "none", "none", entry.id);
  });
});

test("idempotent replay returns the committed state without a second effect", () => {
  catalog.cases.forEach((entry, index) => {
    if (!entry.covers.includes("idempotent_replay")) return;
    assert.equal(results[index].actual.replayed, true, entry.id);
    assert.equal(results[index].actual.effect, "none", entry.id);
  });
});
