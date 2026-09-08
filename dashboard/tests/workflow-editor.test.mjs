import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

import {
  addNode, connectNodes, createStarterDocument, decodePredicate, deriveEditorGraph, edgeIdentity, encodePredicate, findingTarget, inspectNode, inspectTransition, moveNode, nodeExecutorComplete, nodeTypeAvailability, previewNodeRemoval, previewNodeRename, removeNode, removeNodeLayout, renameNode, updateNode, updateNodeCheckpoint, updateNodeExecutor, updateNodeShared, updateTransition, useNodeDefinition, validationMatches,
} from "../src/pages/workflowEditorModel.ts";

test("layout is a separate projection and cannot change semantic document bytes", () => {
  const document = createStarterDocument("example/editor");
  const before = JSON.stringify(document);
  const layout = moveNode({ futureLayoutField: { retained: true } }, "start", { x: 405.4, y: 207.7 });
  assert.equal(JSON.stringify(document), before);
  assert.deepEqual(layout.nodes.start, { x: 405, y: 208 });
  assert.deepEqual(layout.futureLayoutField, { retained: true });
  assert.deepEqual(deriveEditorGraph(document, layout).nodes[0].position, { x: 405, y: 208 });
});

test("graph derivation uses stable composite identities and closed edge kinds", () => {
  let document = createStarterDocument("example/editor");
  const added = addNode(document, "command", "finish"); document = added.document;
  document = addNode(document, "subworkflow", "child_flow").document;
  document = connectNodes(document, "start", "finish", "conditional");
  document = connectNodes(document, "finish", "start", "bounded_repair");
  document = connectNodes(document, "child_flow", "finish");
  const graph = deriveEditorGraph(document, {});
  assert.equal(graph.edges[0].id, edgeIdentity("start", "start_to_finish", "finish"));
  assert.deepEqual(graph.edges.map((edge) => edge.kind), ["conditional", "bounded_repair", "subworkflow"]);
});

test("structural edits preserve unknown authoring fields", () => {
  const original = createStarterDocument("example/editor");
  original.futureRoot = { retained: true };
  original.spec.futureSpec = "kept";
  original.spec.nodes.start.futureNode = [1, 2, 3];
  const added = addNode(original, "approval", "review").document;
  const updated = updateNode(added, "start", { displayName: "Start here" });
  assert.deepEqual(updated.futureRoot, { retained: true });
  assert.equal(updated.spec.futureSpec, "kept");
  assert.deepEqual(updated.spec.nodes.start.futureNode, [1, 2, 3]);
});

test("node removal blocks authoritative references before deleting incident transitions", () => {
  let document = createStarterDocument("example/editor");
  document = addNode(document, "command", "middle").document;
  document = addNode(document, "approval", "finish").document;
  document = connectNodes(document, "start", "middle");
  document = connectNodes(document, "middle", "finish");
  document.spec.nodes.finish.join = { mode: "one", from: ["middle_to_finish", "external_transition"] };
  assert.equal(previewNodeRemoval(document, "middle").kind, "blocked");
  assert.deepEqual(removeNode(document, "middle"), document);
  delete document.spec.nodes.finish.join;
  const removed = removeNode(document, "middle");
  assert.equal(removed.spec.nodes.middle, undefined);
  assert.deepEqual(removed.spec.nodes.start.transitions, []);
  assert.equal(removed.spec.nodes.finish.join, undefined);
  assert.equal(removeNodeLayout({ version: 1, nodes: { middle: { x: 1, y: 2 }, finish: { x: 3, y: 4 } } }, "middle").nodes.middle, undefined);
});

test("node removal preview reports route, profile, binding, and readiness references", () => {
  let document = addNode(createStarterDocument("example/editor"), "command", "middle").document;
  document.spec.profiles = { alternate: { entry: "middle", terminals: ["middle"], inputDefaults: {} } };
  document.spec.nodes.start.inputs = { result: { from: "node.middle.output.result", type: "object", required: true } };
  document.spec.nodes.start.readiness = { recommendedEvidence: [], policyGates: [], invariants: [], remedies: [{ code: "retry", target: "middle", action: "rerun_validation", description: "Retry" }] };
  const preview = previewNodeRemoval(document, "middle");
  assert.equal(preview.kind, "blocked");
  assert.deepEqual(preview.references, ["spec.nodes.start.inputs", "spec.nodes.start.readiness.remedies", "spec.profiles.alternate.entry", "spec.profiles.alternate.terminals"]);
});

