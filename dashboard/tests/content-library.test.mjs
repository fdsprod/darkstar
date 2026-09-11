import test from "node:test";
import assert from "node:assert/strict";
import { contentReference, filterContent, sameReference, documentText } from "../src/pages/contentLibraryModel.ts";
import { linkOutputTemplate, setNodePrompt, setOptionalContext } from "../src/pages/workflowContentModel.ts";
import { inspectNode } from "../src/pages/workflowEditorModel.ts";

const ref = { id: "design", version: "1.0.0", digest: "a".repeat(64) };
const document = {
  apiVersion: "darkstar.local/v1alpha3",
  spec: {
    inputs: { backlog: { type: "open_items", resource: { kind: "open_items" } } },
    nodes: {
      design: {
        type: "reasoning", entry: true, terminal: true, checkpoint: { mode: "approve" },
        inputs: { task: { type: "task", from: "run.input.task" } },
        outputs: { product: { type: "markdown", artifact: { filename: "design.md" } }, technical: { type: "markdown", artifact: { filename: "technical.md" } } },
      },
    },
  },
};

test("published content references compare the digest as well as identity and version", () => {
  assert.equal(sameReference(ref, { ...ref, digest: "b".repeat(64) }), false);
  assert.equal(contentReference({ id: "draft" }), undefined);
  assert.deepEqual(contentReference(ref), ref);
});

test("linking each document template keeps an exact reference, one named input, and the review checkpoint", () => {
  const linked = linkOutputTemplate(document, "design", "product", ref);
  const node = linked.spec.nodes.design;
  const binding = node.outputs.product.artifact.templateInput;
  const resource = linked.spec.inputs[node.inputs[binding].from.slice(10)].resource;
  assert.deepEqual(resource, { kind: "template_reference", reference: ref });
  assert.deepEqual(node.checkpoint, { mode: "approve" });
  assert.equal(node.outputs.technical.artifact.templateInput, undefined);
  assert.equal(document.spec.nodes.design.outputs.product.artifact.templateInput, undefined);
  const updated = linkOutputTemplate(linked, "design", "product", { ...ref, version: "2.0.0" });
  assert.equal(Object.values(updated.spec.nodes.design.inputs).filter((input) => input.type === "template").length, 1);
  assert.equal(Object.values(updated.spec.inputs).filter((input) => input.type === "template").length, 1);
  const unlinked = linkOutputTemplate(updated, "design", "product");
  assert.equal(Object.values(unlinked.spec.nodes.design.inputs).filter((input) => input.type === "template").length, 0);
  assert.equal(unlinked.spec.nodes.design.outputs.product.artifact.templateInput, undefined);
  assert.equal(Object.values(unlinked.spec.inputs).filter((input) => input.type === "template").length, 0);
});

test("shared root template declarations survive replacement of one consumer", () => {
  const linked = linkOutputTemplate(document, "design", "product", ref);
  const source = linked.spec.nodes.design.inputs[linked.spec.nodes.design.outputs.product.artifact.templateInput].from;
  linked.spec.nodes.other = { inputs: { shared: { type: "template", from: source } } };
  const removed = linkOutputTemplate(linked, "design", "product");
  assert.equal(removed.spec.inputs[source.slice(10)].resource.reference.id, ref.id);
});

test("linked prompt nodes remain editable and malformed exact references fail closed", () => {
  const linked = setNodePrompt(document, "design", ref);
  linked.spec.nodes.design.reasoning = { agent: "designer" };
  assert.ok(inspectNode(linked, "design"));
  linked.spec.nodes.design.prompt.digest = "invalid";
  assert.equal(inspectNode(linked, "design"), undefined);
});

test("switching one output template preserves another output's shared binding", () => {
  const linked = linkOutputTemplate(document, "design", "product", ref);
  linked.spec.nodes.design.outputs.technical.artifact.templateInput = linked.spec.nodes.design.outputs.product.artifact.templateInput;
  const updated = linkOutputTemplate(linked, "design", "product", { ...ref, version: "2.0.0" });
  const node = updated.spec.nodes.design;
  assert.notEqual(node.outputs.technical.artifact.templateInput, node.outputs.product.artifact.templateInput);
  assert.equal(Object.values(node.inputs).filter((input) => input.type === "template").length, 2);
});

test("outputs sharing a published template supply one named template input", () => {
  const product = linkOutputTemplate(document, "design", "product", ref);
  const linked = linkOutputTemplate(product, "design", "technical", ref);
  const node = linked.spec.nodes.design;
  assert.equal(node.outputs.product.artifact.templateInput, node.outputs.technical.artifact.templateInput);
  assert.equal(Object.values(node.inputs).filter((input) => input.type === "template").length, 1);
});

test("optional work context is explicitly bound and unlinking preserves other declarations", () => {
  const linked = setOptionalContext(document, "design", "deferred_work", "run.input.backlog");
  assert.deepEqual(linked.spec.nodes.design.inputs.deferred_work, { from: "run.input.backlog", type: "open_items", required: false });
  const unlinked = setOptionalContext(linked, "design", "deferred_work", "");
  assert.equal(unlinked.spec.nodes.design.inputs.deferred_work, undefined);
  assert.deepEqual(unlinked.spec.inputs, document.spec.inputs);
  const prompted = setNodePrompt(linked, "design", ref);
  assert.deepEqual(prompted.spec.nodes.design.prompt, ref);
  assert.deepEqual(prompted.spec.nodes.design.checkpoint, { mode: "approve" });
});

test("library search respects kind and explicit archived visibility", () => {
  const items = [{ name: "Product Design", description: "behavior", kind: "template" }, { name: "Old Design", description: "", kind: "template", archivedAt: "2026-01-01" }, { name: "Design Prompt", description: "", kind: "prompt" }];
  assert.equal(filterContent(items, "design", "template", false).length, 1);
  assert.equal(filterContent(items, "design", "template", true).length, 2);
  assert.match(documentText({ kind: "prompt", instructions: "Base", sections: [{ id: "always", when: { kind: "always" }, instructions: "Always included" }] }), /Always included/);
});
