import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

import {
  buildWorkflowPreviewRequest,
  decodeAuthoredWorkflow,
  parseRunInputs,
  previewImpact,
  readinessAdvice,
  sortWorkflowVersions,
} from "../src/pages/workflowPreviewModel.ts";

test("workflow preview requests normalize only the caller's explicit route choices", () => {
  const until = Object.freeze([" delivery ", "review", "delivery", ""]);
  const requiredNodes = Object.freeze([" security ", "review", "security", ""]);
  const input = Object.freeze({
    from: " intake ",
    until,
    requiredNodes,
    runInputsText: '{"work_item":{"title":"Ship"},"present_null":null}',
  });

  assert.deepEqual(buildWorkflowPreviewRequest(input), {
    range: {
      from: "intake",
      until: ["delivery", "review"],
    },
    context: {
      runInputs: {
        work_item: { title: "Ship" },
        present_null: null,
      },
      requiredNodes: ["review", "security"],
    },
  });

  assert.deepEqual(until, [" delivery ", "review", "delivery", ""]);
  assert.deepEqual(requiredNodes, [" security ", "review", "security", ""]);
  assert.deepEqual(buildWorkflowPreviewRequest({
    from: "   ",
    until: [],
    requiredNodes: [],
    runInputsText: "{}",
  }), {
    range: {},
    context: { runInputs: {} },
  });
});

test("workflow preview input accepts one JSON object and rejects ambiguous shapes", () => {
  assert.deepEqual(parseRunInputs('{"optional":null,"enabled":false}'), {
    optional: null,
    enabled: false,
  });
  assert.throws(() => parseRunInputs("{"), /one valid JSON object/);
  for (const source of ["[]", "null", '"value"', "42"]) {
    assert.throws(() => parseRunInputs(source), /JSON object, not an array or scalar/);
  }
});

test("workflow preview validates every authored identifier before making a request", () => {
  assert.throws(() => buildWorkflowPreviewRequest({
    from: "Not Canonical",
    until: [],
    requiredNodes: [],
    runInputsText: "{}",
  }), /Entry node must be a canonical workflow identifier/);
  assert.throws(() => buildWorkflowPreviewRequest({
    until: ["delivery-node"],
    requiredNodes: [],
    runInputsText: "{}",
  }), /Terminal nodes must use canonical workflow identifiers/);
  assert.throws(() => buildWorkflowPreviewRequest({
    until: [],
    requiredNodes: ["Review"],
    runInputsText: "{}",
  }), /Required nodes must use canonical workflow identifiers/);
  assert.throws(() => buildWorkflowPreviewRequest({
    until: [],
    requiredNodes: [],
    runInputsText: '{"work-item":{}}',
  }), /Run input .* is not a canonical workflow identifier/);
});

test("installed workflow decoding preserves authored readiness advice without inventing live state", () => {
  const definition = {
    apiVersion: "darkstar/v1alpha1",
    metadata: { displayName: "Delivery workflow", ignored: "not a view concern" },
    spec: {
      routeDefaults: { entry: "intake", terminals: ["delivery"] },
      profiles: {
        expedited: {
          description: "Review then deliver",
          entry: "review",
          terminals: ["delivery"],
        },
      },
      nodes: {
        intake: {
          displayName: "Intake",
          type: "human",
          entry: true,
          terminal: false,
          inputs: {
            request: { required: true },
            context: { required: false },
            owner: "project.owner",
          },
          readiness: {
            recommendedEvidence: [
              { role: "risk_context", description: "Document material delivery risk." },
            ],
            policyGates: [
              { policy: "route_readiness", enforcement: "advisory", description: "Ask for missing context." },
              { policy: "release_approval", enforcement: "blocking", description: "Approval must be authored." },
              { policy: "vendor_review", enforcement: "external", description: "Review happens outside Darkstar." },
            ],
            invariants: ["Explicit scope remains authoritative."],
            remedies: [
              { code: "insufficient_intake", target: "intake", action: "supply_input", description: "Supply the missing request." },
              { code: "unclear_route", target: "review", action: "clarify_decision", description: "Clarify the intended boundary." },
            ],
          },
        },
        delivery: {
          type: "automated",
          entry: false,
          terminal: true,
        },
      },
    },
  };

  const decoded = decodeAuthoredWorkflow(definition);

  assert.deepEqual(decoded, {
    apiVersion: "darkstar/v1alpha1",
    displayName: "Delivery workflow",
    routeDefaults: { entry: "intake", terminals: ["delivery"] },
    profiles: [{
      id: "expedited",
      entry: "review",
      terminals: ["delivery"],
      description: "Review then deliver",
    }],
    nodes: [
      {
        id: "intake",
        displayName: "Intake",
        type: "human",
        entry: true,
        terminal: false,
        requiredInputs: ["request", "owner"],
        readiness: {
          recommendedEvidence: [
            { role: "risk_context", description: "Document material delivery risk." },
          ],
          policyGates: [
            { policy: "route_readiness", enforcement: "advisory", description: "Ask for missing context." },
            { policy: "release_approval", enforcement: "blocking", description: "Approval must be authored." },
            { policy: "vendor_review", enforcement: "external", description: "Review happens outside Darkstar." },
          ],
          invariants: ["Explicit scope remains authoritative."],
          remedies: [
            { code: "insufficient_intake", target: "intake", action: "supply_input", description: "Supply the missing request." },
            { code: "unclear_route", target: "review", action: "clarify_decision", description: "Clarify the intended boundary." },
          ],
        },
      },
      {
        id: "delivery",
        displayName: undefined,
        type: "automated",
        entry: false,
        terminal: true,
        requiredInputs: [],
        readiness: undefined,
      },
    ],
  });

  const keys = collectKeys(decoded);
  for (const inferredState of ["status", "satisfied", "blocked", "disposition", "live", "current"]) {
    assert.ok(!keys.has(inferredState), `authored readiness must not claim a ${inferredState} state`);
  }
});

