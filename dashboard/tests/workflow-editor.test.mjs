import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

import {
  addNode, connectNodes, createStarterDocument, deriveEditorGraph, edgeIdentity, moveNode, removeNode, updateNode,
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

test("removing a node clears incoming transitions and joins that referenced its outgoing transition", () => {
  let document = createStarterDocument("example/editor");
  document = addNode(document, "command", "middle").document;
  document = addNode(document, "approval", "finish").document;
  document = connectNodes(document, "start", "middle");
  document = connectNodes(document, "middle", "finish");
  document.spec.nodes.finish.join = { mode: "one", from: ["middle_to_finish", "external_transition"] };
  const removed = removeNode(document, "middle");
  assert.equal(removed.spec.nodes.middle, undefined);
  assert.deepEqual(removed.spec.nodes.start.transitions, []);
  assert.deepEqual(removed.spec.nodes.finish.join.from, ["external_transition"]);
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

test("editor source exposes direct URL state, keyboard parity, conflict retention, and mobile outline", async () => {
  const [page, styles, client] = await Promise.all([
    readFile(new URL("../src/pages/WorkflowsPage.tsx", import.meta.url), "utf8"),
    readFile(new URL("../src/styles.css", import.meta.url), "utf8"),
    readFile(new URL("../src/api/client.ts", import.meta.url), "utf8"),
  ]);
  for (const parameter of ["item", "view", "selection"]) assert.match(page, new RegExp(`params\\.get\\("${parameter}"\\)`));
  for (const affordance of ["Duplicate as draft", "Archive", "Start connection", "Move ${node.id} earlier", "Remove", "aria-live=\"polite\"", "event.key === \"Escape\""]) assert.ok(page.includes(affordance), `missing ${affordance}`);
  assert.match(page, /persistence\.remote/);
  assert.match(styles, /@media \(max-width: 820px\)[^{]*\{[^}]*\.workflow-editor-shell/s);
  assert.match(styles, /\.workflow-view-tabs button:first-child \{ display: none; \}/);
  for (const method of ["getWorkflowLibrary", "createWorkflowDraft", "duplicateWorkflowDraft", "updateWorkflowDraft", "validateWorkflowDraft", "archiveWorkflowVersion"]) assert.match(client, new RegExp(`${method}\\(`));
});
