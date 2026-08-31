import test from "node:test";
import assert from "node:assert/strict";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { loadInventory, summarizeInventory, validateInventory } from "../scripts/threat-model-reference.mjs";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const inventory = loadInventory(resolve(root, "examples/security/threat-negative-tests.json"));

test("every high-risk boundary has an owner, backlog control, and negative test", () => {
  assert.deepEqual(validateInventory(inventory), []);
  for (const boundary of inventory.boundaries) {
    assert.match(boundary.owner, /^[a-z0-9-]+$/);
    assert.ok(boundary.controls.length > 0);
    assert.ok(boundary.negativeTests.length > 0);
  }
});

test("required DS-010 threat topics have fail-closed coverage", () => {
  const topics = new Set(inventory.tests.filter((entry) => entry.failClosed).map((entry) => entry.topic));
  for (const topic of ["local_api","prompt_injection","malicious_repo","path_traversal","unsafe_command","inherited_tool","secret_disclosure","git_damage","upload_parser","pr_side_effect"])
    assert.ok(topics.has(topic), `missing ${topic}`);
});

test("critical boundary tests assert prevented effects and evidence", () => {
  const critical = new Set(inventory.boundaries.filter((entry) => entry.risk === "critical").map((entry) => entry.id));
  for (const entry of inventory.tests.filter((testCase) => testCase.boundaries.some((id) => critical.has(id)))) {
    assert.equal(entry.failClosed, true, entry.id);
    assert.match(entry.expected, /no |deni(?:ed|al)|blocked|rejected|absent|untouched|unchanged|conflict|withheld|quarantined|unresolved|empty|cannot|reconcile_required|omitted/, `${entry.id}: expected result does not state prevention`);
  }
});

test("inventory remains substantial and review-gated", () => {
  const summary = summarizeInventory(inventory);
  assert.equal(inventory.reviewGate, "DS-200");
  assert.ok(summary.boundaries >= 12);
  assert.ok(summary.tests >= 30);
  assert.ok(summary.controls >= 20);
});
