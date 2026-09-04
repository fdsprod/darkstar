import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

import {
  agentChanged,
  buildProviderPermissionDecision,
  canCancelAgent,
  decodeAgentLog,
  formatElapsed,
  groupAgents,
  logTailOffset,
  mergeAgentLogWindow,
  permissionActionPresentation,
  providerPermissionChanged,
} from "../src/pages/agentsModel.ts";
import { tabKeyTarget } from "../src/accessibility/keyboard.ts";

function agent(id, status, overrides = {}) {
  return { attemptId: id, status, allowedActions: [], resourceVersion: 1, updatedAt: "2026-09-04T00:00:00Z", ...overrides };
}

test("agent grouping preserves authoritative queue order and does not mutate the projection", () => {
  const values = [agent("active-a", "running"), agent("queued-a", "created"), agent("active-b", "validating"), agent("queued-b", "created")];
  const groups = groupAgents(values);
  assert.deepEqual(groups.queued.map((value) => value.attemptId), ["queued-a", "queued-b"]);
  assert.deepEqual(groups.active.map((value) => value.attemptId), ["active-a", "active-b"]);
  assert.deepEqual(values.map((value) => value.attemptId), ["active-a", "queued-a", "active-b", "queued-b"]);
});

test("agent tabs expose wrapped arrow navigation and boundary keys", () => {
  assert.equal(tabKeyTarget(0, "ArrowRight", 2), 1);
  assert.equal(tabKeyTarget(1, "ArrowRight", 2), 0);
  assert.equal(tabKeyTarget(0, "ArrowLeft", 2), 1);
  assert.equal(tabKeyTarget(1, "Home", 2), 0);
  assert.equal(tabKeyTarget(0, "End", 2), 1);
  assert.equal(tabKeyTarget(0, "Enter", 2), undefined);
});

test("provider permission decisions preserve exact scope and server authority", () => {
  const permission = {
    id: "permission-a", providerTurnId: "turn-a", scope: { target: "command", operation: "execute", subject: "go test" },
    scopeDigest: "a".repeat(64), policyDigest: "b".repeat(64), status: "pending", allowedActions: ["allow_once", "deny"],
    resourceVersion: 1, updatedAt: "2026-09-04T00:00:00Z",
  };
  assert.deepEqual(buildProviderPermissionDecision(permission, "allow_once"), { decision: "allow_once", scopeDigest: "a".repeat(64) });
  assert.throws(() => buildProviderPermissionDecision(permission, "cancel"), /not currently allowed/);
  assert.throws(() => buildProviderPermissionDecision({ ...permission, status: "decision_recorded" }, "deny"), /not currently allowed/);
  assert.match(permissionActionPresentation("allow_once").description, /only this recorded provider interaction/);
  assert.equal(providerPermissionChanged(permission, { ...permission }), false);
  assert.equal(providerPermissionChanged(permission, { ...permission, resourceVersion: 2 }), true);
  assert.equal(providerPermissionChanged(permission, { ...permission, scope: { ...permission.scope, subject: "go test ./..." } }), true);
});

test("cancellation authority comes only from server allowedActions", () => {
  assert.equal(canCancelAgent(agent("terminal-but-authorized", "succeeded", { allowedActions: ["cancel"] })), true);
  assert.equal(canCancelAgent(agent("running-but-not-authorized", "running")), false);
});

test("agent identity detects version, lifecycle, action and log changes", () => {
  const first = agent("attempt-a", "running", { allowedActions: ["cancel"], logReference: "log-a" });
  assert.equal(agentChanged(first, { ...first }), false);
  assert.equal(agentChanged(first, { ...first, resourceVersion: 2 }), true);
  assert.equal(agentChanged(first, { ...first, allowedActions: [] }), true);
  assert.equal(agentChanged(first, { ...first, logReference: "log-b" }), true);
});

test("elapsed time formatting is compact and rejects impossible projections", () => {
  assert.equal(formatElapsed(59_999), "59s");
  assert.equal(formatElapsed(61_000), "1m 1s");
  assert.equal(formatElapsed(3_661_000), "1h 1m");
  assert.equal(formatElapsed(-1), "Unavailable");
});

