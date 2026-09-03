import assert from "node:assert/strict";
import { dirname, join, resolve } from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

import { Runner, WorkflowError, freezeRoute, loadJson, resolveProfile, validateWorkflow } from "../scripts/workflow-reference.mjs";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const workflowPath = (name) => join(root, "examples", "workflows", name);
const scenarioPath = (name) => join(root, "examples", "scenarios", name);

function deliveryRun(flags) {
  const path = workflowPath("software-delivery.json");
  const document = loadJson(path);
  const fixture = loadJson(scenarioPath("software-delivery-full.json"));
  Object.assign(fixture.runInputs, flags);
  fixture.results.p3_poc = [{ poc_findings: { artifactId: "artifact:poc:1" } }];
  fixture.results.p5_experience_design = [{ experience_design: { artifactId: "artifact:experience:1" } }];
  fixture.results.p7_technical_research = [{ technical_research: { artifactId: "artifact:research:1" } }];
  fixture.checkpoints.p3_poc = ["approve"];
  fixture.checkpoints.p5_experience_design = ["approve"];
  fixture.checkpoints.p7_technical_research = ["approve"];
  return new Runner(document, path, fixture, document.spec.routeDefaults.entry, document.spec.routeDefaults.terminals).run();
}

test("the shipped full route validates and reaches production verification", () => {
  const path = workflowPath("software-delivery.json");
  const document = loadJson(path);
  assert.deepEqual(validateWorkflow(document, path), []);
  const route = freezeRoute(document, document.spec.routeDefaults.entry, document.spec.routeDefaults.terminals);
  assert.equal(route.nodes.has("p0_intake"), true);
  assert.equal(route.nodes.has("p17_verification"), true);
  assert.equal(deliveryRun({}).status, "completed");
});

test("every shipped profile has a valid ordinary-route preview", () => {
  const expected = new Map([
    ["idea_to_production", ["software-delivery.json", "p0_intake", "p17_verification"]],
    ["poc", ["software-delivery.json", "p3_poc", "p3_poc"]],
    ["prd_only", ["software-delivery.json", "p4_requirements", "p4_requirements"]],
    ["design_only", ["software-delivery.json", "p8_technical_design", "p8_technical_design"]],
    ["accepted_story", ["story-execution.json", "s0_readiness", "s6_validation"]],
    ["implementation_only", ["story-execution.json", "s5_implementation", "s6_validation"]],
    ["bug", ["story-execution.json", "s0_readiness", "s6_validation"]],
    ["validation", ["story-execution.json", "s6_validation", "s6_validation"]],
    ["pr", ["software-delivery.json", "p13_pull_request", "p13_pull_request"]],
    ["release", ["software-delivery.json", "p15_release_readiness", "p17_verification"]],
  ]);
  for (const [profileId, [file, entry, terminal]] of expected) {
    const document = loadJson(workflowPath(file));
    const profile = resolveProfile(document, profileId);
    const route = freezeRoute(document, profile.entry, profile.terminals);
    assert.equal(route.entry, entry, profileId);
    assert.deepEqual([...route.terminals], [terminal], profileId);
  }
});

test("POC, experience, and technical research activate only from applicability input", () => {
  const cases = [
    [{}, []],
    [{ needs_poc: true }, ["p3_poc"]],
    [{ needs_experience_design: true }, ["p5_experience_design"]],
    [{ needs_technical_research: true }, ["p7_technical_research"]],
    [{ needs_poc: true, needs_experience_design: true, needs_technical_research: true }, ["p3_poc", "p5_experience_design", "p7_technical_research"]],
  ];
  const conditional = ["p3_poc", "p5_experience_design", "p7_technical_research"];
  for (const [flags, expected] of cases) {
    const result = deliveryRun(flags);
    assert.deepEqual(conditional.filter((nodeId) => result.visits[nodeId]), expected, JSON.stringify(flags));
  }
});

test("invalid profile references and defaults fail closed", () => {
  const path = workflowPath("software-delivery.json");
  const document = loadJson(path);
  document.spec.profiles.invalid_entry = { description: "invalid", entry: "p16_release", terminals: ["p17_verification"], inputDefaults: {} };
  document.spec.profiles.invalid_input = { description: "invalid", entry: "p0_intake", terminals: ["p17_verification"], inputDefaults: { missing: true } };
  document.spec.profiles.invalid_type = { description: "invalid", entry: "p0_intake", terminals: ["p17_verification"], inputDefaults: { needs_poc: "yes" } };
  const codes = validateWorkflow(document, path).map((error) => error.code);
  assert.ok(codes.includes("WF_DEFAULT_ROUTE_INVALID"));
  assert.ok(codes.includes("WF_REFERENCE_MISSING"));
  assert.ok(codes.includes("WF_BINDING_INCOMPATIBLE"));

  const clean = loadJson(path);
  assert.throws(() => resolveProfile(clean, "missing"), (error) => error instanceof WorkflowError && error.code === "ROUTE_PROFILE_INVALID");
});
