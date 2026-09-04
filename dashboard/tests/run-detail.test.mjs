import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

import {
  attemptsForVisit,
  sortNodeVisits,
  terminalBoundary,
} from "../src/pages/runDetailModel.ts";

const early = "2026-09-03T12:00:00Z";
const late = "2026-09-03T13:00:00Z";

function node(id, nodeId, status, createdAt) {
  return {
    id,
    runId: "run_01K3Z1C2AAAAAAAAAAAAAAAAAA",
    nodeId,
    status,
    resourceVersion: 1,
    lastGlobalPosition: 1,
    createdAt,
    updatedAt: createdAt,
  };
}

function attempt(id, visitId, status, createdAt, overrides = {}) {
  return {
    id,
    runId: "run_01K3Z1C2AAAAAAAAAAAAAAAAAA",
    visitId,
    nodeId: "technical_design",
    scenario: "fake-success",
    provider: "fake",
    status,
    lastSequence: 0,
    resourceVersion: 1,
    lastGlobalPosition: 1,
    createdAt,
    updatedAt: createdAt,
    ...overrides,
  };
}

test("run detail preserves authoritative node-visit chronology without synthesizing visits", () => {
  const second = node("visit_b", "implementation", "running", late);
  const tieB = node("visit_c", "validate", "ready", late);
  const first = node("visit_a", "technical_design", "succeeded", early);
  const input = [second, tieB, first];

  const sorted = sortNodeVisits(input);

  assert.deepEqual(sorted.map((visit) => visit.id), ["visit_a", "visit_b", "visit_c"]);
  assert.deepEqual(sorted.map((visit) => visit.status), ["succeeded", "running", "ready"]);
  assert.equal(sorted[0], first);
  assert.equal(sorted[1], second);
  assert.equal(sorted[2], tieB);
  assert.deepEqual(input.map((visit) => visit.id), ["visit_b", "visit_c", "visit_a"], "sorting must not mutate the projection");
});

test("attempt chronology uses persisted visit identity and never infers membership from node id", () => {
  const retry = attempt("attempt_c", "visit_a", "running", late);
  const otherVisit = attempt("attempt_b", "visit_b", "failed", early);
  const first = attempt("attempt_a", "visit_a", "failed", early);
  const legacyWithoutVisit = attempt("attempt_legacy", undefined, "succeeded", early);

  const attempts = attemptsForVisit([retry, otherVisit, first, legacyWithoutVisit], "visit_a");

  assert.deepEqual(attempts.map((value) => value.id), ["attempt_a", "attempt_c"]);
  assert.deepEqual(attempts.map((value) => value.status), ["failed", "running"]);
  assert.ok(!attempts.includes(otherVisit));
  assert.ok(!attempts.includes(legacyWithoutVisit));
});

test("terminal boundaries come only from the frozen route snapshot", () => {
  const frozenRoute = {
    entry: "technical_design",
    terminals: ["validated", "delivered"],
    nodes: [],
    transitions: [],
    excludedNodes: [],
    inputRequirements: [],
  };

  assert.equal(terminalBoundary(frozenRoute), frozenRoute.terminals);
  assert.deepEqual(terminalBoundary(undefined), []);
});

test("OpenAPI run view extends the existing run/attempt contract with required array projections", async () => {
  const openapi = JSON.parse(await readFile(new URL("../../schemas/openapi-v1alpha1.json", import.meta.url), "utf8"));
  const schemas = openapi.components.schemas;
  const runView = schemas.RunView;

  assert.equal(runView.additionalProperties, false);
  for (const field of ["schemaVersion", "run", "nodes", "attempts", "timeline", "timelinePageInfo", "commands", "commandsPageInfo"]) {
    assert.ok(runView.required.includes(field), `RunView must require ${field}`);
  }
  assert.equal(runView.properties.run.$ref, "#/components/schemas/Run");
  assert.deepEqual(runView.properties.nodes, { type: "array", items: { $ref: "#/components/schemas/NodeVisit" } });
  assert.deepEqual(runView.properties.attempts, { type: "array", items: { $ref: "#/components/schemas/Attempt" } });
  assert.deepEqual(runView.properties.timeline, { type: "array", items: { $ref: "#/components/schemas/RunTimelineEntry" } });
  assert.deepEqual(runView.properties.commands, { type: "array", items: { $ref: "#/components/schemas/RunCommandSummary" } });
});

test("frozen route schema is closed and preserves every route-evidence collection", async () => {
  const openapi = JSON.parse(await readFile(new URL("../../schemas/openapi-v1alpha1.json", import.meta.url), "utf8"));
  const schemas = openapi.components.schemas;
  const route = schemas.FrozenRoute;

  assert.equal(schemas.Run.properties.routeSnapshot.$ref, "#/components/schemas/FrozenRoute");
  assert.equal(route.additionalProperties, false);
  assert.deepEqual(route.required, ["entry", "terminals", "nodes", "transitions", "excludedNodes", "inputRequirements"]);
  assert.equal(route.properties.entry.type, "string");
  for (const field of ["terminals", "nodes", "transitions", "excludedNodes", "inputRequirements"]) {
    assert.equal(route.properties[field].type, "array", `FrozenRoute.${field} must be an array`);
  }
});

test("public timeline and command summary schemas cannot carry raw or replay-sensitive material", async () => {
  const openapi = JSON.parse(await readFile(new URL("../../schemas/openapi-v1alpha1.json", import.meta.url), "utf8"));
  const timeline = openapi.components.schemas.RunTimelineEntry;
  const command = openapi.components.schemas.RunCommandSummary;

  assert.equal(timeline.additionalProperties, false);
  assert.equal(command.additionalProperties, false);
  for (const field of ["data", "metadata", "commandId", "actor", "actorId"]) {
    assert.ok(!(field in timeline.properties), `timeline must omit ${field}`);
  }
  for (const field of ["idempotencyKey", "requestDigest", "request", "response"]) {
    assert.ok(!(field in command.properties), `command summary must omit ${field}`);
  }
  assert.ok("actorType" in timeline.properties, "timeline retains non-identifying actor classification");
});