test("entry, terminal, route defaults, and checkpoint remain synchronized", () => {
  let document = createStarterDocument("example/editor");
  document = addNode(document, "approval", "finish").document;
  document = updateNode(document, "finish", { entry: true, terminal: true, checkpoint: "approve" });
  assert.equal(document.spec.routeDefaults.entry, "finish");
  assert.deepEqual(document.spec.routeDefaults.terminals, ["start", "finish"]);
  assert.deepEqual(document.spec.nodes.finish.checkpoint, { mode: "approve" });
  document = updateNode(document, "start", { terminal: false });
  assert.deepEqual(document.spec.routeDefaults.terminals, ["finish"]);
  document = updateNode(document, "finish", { entry: false, terminal: false });
  assert.equal(document.spec.routeDefaults.entry, "start");
  assert.equal(document.spec.nodes.start.entry, true);
  assert.deepEqual(document.spec.routeDefaults.terminals, ["start"]);
  assert.equal(document.spec.nodes.start.terminal, true);
});

test("all seven executor variants decode and update through a closed typed model", () => {
  let document = createStarterDocument("example/editor");
  for (const type of ["reasoning", "gate", "approval", "subworkflow", "point_execution", "routing"]) document = addNode(document, type, type).document;
  assert.deepEqual(Object.values(document.spec.nodes).map((_, index) => inspectNode(document, Object.keys(document.spec.nodes)[index]).executor.type), ["command", "reasoning", "gate", "approval", "subworkflow", "point_execution", "routing"]);
  document = updateNodeExecutor(document, "gate", { type: "gate", policy: "release_policy", condition: { kind: "comparison", operator: "eq", left: { kind: "reference", ref: "input.ready" }, right: { kind: "literal", value: true } } });
  document = updateNodeExecutor(document, "start", { type: "command", argv: ["go", "test", "./..."], cwd: "runtime", timeoutSeconds: 120 });
  document = updateNodeExecutor(document, "approval", { type: "approval", actor: "external", externalCondition: "checks/pass", evidenceOutput: "approval_evidence" });
  document = updateNodeExecutor(document, "subworkflow", { type: "subworkflow", workflow: { name: "child/workflow", version: "2.1.0", digest: "a".repeat(64) }, entry: "start", terminals: ["finish"], inputs: { request: "request" }, outputs: { result: "node.finish.output.result" } });
  document.spec.nodes.point_execution.inputs.implementation_plan = { from: "run.input.implementation_plan", type: "object", required: true };
  document = updateNodeExecutor(document, "point_execution", { type: "point_execution", planInput: "implementation_plan", approval: "risk", riskTags: ["database"], validation: "each_and_combined", publishing: "after_each_point" });
  document = updateNodeExecutor(document, "routing", { ...inspectNode(document, "routing").executor, branches: [{ name: "review", transition: "to_review" }] });
  assert.equal(document.spec.nodes.gate.gate.condition.op, "eq");
  assert.deepEqual(document.spec.nodes.start.command.argv, ["go", "test", "./..."]);
  assert.equal(document.spec.nodes.approval.outputs.approval_evidence.schema, "darkstar/approval-evidence/v1");
  assert.equal(document.spec.nodes.subworkflow.call.workflow.digest, "a".repeat(64));
  assert.equal(document.spec.nodes.point_execution.inputs.implementation_plan.type, "object");
  assert.deepEqual(document.spec.nodes.routing.routing.branches, [{ name: "review", transition: "to_review" }]);
  assert.equal(document.apiVersion, "darkstar.local/v1alpha3");
});

test("using a reusable node definition pins its exact immutable version", () => {
  const definition = { scope: "project", owner: "project-1", name: "nodes/review", version: "2.1.0", digest: "a".repeat(64) };
  const document = useNodeDefinition(createStarterDocument("example/editor"), "start", definition);
  assert.equal(document.apiVersion, "darkstar.local/v1alpha3");
  assert.deepEqual(document.spec.nodes.start.definition, definition);
  definition.version = "9.9.9";
  assert.equal(document.spec.nodes.start.definition.version, "2.1.0");
});

