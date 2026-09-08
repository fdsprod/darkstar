import type { JsonObject, ValueType } from "./workflowEditorModel";

export const resourceKinds = ["task", "repository", "artifact", "template", "constant", "config", "open_items", "decision_log"] as const;
export type ResourceKind = typeof resourceKinds[number];
export const record = (value: unknown): JsonObject => value && typeof value === "object" && !Array.isArray(value) ? value as JsonObject : {};
export const inputNodeId = (id: string) => `$input:${id}`;
export const outputNodeId = (node: string, id: string) => `$output:${node}:${id}`;

export function resourceLabel(id: string, declaration: JsonObject) {
  const kind = String(record(declaration.resource).kind ?? (id === "story" ? "task" : id === "repository" ? "repository" : "value"));
  return { kind, title: String(declaration.description || id.replaceAll("_", " ")) };
}

export function addResource(document: JsonObject, kind: ResourceKind) {
  const next = structuredClone(document), spec = record(next.spec);
  next.apiVersion = "darkstar.local/v1alpha3";
  const inputs = record(spec.inputs); spec.inputs = inputs;
  let id: string = kind; for (let i = 2; Object.hasOwn(inputs, id); i++) id = `${kind}_${i}`;
  const source: JsonObject = { kind };
  if (kind === "template") Object.assign(source, { version: "1.0.0", content: "# Document\n\n## Overview\n\nDescribe the proposed solution.\n", requiredHeadings: ["Overview"] });
  if (kind === "constant") source.value = "";
  if (kind === "artifact") Object.assign(source,{filename:"document.md",content:""});
  if (kind === "config") source.key = "provider.codex.executable";
  inputs[id] = { type: kind === "constant" || kind === "config" || kind === "artifact" ? "string" : "object", resource: source };
  return { document: next, id: inputNodeId(id) };
}

export function updateResource(document: JsonObject, id: string, declaration: JsonObject): JsonObject {
  const next = structuredClone(document); record(record(next.spec).inputs)[id] = declaration; return next;
}

export function removeResource(document: JsonObject, id: string): JsonObject {
  const next = structuredClone(document), spec = record(next.spec);
  // Do not leave dangling edges: removing a source also removes its bindings.
  for (const raw of Object.values(record(spec.nodes))) {
    const inputs = record(record(raw).inputs);
    for (const [key, binding] of Object.entries(inputs)) if (record(binding).from === `run.input.${id}`) delete inputs[key];
  }
  delete record(spec.inputs)[id]; return next;
}

export function outputDeclaration(document: JsonObject, node: string, id: string) {
  return record(record(record(record(document.spec).nodes)[node]).outputs)[id] as JsonObject | undefined;
}

export function setOutputContract(document: JsonObject, node: string, id: string, changes: JsonObject) {
  const next = structuredClone(document), outputs = record(record(record(record(next.spec).nodes)[node]).outputs);
  next.apiVersion = "darkstar.local/v1alpha3";
  outputs[id] = { ...record(outputs[id]), ...changes }; return next;
}

export function addArtifactOutput(document: JsonObject, node: string) {
  const outputs = record(record(record(record(document.spec).nodes)[node]).outputs);
  let id = "document"; for (let i = 2; Object.hasOwn(outputs, id); i++) id = `document_${i}`;
  return { document: setOutputContract(document, node, id, { type: "string" satisfies ValueType, artifact: { filename: `${id}.md` } }), id: outputNodeId(node, id) };
}