test("workflow definition decoding fails closed for unknown node structure and advice values", () => {
  assert.equal(decodeAuthoredWorkflow({ apiVersion: "v1", spec: { nodes: { intake: { type: "human" } } } }), undefined);

  const decoded = decodeAuthoredWorkflow({
    apiVersion: "v1",
    spec: {
      nodes: {
        intake: {
          type: "human",
          entry: true,
          terminal: false,
          readiness: {
            recommendedEvidence: [],
            policyGates: [{ policy: "unknown", enforcement: "automatic", description: "Do not infer this." }],
            invariants: [],
            remedies: [],
          },
        },
      },
    },
  });
  assert.equal(decoded.nodes[0].readiness, undefined);
});

test("readiness presentation labels authored advice categories without evaluating them", () => {
  const node = {
    id: "review",
    type: "human",
    entry: false,
    terminal: false,
    requiredInputs: ["request"],
    readiness: {
      recommendedEvidence: [{ role: "risk_context", description: "Capture delivery risk." }],
      policyGates: [
        { policy: "advice", enforcement: "advisory", description: "Consider another review." },
        { policy: "approval", enforcement: "blocking", description: "Approval is required." },
        { policy: "vendor", enforcement: "external", description: "Vendor review is external." },
      ],
      invariants: ["The requested scope remains fixed."],
      remedies: [
        { code: "missing_request", target: "review", action: "supply_input", description: "Supply the request." },
      ],
    },
  };

  assert.deepEqual(readinessAdvice(node), [
    { kind: "required_input", code: "request", summary: "Required input request" },
    { kind: "recommended_evidence", code: "risk_context", summary: "Capture delivery risk." },
    { kind: "policy_gate", code: "advice", summary: "Consider another review.", enforcement: "advisory" },
    { kind: "policy_gate", code: "approval", summary: "Approval is required.", enforcement: "blocking" },
    { kind: "policy_gate", code: "vendor", summary: "Vendor review is external.", enforcement: "external" },
    { kind: "invariant", code: "invariant_1", summary: "The requested scope remains fixed." },
    { kind: "remedy", code: "missing_request", summary: "Supply the request.", action: "supply_input", target: "review" },
  ]);

  const keys = collectKeys(readinessAdvice(node));
  for (const evaluatedState of ["status", "satisfied", "blocked", "selected", "applied"]) {
    assert.ok(!keys.has(evaluatedState), `advice must not evaluate ${evaluatedState}`);
  }
});

test("preview impact is a detached summary of the exact candidate frozen route", () => {
  const route = {
    entry: "intake",
    terminals: ["delivery"],
    nodes: [{ id: "intake" }, { id: "review" }, { id: "delivery" }],
    transitions: [
      { id: "intake_review", from: "intake", to: "review" },
      { id: "review_delivery", from: "review", to: "delivery" },
    ],
    excludedNodes: [{ id: "archive", reason: "past_terminal" }],
    inputRequirements: [{ code: "missing_input", node: "review", input: "request", source: "run_input" }],
  };

  const impact = previewImpact(route);

  assert.deepEqual(impact, {
    entry: "intake",
    terminalNodeIds: ["delivery"],
    includedNodeIds: ["intake", "review", "delivery"],
    excludedNodes: [{ id: "archive", reason: "past_terminal" }],
    unresolvedInputs: [{ code: "missing_input", node: "review", input: "request", source: "run_input" }],
  });
  assert.notEqual(impact.terminalNodeIds, route.terminals);
  assert.notEqual(impact.excludedNodes[0], route.excludedNodes[0]);
  assert.notEqual(impact.unresolvedInputs[0], route.inputRequirements[0]);
  assert.ok(!("transitions" in impact), "the summary must not synthesize a replacement route");
});