test("recursive predicates round trip and malformed conditions remain explicit and lossless", () => {
  const predicate = { kind: "group", operator: "all", predicates: [{ kind: "present", ref: "input.request" }, { kind: "not", predicate: { kind: "comparison", operator: "gte", left: { kind: "reference", ref: "run.input.count" }, right: { kind: "literal", value: 2 } } }] };
  assert.deepEqual(decodePredicate(encodePredicate(predicate)), predicate);
  const malformed = { op: "future", args: [{ ref: "input.value" }], retained: true };
  const decoded = decodePredicate(malformed);
  assert.equal(decoded.kind, "unsupported");
  assert.deepEqual(encodePredicate(decoded), malformed);
  let document = addNode(createStarterDocument("example/editor"), "gate", "gate").document;
  document.spec.nodes.gate.gate.condition = malformed;
  assert.equal(inspectNode(document, "gate"), undefined);
  assert.deepEqual(document.spec.nodes.gate.gate.condition, malformed);
});

test("checkpoint, shared contract, and transition setters enforce closed invariants", () => {
  let document = addNode(createStarterDocument("example/editor"), "gate", "gate").document;
  document = updateNodeCheckpoint(document, "gate", { mode: "approve_on_change", maxRevisions: 999, when: { kind: "constant", value: false } });
  assert.equal(document.spec.nodes.gate.checkpoint.maxRevisions, 100);
  const beforeInputs = document.spec.nodes.gate.inputs;
  document = updateNodeShared(document, "gate", { kind: "inputs", value: [{ id: "duplicate", from: "run.input.one", type: "string", required: true }, { id: "duplicate", from: "run.input.two", type: "string", required: true }] });
  assert.deepEqual(document.spec.nodes.gate.inputs, beforeInputs);
  document = updateNodeShared(document, "gate", { kind: "outputs", value: [{ id: "result", type: "object", schema: "schema/result", required: true }] });
  document = updateNodeShared(document, "gate", { kind: "validators", value: [{ kind: "schema", output: "missing", schema: "schema/result" }] });
  assert.deepEqual(document.spec.nodes.gate.validators ?? [], []);
  document = updateNodeShared(document, "gate", { kind: "join", value: { mode: "all", from: ["only_one"] } });
  assert.equal(document.spec.nodes.gate.join, undefined);
  document = connectNodes(document, "start", "gate", "bounded_repair");
  const edge = deriveEditorGraph(document, {}).edges[0];
  let updated = updateTransition(document, edge.id, { maxTraversals: 99_999, when: { kind: "present", ref: "input.ready" }, enabledByDefault: false });
  assert.equal(inspectTransition(updated.document, updated.edgeId).maxTraversals, 10_000);
  assert.equal(inspectTransition(updated.document, updated.edgeId).enabledByDefault, false);
});

test("omitted binding defaults canonically and malformed or unknown shared fields fail loud", () => {
  const document = createStarterDocument("example/editor");
  document.spec.nodes.start.inputs = { request: { from: "run.input.request", type: "object" } };
  document.spec.nodes.start.outputs = { result: { type: "object", required: true } };
  document.spec.nodes.start.validators = [{ output: "result", schema: "schema/result" }];
  document.spec.nodes.start.readiness = { recommendedEvidence: [{ role: "request", description: "Request" }], policyGates: [], invariants: [], remedies: [] };
  const inspected = inspectNode(document, "start");
  assert.equal(inspected.inputs[0].required, true);
  document.spec.nodes.start.inputs.request.futureBinding = true;
  assert.equal(inspectNode(document, "start"), undefined);
  delete document.spec.nodes.start.inputs.request.futureBinding;
  document.spec.nodes.start.readiness.policyGates = [{ policy: "policy", enforcement: "future", description: "Unknown" }];
  assert.equal(inspectNode(document, "start"), undefined);
});

test("malformed executor, checkpoint, and validator variants fail loud", () => {
  const cases = [
    (document) => { document.spec.nodes.start.reasoning = { agent: 12 }; },
    (document) => { document.spec.nodes.start.checkpoint = { mode: "future" }; },
    (document) => { document.spec.nodes.start.validators = [{ command: ["test"], output: "result", schema: "schema/result" }]; },
    (document) => { document.spec.nodes.start.validators = [{ future: true }]; },
  ];
  for (const mutate of cases) { const document = createStarterDocument("example/editor"); mutate(document); assert.equal(inspectNode(document, "start"), undefined); }
});