test("log windows append overlapping pages and remain memory bounded", () => {
  const encoder = new TextEncoder();
  const first = mergeAgentLogWindow(undefined, { offset: 4, nextOffset: 8, size: 10, complete: false, bytes: encoder.encode("abcd") }, 6);
  const merged = mergeAgentLogWindow(first, { offset: 8, nextOffset: 10, size: 10, complete: true, bytes: encoder.encode("ef") }, 6);
  assert.equal(merged.offset, 4);
  assert.equal(merged.nextOffset, 10);
  assert.equal(merged.omittedBefore, true);
  assert.equal(decodeAgentLog(merged), "abcdef");
  const capped = mergeAgentLogWindow(merged, { offset: 10, nextOffset: 12, size: 12, complete: true, bytes: encoder.encode("gh") }, 6);
  assert.equal(capped.offset, 6);
  assert.equal(decodeAgentLog(capped), "cdefgh");
  assert.equal(logTailOffset(300, 256), 44);
});

test("agent route is API backed and preserves binary log cursor metadata", async () => {
  const [router, page, client] = await Promise.all([
    readFile(new URL("../src/app/router.tsx", import.meta.url), "utf8"),
    readFile(new URL("../src/pages/AgentsPage.tsx", import.meta.url), "utf8"),
    readFile(new URL("../src/api/client.ts", import.meta.url), "utf8"),
  ]);
  assert.match(router, /case "agents": return <AgentsPage/);
  assert.doesNotMatch(router, /case "agents": return <PlaceholderPage/);
  assert.match(page, /allowedActions/);
  assert.match(page, /state\.cursor/);
  assert.match(page, /tabKeyTarget/);
  assert.match(page, /tabIndex=\{tab === "agents" \? 0 : -1\}/);
  assert.match(page, /window\.setInterval/);
  assert.match(page, /window\.clearInterval/);
  assert.match(page, /role="log" tabIndex=\{0\}/);
  assert.match(page, /role="alert"/);
  assert.match(page, /<dialog/);
  for (const name of ["X-Darkstar-Log-Offset", "X-Darkstar-Log-Next-Offset", "X-Darkstar-Log-Size", "X-Darkstar-Log-Complete"]) assert.match(client, new RegExp(name));
});

test("agent and provider permission OpenAPI contracts are closed, redacted, and server-authorized", async () => {
  const openapi = JSON.parse(await readFile(new URL("../../schemas/openapi-v1alpha1.json", import.meta.url), "utf8"));
  const schemas = openapi.components.schemas;
  for (const name of ["Agent", "AgentExecution", "AgentWorkspace", "ProviderInteractionScope", "ProviderPermission", "ProviderPermissionEvidence", "ProviderPermissionDecisionRequest", "UserInputRequest", "UserInputQuestion", "UserInputSchema", "UserInputAnswerValue", "UserInputAnswerEnvelope"]) {
    assert.equal(schemas[name].additionalProperties, false, `${name} must reject unknown fields`);
  }
  for (const field of ["allowedActions", "resourceVersion", "execution", "elapsedMilliseconds"]) assert.ok(schemas.Agent.required.includes(field));
  for (const field of ["providerTurnId", "scope", "scopeDigest", "policyDigest", "evidence"]) assert.ok(schemas.ProviderPermission.required.includes(field));
  assert.equal(schemas.InputRequest.properties.request.$ref, "#/components/schemas/UserInputRequest");
  assert.equal(schemas.InputRequestAnswerRequest.properties.answer.$ref, "#/components/schemas/UserInputAnswerEnvelope");
  assert.equal(schemas.Agent.properties.allowedActions.items.const, "cancel");
  assert.deepEqual(schemas.ProviderPermissionDecisionRequest.properties.decision.enum, ["allow_once", "deny", "cancel"]);
  assert.ok(!schemas.ProviderPermissionDecisionRequest.properties.allow_for_session);
  for (const forbidden of ["raw", "arguments", "path", "token", "rawEvidenceRef"]) assert.ok(!(forbidden in schemas.ProviderPermissionEvidence.properties));
  assert.equal(openapi.paths["/api/v1/agents/{attemptId}/cancel"].post.operationId, "cancelAgent");
  assert.equal(openapi.paths["/api/v1/agents/permissions/{permissionRequestId}/decisions"].post.operationId, "decideProviderPermission");
  assert.equal(openapi.paths["/api/v1/agents/{attemptId}/logs"].get.operationId, "readAgentLog");
});
