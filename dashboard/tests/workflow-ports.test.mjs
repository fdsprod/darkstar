import assert from "node:assert/strict";
import test from "node:test";
import { addNode, connectNodes, createStarterDocument, deriveEditorGraph, normalizeLayout, renameNode } from "../src/pages/workflowEditorModel.ts";
import { autoLayoutGraph, bindPorts, connectionError, derivePortGraph, graphBounds, portKey, preparePortLayout } from "../src/pages/workflowPortModel.ts";

function fixture() {
  let document = addNode(createStarterDocument("example/ports"), "command", "finish").document;
  document.spec.inputs = { task: { type: "string" } };
  document.spec.nodes.start.outputs = { answer: { type: "integer" }, report: { type: "object" } };
  document.spec.nodes.finish.inputs = { answer: { type: "number", from: "node.start.output.answer", required: true }, task: { type: "string", from: "run.input.task" } };
  document = connectNodes(document, "start", "finish");
  return document;
}
const project = (document, layout = {}) => { const graph = deriveEditorGraph(document, layout); return { graph, ...derivePortGraph(document, graph) }; };

test("typed projection separates execution, node outputs and run-input bindings using stable IDs", () => {
  const document = fixture(), before = structuredClone(document), result = project(document);
  assert.deepEqual(result.edges.map((edge) => edge.kind), ["execution", "data", "data"]);
  assert.equal(result.edges[1].source.portId, "answer");
  assert.equal(result.edges[2].source.kind, "run_input");
  assert.deepEqual(result.findings, []);
  assert.deepEqual(document, before);
  const moved = project(document, { nodes: { start: { x: 900, y: 500 } } });
  assert.deepEqual(result.edges, moved.edges);
  assert.deepEqual(result.ports.map(portKey), moved.ports.map(portKey));
});

test("incompatible ports fail with exact daemon-compatible message and do not change document", () => {
  const document = fixture(), original = structuredClone(document), { ports } = project(document);
  const source = ports.find((port) => port.portId === "report");
  const target = ports.find((port) => port.portId === "answer" && port.direction === "input");
  assert.deepEqual(bindPorts(document, source, target, ports), { kind: "invalid", message: 'binding "answer" expects number from object' });
  assert.deepEqual(document, original);
  assert.equal(connectionError(ports.find((port) => port.portId === "complete"), target), "Execution ports cannot connect to data ports.");
  assert.equal(connectionError(target, source), "Connections must run from an output port to an input port.");
});

test("connection reloads current port types instead of trusting a stale selection", () => {
  const document = fixture(), old = project(document);
  const source = old.ports.find((port) => port.portId === "answer" && port.direction === "output"), target = old.ports.find((port) => port.portId === "answer" && port.direction === "input");
  document.spec.nodes.start.outputs.answer.type = "string";
  assert.equal(bindPorts(document, source, target, project(document).ports).kind, "invalid");
  delete document.spec.nodes.start.outputs.answer;
  assert.match(bindPorts(document, source, target, project(document).ports).message, /no longer exists/);
});

test("reconnecting a whole value preserves optional policy and unknown fields while clearing old pointer", () => {
  const document = fixture();
  document.spec.nodes.finish.inputs.answer = { from: "node.start.output.report", pointer: "/score", type: "number", required: false, default: 0, description: "Score", futureBinding: "preserved" };
  const { ports } = project(document);
  const result = bindPorts(document, ports.find((port) => port.portId === "answer" && port.direction === "output"), ports.find((port) => port.portId === "answer" && port.direction === "input"), ports);
  assert.equal(result.kind, "changed");
  assert.deepEqual(result.document.spec.nodes.finish.inputs.answer, { from: "node.start.output.answer", type: "number", required: false, default: 0, description: "Score", futureBinding: "preserved" });
  assert.equal(document.spec.nodes.finish.inputs.answer.pointer, "/score");
  assert.deepEqual(project(result.document).findings, []);
  assert.equal(project(JSON.parse(JSON.stringify(result.document))).edges[1].source.portId, "answer");
});

test("incomplete and incompatible draft bindings are navigable before server validation", () => {
  const document = fixture(); document.spec.nodes.finish.inputs.answer.from = "node.start.output.missing";
  document.spec.nodes.finish.inputs.task.type = "object";
  const { findings } = project(document);
  assert.deepEqual(findings.map((finding) => [finding.nodeId, finding.field, finding.code]), [["finish", "inputs.answer.from", "WF_REFERENCE_MISSING"], ["finish", "inputs.task", "WF_BINDING_INCOMPATIBLE"]]);
  document.spec.nodes.finish.inputs.answer.from = "node.start.output.report";
  document.spec.nodes.finish.inputs.answer.pointer = "/score";
  assert.equal(project(document).findings.length, 1, "selectors are checked by runtime values, not the container type");
});

test("malformed unknown port types never pass compatibility by equality", () => {
  const document = fixture(); delete document.spec.nodes.start.outputs.answer.type; delete document.spec.nodes.finish.inputs.answer.type;
  assert.equal(project(document).findings[0].code, "WF_BINDING_INCOMPATIBLE");
});

test("auto-layout handles cycles and disconnected nodes without touching execution semantics", () => {
  let document = addNode(fixture(), "command", "isolated").document;
  document = connectNodes(document, "finish", "start", "bounded_repair");
  const before = JSON.stringify(document), { graph, ports } = project(document), layout = normalizeLayout({ futureLayout: "kept" });
  const next = autoLayoutGraph(layout, graph, ports);
  assert.equal(next.futureLayout, "kept"); assert.equal(Object.keys(next.nodes).length, 3);
  assert.deepEqual(next, autoLayoutGraph(layout, graph, ports));
  assert.equal(JSON.stringify(document), before);
  assert.deepEqual(project(document, next).edges, project(document).edges);
  const bounds = graphBounds(deriveEditorGraph(document, next), ports);
  assert.ok(bounds.x < 0, "run inputs have room to the left of nodes");
  assert.ok(bounds.width >= 720 && bounds.height >= 440, "canvas bounds reserve usable graph extents");
  const start = next.nodes.start, finish = next.nodes.finish, isolated = next.nodes.isolated;
  assert.ok(Math.abs(start.x - finish.x) >= 460, "connected layers have enough horizontal clearance for nodes and edges");
  assert.ok(Math.abs(start.y - isolated.y) >= 110, "nodes sharing a layer receive content-aware vertical clearance");
  assert.equal(next.portLayoutVersion, 4);
  assert.equal(next.viewport.zoom, 0.85, "ordinary auto-layout keeps labels readable instead of fitting the whole graph");
  assert.deepEqual(preparePortLayout(next, graph, ports), next, "current layout coordinates remain authoritative");
  assert.equal(preparePortLayout({ ...next, portLayoutVersion: 3 }, graph, ports).portLayoutVersion, 4, "legacy card coordinates migrate deterministically");
});

test("authoritative node rename rewrites bindings and therefore both graph views", () => {
  const renamed = renameNode(fixture(), {}, "start", "producer");
  assert.equal(renamed.preview.kind, "ready");
  const projection = project(renamed.document, renamed.layout);
  assert.equal(projection.edges[1].source.nodeId, "producer");
  assert.equal(projection.edges[0].source.nodeId, "producer");
  assert.deepEqual(projection.findings, []);
});