test("catalog availability enables only authoritative reference-backed node creation", () => {
  const unavailable = { status: "unavailable", reason: "not_configured" };
  const catalog = { agents: unavailable, policies: unavailable, workflows: { status: "known", items: [{ name: "child", version: "1.0.0", digest: "a".repeat(64) }] } };
  assert.equal(nodeTypeAvailability("reasoning", catalog).available, false);
  assert.equal(nodeTypeAvailability("gate", catalog).available, false);
  assert.equal(nodeTypeAvailability("subworkflow", catalog).available, true);
  assert.equal(nodeTypeAvailability("command", catalog).available, true);
});

test("binding syntax and point selected-input invariants reject invalid candidate nodes", () => {
  let document = addNode(createStarterDocument("example/editor"), "point_execution", "points").document;
  document.spec.nodes.points.inputs.plan.from = "not-a-binding";
  assert.equal(inspectNode(document, "points"), undefined);
  document.spec.nodes.points.inputs.plan.from = "run.input.plan";
  document.spec.nodes.points.inputs.plan.pointer = "/bad~2escape";
  assert.equal(inspectNode(document, "points"), undefined);
  delete document.spec.nodes.points.inputs.plan.pointer;
  const inspected = inspectNode(document, "points");
  assert.ok(inspected);
  const before = JSON.stringify(document);
  const invalidChanges = [
    { kind: "inputs", value: [] },
    { kind: "inputs", value: inspected.inputs.map((input) => input.id === inspected.executor.planInput ? { ...input, id: "renamed_plan" } : input) },
    { kind: "inputs", value: inspected.inputs.map((input) => input.id === inspected.executor.planInput ? { ...input, type: "string" } : input) },
  ];
  for (const change of invalidChanges) {
    const result = updateNodeShared(document, "points", change);
    assert.strictEqual(result, document);
    assert.equal(JSON.stringify(result), before);
    assert.ok(inspectNode(result, "points"));
  }
});

test("subworkflow executor completeness rejects malformed exact route and mappings", () => {
  const valid = { type: "subworkflow", workflow: { name: "team/child", version: "1.2.3", digest: "a".repeat(64) }, entry: "start", terminals: ["finish"], inputs: { request: "request" }, outputs: { result: "node.finish.output.result" } };
  assert.equal(nodeExecutorComplete(valid), true);
  for (const invalid of [
    { ...valid, terminals: ["bad-id"] },
    { ...valid, entry: "bad-id" },
    { ...valid, workflow: { ...valid.workflow, name: "Bad workflow" } },
    { ...valid, workflow: { ...valid.workflow, version: "latest" } },
    { ...valid, inputs: { "bad-id": "request" } },
    { ...valid, outputs: { result: "bad.output.source" } },
  ]) assert.equal(nodeExecutorComplete(invalid), false);
});

test("point plan input selects an existing object binding without inferred migration", () => {
  let document = createStarterDocument("example/editor");
  document.spec.inputs = { plan: { type: "string" } };
  document = addNode(document, "point_execution", "points").document;
  const point = inspectNode(document, "points");
  assert.equal(point.executor.planInput, "plan_2");
  const collided = updateNodeExecutor(document, "points", { ...point.executor, planInput: "plan" });
  assert.equal(inspectNode(collided, "points").executor.planInput, "plan_2");
  document.spec.nodes.points.inputs.implementation_plan = { from: "run.input.implementation_plan", type: "object", required: true };
  const migrated = updateNodeExecutor(document, "points", { ...point.executor, planInput: "implementation_plan" });
  assert.ok(migrated.spec.nodes.points.inputs.plan_2);
  assert.ok(migrated.spec.inputs.plan_2);
  assert.equal(migrated.spec.nodes.points.points.planInput, "implementation_plan");
});

