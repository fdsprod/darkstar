import assert from "node:assert/strict";
import { readdirSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

import {
  Runner,
  WorkflowError,
  applyPatchBeforeRun,
  loadJson,
  validateWorkflow,
} from "../scripts/workflow-reference.mjs";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const workflowPath = (name) => join(root, "examples", "workflows", name);
const scenarioPath = (name) => join(root, "examples", "scenarios", name);

function runExample(workflow, scenario, entry, terminals, overrides = new Map()) {
  const path = workflowPath(workflow);
  const document = loadJson(path);
  const fixture = loadJson(scenarioPath(scenario));
  const defaults = document.spec.routeDefaults;
  return new Runner(document, path, fixture, entry ?? defaults.entry, terminals ?? defaults.terminals, overrides).run();
}

function expectCode(action, code) {
  assert.throws(action, (error) => error instanceof WorkflowError && error.code === code);
}

test("all shipped default, MVP, sub-workflow, and custom examples validate", () => {
  for (const name of readdirSync(join(root, "examples", "workflows")).filter((item) => item.endsWith(".json"))) {
    const path = workflowPath(name);
    assert.deepEqual(validateWorkflow(loadJson(path), path), [], name);
  }
});

test("MVP checkpoint revisions preserve the final approved candidate", () => {
  const result = runExample("mvp-walking-skeleton.json", "mvp-walking-skeleton.json");
  assert.equal(result.status, "completed");
  assert.equal(result.outputs.technical_design.technical_design.revision, 2);
  assert.deepEqual(result.terminalsReached, ["technical_design"]);
});

test("full default route executes a pinned story sub-workflow", () => {
  const result = runExample("software-delivery.json", "software-delivery-full.json");
  assert.equal(result.status, "completed");
  assert.deepEqual(result.terminalsReached, ["p17_verification"]);
  assert.equal(result.visits.p11_story_execution, 1);
  assert.equal(result.visits.p1_route_gate, 1);
  assert.equal(result.outputs.p1_route_gate.passed, true);
  assert.equal(result.outputs.p1_route_gate.gate_evidence.policy, "darkstar/route-readiness-v1");
  assert.equal(result.visits.p1_route_review, undefined);
  assert.equal(result.outputs.p11_story_execution.story_validation.passed, true);
  assert.equal(result.visits.p3_poc, undefined);
  assert.equal(result.visits.p5_experience_design, undefined);
  assert.equal(result.visits.p7_technical_research, undefined);
});

test("recorded readiness score is evaluated by a deterministic gate before routing", () => {
  const path = workflowPath("software-delivery.json");
  const document = loadJson(path);
  const fixture = loadJson(scenarioPath("software-delivery-full.json"));
  fixture.results.p1_route_assessment[0].readiness.score = 0.5;
  fixture.results.p1_route_review = [{ route_decision: { action: "continue_with_waiver" } }];
  const result = new Runner(document, path, fixture, "p0_intake", ["p17_verification"]).run();
  assert.equal(result.outputs.p1_route_gate.passed, false);
  assert.equal(result.visits.p1_route_review, 1);
  assert.equal(result.events.some((event) => event.type === "gate.evaluated" && event.passed === false), true);
});

test("selected middle terminal suppresses its authored outgoing transition", () => {
  const result = runExample(
    "software-delivery.json",
    "software-delivery-design-only.json",
    "p8_technical_design",
    ["p8_technical_design"],
  );
  assert.deepEqual(result.visits, { p8_technical_design: 1 });
  assert.equal(result.events.some((event) => event.type === "transition.fired"), false);
});

test("story alternative joins and bounded implementation loop are deterministic", () => {
  const result = runExample("story-execution.json", "story-execution.json");
  assert.equal(result.visits.s5_implementation, 2);
  assert.equal(result.loopTraversals.s5_more_points, 1);
  assert.deepEqual(result.terminalsReached, ["s6_validation"]);
});

test("route patch inserts only predeclared nodes and transitions", () => {
  const document = loadJson(workflowPath("split-design.json"));
  const patch = loadJson(join(root, "examples", "route-patches", "insert-peer-review.json"));
  const { overrides, terminals } = applyPatchBeforeRun(document, patch, document.spec.routeDefaults.terminals);
  const fixture = loadJson(scenarioPath("split-design-with-review.json"));
  const result = new Runner(document, workflowPath("split-design.json"), fixture, "discovery", terminals, overrides).run();
  assert.equal(result.visits.peer_review, 1);
  assert.equal(result.events.some((event) => event.transition === "functional_to_technical"), false);
});

test("missing required middle-entry input has a stable code", () => {
  const path = workflowPath("mvp-walking-skeleton.json");
  const document = loadJson(path);
  const fixture = {
    runInputs: { work_item: { title: "missing repository" } },
    results: { technical_design: [{ technical_design: {} }] },
    checkpoints: { technical_design: ["approve"] },
  };
  expectCode(() => new Runner(document, path, fixture, "technical_design", ["technical_design"]).run(), "RUN_INPUT_REQUIRED");
});

test("overlapping exclusive predicates fail instead of using authored order", () => {
  const document = {
    apiVersion: "darkstar.local/v1alpha1",
    kind: "Workflow",
    metadata: { name: "test/ambiguous", version: "1.0.0" },
    spec: {
      inputs: {},
      routeDefaults: { entry: "start", terminals: ["end"] },
      nodes: {
        start: {
          type: "command", entry: true, terminal: false, inputs: {},
          outputs: { flag: { type: "boolean" } }, command: { argv: ["fixture"] },
          transitions: [
            { id: "first", to: "end", when: { const: true } },
            { id: "second", to: "end", when: { op: "eq", args: [{ ref: "output.flag" }, { literal: true }] } },
          ],
        },
        end: {
          type: "command", entry: false, terminal: true, join: { mode: "one", from: ["first", "second"] },
          inputs: {}, outputs: { done: { type: "boolean" } }, command: { argv: ["fixture"] }, transitions: [],
        },
      },
    },
  };
  assert.deepEqual(validateWorkflow(document), []);
  expectCode(
    () => new Runner(document, join(root, "ambiguous.json"), { results: { start: [{ flag: true }] } }, "start", ["end"]).run(),
    "RUN_EDGE_AMBIGUOUS",
  );
});

test("fanout requires every all-join token before scheduling", () => {
  const document = {
    apiVersion: "darkstar.local/v1alpha1", kind: "Workflow",
    metadata: { name: "test/fanout", version: "1.0.0" },
    spec: {
      inputs: {}, routeDefaults: { entry: "start", terminals: ["joined"] },
      nodes: {
        start: {
          type: "reasoning", entry: true, terminal: false, inputs: {}, outputs: {}, reasoning: { agent: "fixture" }, transitionMode: "fanout",
          transitions: [{ id: "to_left", to: "left" }, { id: "to_right", to: "right" }],
        },
        left: {
          type: "command", entry: false, terminal: false, inputs: {}, outputs: { value: { type: "string" } }, command: { argv: ["fixture"] },
          transitions: [{ id: "left_to_join", to: "joined" }],
        },
        right: {
          type: "command", entry: false, terminal: false, inputs: {}, outputs: { value: { type: "string" } }, command: { argv: ["fixture"] },
          transitions: [{ id: "right_to_join", to: "joined" }],
        },
        joined: {
          type: "command", entry: false, terminal: true, join: { mode: "all", from: ["left_to_join", "right_to_join"] },
          inputs: {
            left: { from: "node.left.output.value", type: "string" },
            right: { from: "node.right.output.value", type: "string" },
          },
          outputs: { done: { type: "boolean" } }, command: { argv: ["fixture"] }, transitions: [],
        },
      },
    },
  };
  assert.deepEqual(validateWorkflow(document), []);
  const fixture = { results: { start: [{}], left: [{ value: "L" }], right: [{ value: "R" }], joined: [{ done: true }] } };
  const result = new Runner(document, join(root, "fanout.json"), fixture, "start", ["joined"]).run();
  assert.equal(result.visits.joined, 1);
  const joinedStart = result.events.findIndex((event) => event.type === "node.started" && event.node === "joined");
  const rightSuccess = result.events.findIndex((event) => event.type === "node.succeeded" && event.node === "right");
  assert.ok(joinedStart > rightSuccess);
});

test("true bounded transition fails visibly when its budget is exhausted", () => {
  const path = workflowPath("story-execution.json");
  const document = loadJson(path);
  const fixture = {
    runInputs: { work_item: {}, repository: "C:/work/example" },
    results: {
      s5_implementation: Array.from({ length: 22 }, (_, index) => ({
        changeset: { point: index + 1 },
        progress: { completed_points: index + 1, remaining_points: 1 },
      })),
    },
  };
  expectCode(() => new Runner(document, path, fixture, "s5_implementation", ["s6_validation"]).run(), "RUN_LOOP_LIMIT_EXHAUSTED");
});

test("reasoning output cannot directly select an edge", () => {
  const document = {
    apiVersion: "darkstar.local/v1alpha1", kind: "Workflow",
    metadata: { name: "test/llm-direct-branch", version: "1.0.0" },
    spec: {
      inputs: {}, routeDefaults: { entry: "assess", terminals: ["done"] },
      nodes: {
        assess: {
          type: "reasoning", entry: true, terminal: false, inputs: {},
          outputs: { ready: { type: "boolean" } }, reasoning: { agent: "assessor" },
          transitions: [
            { id: "assess_to_done", to: "done", when: { op: "eq", args: [{ ref: "output.ready" }, { literal: true }] } },
          ],
        },
        done: {
          type: "command", entry: false, terminal: true, inputs: {}, outputs: {}, command: { argv: ["fixture"] }, transitions: [],
        },
      },
    },
  };
  assert.ok(validateWorkflow(document).some((error) => error.code === "WF_REASONING_EDGE_INVALID"));
});

test("normal-edge cycles are rejected statically", () => {
  const document = {
    apiVersion: "darkstar.local/v1alpha1", kind: "Workflow",
    metadata: { name: "test/unbounded", version: "1.0.0" },
    spec: {
      inputs: {}, routeDefaults: { entry: "cycle", terminals: ["cycle"] },
      nodes: {
        cycle: {
          type: "reasoning", entry: true, terminal: true, inputs: {}, outputs: {}, reasoning: { agent: "fixture" },
          transitions: [{ id: "again", to: "cycle" }],
        },
      },
    },
  };
  assert.ok(validateWorkflow(document).some((error) => error.code === "WF_UNBOUNDED_CYCLE"));
});
