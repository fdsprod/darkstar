import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

import {
  SIDE_EFFECTS,
  decide,
  runSuite,
  validateSuite,
} from "../scripts/recovery-reference.mjs";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const suitePath = join(root, "examples", "recovery", "recovery-scenarios.json");
const modelPath = join(root, "docs", "architecture", "recovery", "RECOVERY_MODEL.md");

function loadSuite() {
  return JSON.parse(readFileSync(suitePath, "utf8"));
}

test("DS-003 failure-injection suite covers every durable side effect and strategy observation", () => {
  const suite = loadSuite();
  assert.deepEqual(validateSuite(suite), []);
  const result = runSuite(suite);
  assert.equal(result.status, "passed");
  assert.equal(result.operationsCovered, Object.keys(SIDE_EFFECTS).length);
  assert.equal(result.scenarios, suite.scenarios.length);
  assert.ok(result.decisions.adopt > 0);
  assert.ok(result.decisions.resume > 0);
  assert.ok(result.decisions.retry > 0);
  assert.ok(result.decisions.interrupt > 0);
  assert.ok(result.decisions.reconcile_required > 0);
});

test("normative side-effect catalog stays aligned with the executable catalog", () => {
  const model = readFileSync(modelPath, "utf8");
  const catalog = model.match(/## 5\. Side-effect ownership and commit-point catalog([\s\S]*?)## 6\. Interruption matrix/);
  assert.ok(catalog, "normative side-effect catalog section is present");
  const documented = [...catalog[1].matchAll(/^\| `([a-z][a-z0-9_]*)` \|/gm)].map((match) => match[1]).sort();
  assert.deepEqual(documented, Object.keys(SIDE_EFFECTS).sort());
});

test("exact durable effects are adopted and uncertain or divergent effects never retry", () => {
  assert.equal(decide("commit_create", "exact_trailer_parent_tree"), "adopt");
  assert.equal(decide("push", "equal"), "adopt");
  assert.equal(decide("pr_create", "unique_owned_exact"), "adopt");
  assert.equal(decide("provider_process", "unknown_identity"), "reconcile_required");
  assert.equal(decide("provider_turn", "active_writer_unknown"), "reconcile_required");
  assert.equal(decide("push", "diverged"), "reconcile_required");
  assert.equal(decide("pr_create", "unowned_or_multiple"), "reconcile_required");
});

test("only proven absence or a safely behind authority permits retry", () => {
  assert.equal(decide("checkpoint_action", "absent"), "retry");
  assert.equal(decide("branch_create", "absent"), "retry");
  assert.equal(decide("commit_create", "absent_head_unchanged"), "retry");
  assert.equal(decide("push", "absent_or_ancestor"), "retry");
  assert.equal(decide("pr_update", "older_owned_revision"), "retry");
});

test("provider work uses resume or interruption rather than replaying an old attempt", () => {
  assert.equal(decide("provider_turn", "resumable_active"), "resume");
  assert.equal(decide("provider_turn", "terminal_result"), "adopt");
  assert.equal(decide("provider_turn", "absent_proven"), "interrupt");
  assert.notEqual(decide("provider_turn", "absent_proven"), "retry");
});

test("suite validation fails when a catalog operation loses coverage", () => {
  const suite = loadSuite();
  suite.scenarios = suite.scenarios.filter((scenario) => scenario.operation !== "pr_update");
  const errors = validateSuite(suite);
  assert.ok(errors.some((item) => item.code === "RECOVERY_COVERAGE_MISSING" && item.message.includes("pr_update")));
});

test("suite validation rejects an expectation that would replay an exact effect", () => {
  const suite = loadSuite();
  const scenario = suite.scenarios.find((item) => item.id === "commit_ref_advanced_before_ack");
  scenario.expectedDecision = "retry";
  const errors = validateSuite(suite);
  assert.ok(errors.some((item) => item.code === "RECOVERY_EXPECTATION_MISMATCH"));
});