test("workflow version ordering is deterministic and leaves the installed projection untouched", () => {
  const values = Object.freeze([
    Object.freeze({ name: "beta", version: "1.0.0", digest: "sha256:b" }),
    Object.freeze({ name: "alpha", version: "1.0.0", digest: "sha256:c" }),
    Object.freeze({ name: "alpha", version: "2.0.0", digest: "sha256:a" }),
  ]);

  assert.deepEqual(sortWorkflowVersions(values).map(({ name, version }) => `${name}@${version}`), [
    "alpha@2.0.0",
    "alpha@1.0.0",
    "beta@1.0.0",
  ]);
  assert.deepEqual(values.map(({ name, version }) => `${name}@${version}`), [
    "beta@1.0.0",
    "alpha@1.0.0",
    "alpha@2.0.0",
  ]);
});

test("route preview response exposes closed typed collections rather than an opaque route", async () => {
  const openapi = JSON.parse(await readFile(new URL("../../schemas/openapi-v1alpha1.json", import.meta.url), "utf8"));
  const schemas = openapi.components.schemas;

  assert.equal(schemas.WorkflowRoutePreview.properties.route.$ref, "#/components/schemas/FrozenRoute");
  assert.equal(schemas.FrozenRoute.additionalProperties, false);
  assert.deepEqual(schemas.FrozenRoute.required, [
    "entry",
    "terminals",
    "nodes",
    "transitions",
    "excludedNodes",
    "inputRequirements",
  ]);
  assert.deepEqual(schemas.FrozenRoute.properties.terminals.items, { type: "string" });

  const collections = {
    nodes: "FrozenRouteNode",
    transitions: "FrozenRouteTransition",
    excludedNodes: "FrozenRouteExcludedNode",
    inputRequirements: "FrozenRouteInputRequirement",
  };
  for (const [field, schemaName] of Object.entries(collections)) {
    assert.deepEqual(schemas.FrozenRoute.properties[field], {
      type: "array",
      items: { $ref: `#/components/schemas/${schemaName}` },
    });
    assert.equal(schemas[schemaName].additionalProperties, false, `${schemaName} must be closed`);
  }
  assert.deepEqual(schemas.FrozenRouteNode.required, ["id"]);
  assert.deepEqual(schemas.FrozenRouteTransition.required, ["id", "from", "to"]);
  assert.deepEqual(schemas.FrozenRouteExcludedNode.required, ["id", "reason"]);
  assert.deepEqual(schemas.FrozenRouteExcludedNode.properties.reason.enum, [
    "before_entry",
    "past_terminal",
    "not_connected",
  ]);
  assert.deepEqual(schemas.FrozenRouteInputRequirement.required, ["code", "node", "input", "source"]);
});

test("workflow route preview is a read-only operation with no hidden run command or reroute", async () => {
  const openapi = JSON.parse(await readFile(new URL("../../schemas/openapi-v1alpha1.json", import.meta.url), "utf8"));
  const preview = openapi.paths["/api/v1/workflows/preview"].post;

  assert.equal(preview.operationId, "previewWorkflowRoute");
  assert.equal(preview.requestBody.content["application/json"].schema.$ref, "#/components/schemas/WorkflowPreviewRequest");
  assert.deepEqual(preview.parameters, [
    { $ref: "#/components/parameters/WorkflowName" },
    { $ref: "#/components/parameters/OptionalWorkflowVersion" },
  ]);

  const client = await readFile(new URL("../src/api/client.ts", import.meta.url), "utf8");
  const previewMethod = client.slice(
    client.indexOf("previewWorkflowRoute("),
    client.indexOf("createWorkItem("),
  );
  assert.match(previewMethod, /this\.operation\("previewWorkflowRoute", \{ query: \{ name, version \}, body, signal \}\)/);
  assert.doesNotMatch(previewMethod, /continueRun|createRun|pauseRun|resumeRun|retryRun|cancelRun/);
  assert.doesNotMatch(previewMethod, /idempotencyKey|resourceVersion|If-Match/);
});

function collectKeys(value, result = new Set()) {
  if (Array.isArray(value)) {
    for (const item of value) collectKeys(item, result);
    return result;
  }
  if (value && typeof value === "object") {
    for (const [key, item] of Object.entries(value)) {
      result.add(key);
      collectKeys(item, result);
    }
  }
  return result;
}