test("transition IDs are global and join references are repaired on remove and retarget", () => {
  let document = createStarterDocument("example/editor");
  document = addNode(document, "command", "left").document;
  document = addNode(document, "command", "right").document;
  document.spec.nodes.left.transitions = [{ id: "start_to_right", to: "right" }];
  document = connectNodes(document, "start", "right");
  assert.deepEqual(document.spec.nodes.start.transitions.map((item) => item.id), ["start_to_right_2"]);
  document.spec.nodes.right.join = { mode: "all", from: ["start_to_right", "start_to_right_2"] };
  const edge = edgeIdentity("start", "start_to_right_2", "right");
  const retargeted = updateTransition(document, edge, { to: "left" });
  assert.equal(retargeted.document.spec.nodes.right.join, undefined);
  delete document.spec.nodes.right.join;
  const removed = removeNode(document, "left");
  assert.equal(removed.spec.nodes.right.join, undefined);
});

test("malformed and ambiguous transitions fail loud instead of coercing an editable edge", () => {
  let document = addNode(createStarterDocument("example/editor"), "command", "finish").document;
  document.spec.nodes.start.transitions = [{ id: "repair", to: "finish", kind: "bounded", maxTraversals: 0 }];
  assert.equal(inspectTransition(document, edgeIdentity("start", "repair", "finish")), undefined);
  document.spec.nodes.start.transitions = [{ id: "repair", to: "finish" }, { id: "repair", to: "finish" }];
  assert.equal(inspectTransition(document, edgeIdentity("start", "repair", "finish")), undefined);
});

test("atomic node rename rewrites semantic references and layout or changes nothing", () => {
  let document = addNode(createStarterDocument("example/editor"), "command", "middle").document;
  document = connectNodes(document, "start", "middle");
  document.spec.nodes.start.inputs = { child: { from: "node.middle.output.value", type: "object" } };
  document.spec.nodes.start.readiness = { recommendedEvidence: [], policyGates: [], invariants: [], remedies: [{ code: "retry", target: "middle", action: "rerun_validation", description: "Retry" }] };
  document.spec.profiles = { review: { description: "Review", entry: "middle", terminals: ["middle"], inputDefaults: {} } };
  const invalid = renameNode(document, { version: 1, nodes: { middle: { x: 10, y: 20 } } }, "middle", "Invalid ID");
  assert.equal(invalid.preview.kind, "invalid");
  assert.ok(document.spec.nodes.middle);
  const renamed = renameNode(document, { version: 1, nodes: { middle: { x: 10, y: 20 } } }, "middle", "execute");
  assert.equal(previewNodeRename(document, "middle", "execute", { version: 1, nodes: { middle: { x: 10, y: 20 } } }).references, 7);
  assert.equal(previewNodeRename(document, "middle", "execute").kind, "ready");
  assert.equal(renamed.document.spec.nodes.start.transitions[0].to, "execute");
  assert.equal(renamed.document.spec.nodes.start.inputs.child.from, "node.execute.output.value");
  assert.equal(renamed.document.spec.nodes.start.readiness.remedies[0].target, "execute");
  assert.deepEqual(renamed.layout.nodes.execute, { x: 10, y: 20 });
});

test("validation evidence survives layout-only CAS revisions but not semantic document changes", () => {
  const state = { kind: "valid", draftId: "draft_one", revision: 2, documentDigest: "a".repeat(64), semanticDigest: "b".repeat(64) };
  assert.equal(validationMatches(state, { draftId: "draft_one", revision: 3, documentDigest: "a".repeat(64) }), true);
  assert.equal(validationMatches(state, { draftId: "draft_one", revision: 3, documentDigest: "c".repeat(64) }), false);
});

test("server transition array findings map to stable composite editor edges", () => {
  let document = addNode(createStarterDocument("example/editor"), "command", "finish").document;
  document = connectNodes(document, "start", "finish", "conditional");
  const target = findingTarget(document, { code: "WF_REFERENCE_MISSING", severity: "error", message: "bad", nodeId: "start", edgeId: "start_to_finish", field: "transitions.0.when" });
  assert.deepEqual(target.selection, { kind: "edge", edgeId: edgeIdentity("start", "start_to_finish", "finish") });
  document.spec.nodes.start.transitions.push(structuredClone(document.spec.nodes.start.transitions[0]));
  assert.deepEqual(findingTarget(document, { code: "WF_SCHEMA_INVALID", severity: "error", message: "ambiguous", nodeId: "start", edgeId: "start_to_finish", field: "transitions.1.to" }).selection, { kind: "node", nodeId: "start" });
});

