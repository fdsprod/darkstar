import assert from "node:assert/strict";
import { dirname, resolve } from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

import {
  implementationPreflight,
  loadRegister,
  validateGovernance,
} from "../scripts/governance-reference.mjs";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const decisionPath = resolve(root, "docs", "decisions", "decision-register.json");
const riskPath = resolve(root, "docs", "risks", "risk-register.json");

function load() {
  return { decisions: loadRegister(decisionPath), risks: loadRegister(riskPath) };
}

test("every foundational spike decision is discoverable and linked to work", () => {
  const { decisions, risks } = load();
  assert.deepEqual(validateGovernance(decisions, risks, root), []);
  assert.deepEqual(decisions.decisions.map((entry) => entry.id), Array.from({ length: 10 }, (_, index) => `DS-${String(index + 1).padStart(3, "0")}`));
  for (const entry of decisions.decisions) {
    assert.match(entry.sourceIssue.id, /^DAR-\d+$/);
    assert.ok(entry.sourceIssue.url.includes(entry.sourceIssue.id));
    assert.ok(entry.affectedIssues.length > 0);
  }
});

test("implementation preflight accepts current decisions and surfaces related risks", () => {
  const { decisions, risks } = load();
  const result = implementationPreflight(decisions, risks, ["DS-004", "DS-010"]);
  assert.equal(result.status, "passed");
  assert.deepEqual(result.checked.map((entry) => entry.state), ["accepted", "accepted"]);
  assert.ok(result.relatedRisks.some((risk) => risk.id === "RSK-005"));
});

test("implementation preflight blocks a superseded decision and names its replacement", () => {
  const { decisions, risks } = load();
  const decision = decisions.decisions.find((entry) => entry.id === "DS-004");
  decision.lifecycle = { state: "superseded", supersededAt: "2026-09-01", supersededBy: "DS-010" };
  const result = implementationPreflight(decisions, risks, ["DS-004"]);
  assert.equal(result.status, "blocked");
  assert.equal(result.failures[0].code, "GOVERNANCE_DECISION_NOT_CURRENT");
  assert.match(result.failures[0].message, /DS-010/);
});

test("tagged lifecycles reject contradictory sibling-state fields", () => {
  const { decisions, risks } = load();
  decisions.decisions[0].lifecycle.supersededBy = "DS-002";
  const failures = validateGovernance(decisions, risks, root);
  assert.ok(failures.some((failure) => failure.code === "GOVERNANCE_LIFECYCLE_INVALID"));
});

test("status metadata cannot escape the tagged lifecycle record", () => {
  const { decisions, risks } = load();
  decisions.decisions[0].supersededBy = "DS-002";
  const failures = validateGovernance(decisions, risks, root);
  assert.ok(failures.some((failure) => failure.code === "GOVERNANCE_RECORD_INVALID"));
});

test("risk records link only registered decisions", () => {
  const { decisions, risks } = load();
  risks.risks[0].affectedDecisions.push("DS-999");
  const failures = validateGovernance(decisions, risks, root);
  assert.ok(failures.some((failure) => failure.code === "GOVERNANCE_RISK_INVALID" && failure.message.includes("DS-999")));
});

test("accepted risk state carries review authority without open-state flags", () => {
  const { risks } = load();
  const accepted = risks.risks.find((risk) => risk.lifecycle.state === "accepted");
  assert.deepEqual(Object.keys(accepted.lifecycle).sort(), ["acceptedAt", "acceptedBy", "reviewBy", "state"]);
  assert.ok(risks.risks.filter((risk) => risk.lifecycle.state === "open").every((risk) => Object.keys(risk.lifecycle).length === 1));
});
