import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

import {
  assessmentChanged,
  buildReadinessDecisionRequest,
  groupFindings,
  readinessActionKey,
  readinessActionPresentation,
  routeChangePresentation,
} from "../src/pages/runReadinessModel.ts";

function readinessView(overrides = {}) {
  return {
    assessment: {
      assessmentId: "assessment-1",
      runId: "run-1",
      nodeId: "review",
      scores: [],
      findings: [],
      remedies: [],
      disposition: "choice_required",
      digest: "sha256:assessment",
    },
    status: "pending",
    allowedActions: [
      { choice: "continue" },
      { choice: "supply_input", remedy: { code: "provide_scope", target: "scope", action: "supply_input", description: "Provide scope." } },
      { choice: "cancel" },
    ],
    resourceVersion: 7,
    createdAt: "2026-09-03T10:00:00Z",
    updatedAt: "2026-09-03T10:00:00Z",
    ...overrides,
  };
}

test("readiness findings are exhaustively grouped by their server discriminator", () => {
  const evidence = [{ source: "provider", observation: "Observed." }];
  const findings = [
    { level: "recommendation", code: "recommend", summary: "Recommendation", evidence, remedyCode: "provide_scope" },
    { level: "information", code: "context", summary: "Context", evidence },
    { level: "policy_gate", code: "policy", summary: "Policy", evidence, policy: "security", status: "unsatisfied" },
    { level: "invariant", code: "invariant", summary: "Invariant", evidence, invariant: "terminal remains reachable", status: "upheld" },
  ];

  const groups = groupFindings(findings);
  assert.deepEqual(groups.information.map(({ code }) => code), ["context"]);
  assert.deepEqual(groups.recommendation.map(({ code }) => code), ["recommend"]);
  assert.deepEqual(groups.policy_gate.map(({ code }) => code), ["policy"]);
  assert.deepEqual(groups.invariant.map(({ code }) => code), ["invariant"]);
  assert.deepEqual(findings.map(({ code }) => code), ["recommend", "context", "policy", "invariant"]);
});

test("decision requests copy only the exact server-authorized choice, digest, remedy, and reason", () => {
  const view = readinessView();
  assert.equal(readinessActionKey(view.allowedActions[1]), "supply_input:provide_scope");
  assert.deepEqual(buildReadinessDecisionRequest(view, "supply_input:provide_scope", "  Scope supplied by owner.  "), {
    action: "supply_input",
    assessmentDigest: "sha256:assessment",
    reason: "Scope supplied by owner.",
    remedyCode: "provide_scope",
  });
  assert.deepEqual(buildReadinessDecisionRequest(view, "continue", "Evidence reviewed."), {
    action: "continue",
    assessmentDigest: "sha256:assessment",
    reason: "Evidence reviewed.",
  });
  assert.deepEqual(buildReadinessDecisionRequest(view, "cancel", "Defer this assessment."), {
    action: "cancel",
    assessmentDigest: "sha256:assessment",
    reason: "Defer this assessment.",
  });
});

test("decision construction rejects unapproved, incomplete, and already-decided choices", () => {
  const view = readinessView();
  assert.throws(() => buildReadinessDecisionRequest(view, "accept_route_change", "Looks good."), /currently allowed/);
  assert.throws(() => buildReadinessDecisionRequest(view, "continue", "   "), /Explain why/);
  assert.throws(() => buildReadinessDecisionRequest({ ...view, status: "decided" }, "continue", "Again."), /already has/);
  assert.throws(() => buildReadinessDecisionRequest({ ...view, allowedActions: [{ choice: "supply_input" }] }, "supply_input:", "Input."), /no server-provided remedy/);
});

test("action copy keeps readiness controls distinct from run controls", () => {
  assert.match(readinessActionPresentation({ choice: "continue" }).description, /separate run-continue command/);
  assert.match(readinessActionPresentation({ choice: "cancel" }).description, /does not cancel the run/);
});

test("assessment identity, digest, resource version, status changes all require rereview", () => {
  const view = readinessView();
  assert.equal(assessmentChanged(view, structuredClone(view)), false);
  assert.equal(assessmentChanged(view, { ...structuredClone(view), resourceVersion: 8 }), true);
  assert.equal(assessmentChanged(view, { ...structuredClone(view), status: "decided" }), true);
  assert.equal(assessmentChanged(view, { ...structuredClone(view), assessment: { ...view.assessment, digest: "sha256:new" } }), true);
});

test("route-change presentation is a detached display projection, never client patch operations", () => {
  const change = {
    patchId: "patch-1",
    reason: "Include security review.",
    impact: {
      addedNodes: ["security"],
      removedNodes: [],
      addedTransitions: ["review-security"],
      previousTerminals: ["delivery"],
      proposedTerminals: ["delivery"],
    },
    candidate: {
      runId: "run-1",
      revision: 2,
      route: {
        entry: "intake",
        terminals: ["delivery"],
        nodes: [{ id: "intake" }, { id: "security" }, { id: "delivery" }],
        transitions: [{ id: "intake-security", from: "intake", to: "security" }, { id: "security-delivery", from: "security", to: "delivery" }],
        excludedNodes: [],
        inputRequirements: [],
      },
    },
    authorizationMode: "require_approval",
    scopeDigest: "scope",
    validationDigest: "validation",
    policyDigest: "policy",
  };

  const display = routeChangePresentation(change);
  assert.deepEqual(display.addedNodes, ["security"]);
  assert.deepEqual(display.removedTransitions, []);
  assert.equal(display.candidate, change.candidate.route);
  assert.equal("operations" in display, false);
  display.addedNodes.push("local-only");
  assert.deepEqual(change.impact.addedNodes, ["security"]);
});

test("readiness client binds the generated operations and required concurrency fields", async () => {
  const [client, generated] = await Promise.all([
    readFile(new URL("../src/api/client.ts", import.meta.url), "utf8"),
    readFile(new URL("../src/api/schema.generated.ts", import.meta.url), "utf8"),
  ]);
  assert.match(generated, /"getRunReadiness": \{ method: "GET"; path: "\/api\/v1\/runs\/\{runId\}\/readiness"/);
  assert.match(generated, /"decideRunReadiness": \{ method: "POST"; path: "\/api\/v1\/runs\/\{runId\}\/readiness\/decisions"/);
  assert.match(client, /decideRunReadiness\(runId: string, resourceVersion: number, idempotencyKey: string, body: Schemas\["ReadinessDecisionRequest"\]/);
  assert.match(client, /this\.operation\("decideRunReadiness", \{ path: \{ runId \}, body, resourceVersion, idempotencyKey, signal \}\)/);
  assert.match(client, /headers\.set\("If-Match", `\\"\$\{options\.resourceVersion\}\\"`\)/);
  assert.match(client, /headers\.set\("Idempotency-Key", options\.idempotencyKey\)/);
});

test("readiness route is reachable and the page does not invoke run-control or patch APIs", async () => {
  const [router, page] = await Promise.all([
    readFile(new URL("../src/app/router.tsx", import.meta.url), "utf8"),
    readFile(new URL("../src/pages/RunReadinessPage.tsx", import.meta.url), "utf8"),
  ]);
  assert.match(router, /\/work\/:workId\/run\/:runId\/readiness/);
  assert.match(page, /view\.allowedActions\.map/);
  assert.match(page, /cause\.status === 409 \|\| cause\.status === 412/);
  assert.doesNotMatch(page, /apiClient\.(?:pauseRun|resumeRun|retryRun|cancelRun|continueRun)/);
  assert.doesNotMatch(page, /patchOperations|applyRoutePatch/);
});