test("editor source exposes direct URL state, keyboard parity, conflict retention, and mobile outline", async () => {
  const [page, graphCanvas, inspector, styles, client, manifest] = await Promise.all([
    readFile(new URL("../src/pages/WorkflowsPage.tsx", import.meta.url), "utf8"),
    readFile(new URL("../src/pages/WorkflowPortGraph.tsx", import.meta.url), "utf8"),
    readFile(new URL("../src/pages/WorkflowAuthoringInspector.tsx", import.meta.url), "utf8"),
    readFile(new URL("../src/styles.css", import.meta.url), "utf8"),
    readFile(new URL("../src/api/client.ts", import.meta.url), "utf8"),
    readFile(new URL("../package.json", import.meta.url), "utf8"),
  ]);
  for (const parameter of ["item", "view", "selection"]) assert.match(page, new RegExp(`params\\.get\\("${parameter}"\\)`));
  const source = `${page}\n${inspector}`;
  for (const affordance of ["Duplicate as draft", "Archive", "Start connection", "Move ${node.id} earlier", "Remove", "aria-live=\"polite\"", "event.key === \"Escape\"", "Preview route", "Validate draft", "Advanced JSON", "Publish exact revision"]) assert.ok(source.includes(affordance), `missing ${affordance}`);
  for (const definitionControl of ["Reusable node definitions", "Create from selected node", "Use", "New version", "Built-in definitions are immutable"]) assert.ok(page.includes(definitionControl), `missing ${definitionControl}`);
  assert.match(page, /persistence\.remote/);
  for (const fence of ["semanticGenerationRef", "publishRequestRef", "currentDocumentRef", "result.published.sourceReference", "child.inert = true"]) assert.ok(page.includes(fence), `missing ${fence}`);
  for (const buffered of ["Apply mappings", "Apply node configuration", "onBlur={commit}", "Existing draft values remain visible and unchanged", "Configured reference unavailable"]) assert.ok(inspector.includes(buffered), `missing ${buffered}`);
  for (const exactField of ["outputs.${value.id}.${name}", "readiness.recommendedEvidence.${index}.role", "readiness.recommendedEvidence.${index}.description", "readiness.policyGates.${index}.policy", "readiness.remedies.${index}.target"]) assert.ok(inspector.includes(exactField), `missing exact field address ${exactField}`);
  assert.match(inspector, /sameKeys\(value\.inputs/);
  assert.match(inspector, /node\.inputs\.some\(\(input\) => input\.id === value\.planInput && input\.type === "object"\)/);
  assert.match(page, /result\.sourceValidationDigest !== request\.semanticDigest/);
  assert.doesNotMatch(page, /endsWith\(field\.dataset\.workflowField/);
  assert.equal(JSON.parse(manifest).dependencies["@xyflow/react"], "^12.11.6");
  for (const integration of ["ReactFlow", "ReactFlowProvider", "Handle", "MiniMap", "isValidConnection={validConnection}", "screenToFlowPosition", "onNodeDragStop"]) assert.ok(graphCanvas.includes(integration), `missing React Flow integration ${integration}`);
  assert.doesNotMatch(graphCanvas, /<svg\b/);
  assert.match(page, /draggable=\{availability\.available\}/);
  assert.match(styles, /@media \(max-width: 820px\)[^{]*\{[^}]*\.workflow-editor-shell/s);
  assert.match(styles, /\.workflow-view-tabs button:first-child \{ display: none; \}/);
  assert.doesNotMatch(styles, /@media \(max-width: 820px\)[^{]*\{[^}]*\.node-palette \{ display: none; \}/s);
  for (const method of ["getWorkflowLibrary", "getWorkflowAuthoringCatalog", "createWorkflowDraft", "duplicateWorkflowDraft", "updateWorkflowDraft", "previewWorkflowDraft", "validateWorkflowDraft", "publishWorkflowDraft", "archiveWorkflowVersion"]) assert.match(client, new RegExp(`${method}\\(`));
  for (const method of ["listNodeDefinitions", "createNodeDefinition", "duplicateNodeDefinition", "versionNodeDefinition", "archiveNodeDefinition"]) assert.match(client, new RegExp(`${method}\\(`));
});
