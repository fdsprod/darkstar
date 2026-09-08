import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

import { assessmentPresentation, buildPreparationRequest, emptyPreparationDraft, routeDifference } from "../src/pages/routePreparationModel.ts";

function route(nodes, entry = nodes[0], terminals = [nodes.at(-1)]) {
  return { entry, terminals, nodes: nodes.map((id) => ({ id })), transitions: [], excludedNodes: [], inputRequirements: [] };
}

function assessment(overrides = {}) {
  const selected = route(["intake", "implement", "verify"]);
  return {
    schemaVersion: 1,
    input: { work: {}, project: {}, workflow: {}, workflowDigest: "a".repeat(64), policy: {}, context: {}, answers: {}, evidence: [], ...overrides.input },
    inputDigest: "b".repeat(64), advice: { confidence: "high", candidates: [], evidenceUsed: [] }, route: selected,
    rationale: "Smallest complete route.", questions: [], confirmationReasons: [], alternatives: [], digest: "c".repeat(64), ...overrides,
  };
}

test("preparation request preserves typed answers, inputs, and distinct evidence references", () => {
  const draft = { ...emptyPreparationDraft(), answers: { scope: "  desktop  ", empty: " " }, runInputs: '{"risk":2}', evidence: " docs/plan.md\ndocs/plan.md\nhttps://example.test/evidence " };
  assert.deepEqual(buildPreparationRequest("work_1", draft), { workItemId: "work_1", preparation: { answers: { scope: "desktop" }, runInputs: { risk: 2 }, evidence: ["docs/plan.md", "https://example.test/evidence"] } });
  assert.throws(() => buildPreparationRequest("work_1", { ...draft, runInputs: "[]" }), /JSON object/);
  assert.throws(() => buildPreparationRequest("work_1", { ...draft, workflowId: "delivery" }), /both a workflow and an exact version/);
  assert.deepEqual(buildPreparationRequest("work_1", { ...emptyPreparationDraft(), entryNodeId: " design ", terminalNodeIds: "verify, publish,verify" }), { workItemId: "work_1", preparation: { routeOverride: { from: "design", until: ["verify", "publish"] } } });
  assert.throws(() => buildPreparationRequest("work_1", { ...emptyPreparationDraft(), profile: "delivery", entryNodeId: "design" }), /not both/);
});

test("assessment presentation derives one readiness state from authoritative facts", () => {
  assert.equal(assessmentPresentation(assessment()).readiness, "automatic");
  assert.equal(assessmentPresentation(assessment({ questions: [{ id: "scope", prompt: "Scope?" }] })).readiness, "input_required");
  assert.equal(assessmentPresentation(assessment({ confirmationReasons: ["Consequential stage."] })).readiness, "confirmation_required");
  const advisory = assessment({ input: { evidence: [{ reference: "docs/plan.md", digest: "d".repeat(64), content: "plan" }] }, advice: { confidence: "high", candidates: [], evidenceUsed: ["docs/plan.md"] } });
  assert.deepEqual(assessmentPresentation(advisory).evidence, [{ reference: "docs/plan.md", available: true, used: true }]);
  assert.equal(assessmentPresentation(advisory).readiness, "advisory");
});

test("before and after route comparison reports only actual boundary or node changes", () => {
  const before = route(["intake", "design", "verify"]);
  const after = route(["intake", "implement", "verify"]);
  assert.deepEqual(routeDifference(before, before), undefined);
  assert.deepEqual(routeDifference(before, after)?.added, ["implement"]);
  assert.deepEqual(routeDifference(before, after)?.removed, ["design"]);
});

test("work context binds prepare, exact-digest confirm, cancellation, and stale draft handling", async () => {
  const [page, component, client] = await Promise.all([
    readFile(new URL("../src/pages/WorkDetailPage.tsx", import.meta.url), "utf8"),
    readFile(new URL("../src/pages/WorkRoutePreparation.tsx", import.meta.url), "utf8"),
    readFile(new URL("../src/api/client.ts", import.meta.url), "utf8"),
  ]);
  assert.match(page, /<WorkRoutePreparation work=\{view\.work\} run=\{currentRun\}/);
  assert.match(component, /apiClient\.prepareRun\(request/);
  assert.match(component, /apiClient\.startRun\(expectedRun, expectedVersion,[^\n]+expectedDigest\)/);
  assert.match(component, /apiClient\.cancelRun\(expectedRun, expectedVersion/);
  assert.match(component, /Your answers and override draft are preserved/);
  assert.match(component, /Skipped stages/);
  assert.match(client, /body: \{ assessmentDigest \}/);
});
