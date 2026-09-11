import type { ContentReference } from "./contentLibraryModel";
import type { JsonObject } from "./workflowEditorModel";

function record(value: unknown): JsonObject {
  return value && typeof value === "object" && !Array.isArray(value) ? value as JsonObject : {};
}

export function setNodePrompt(document: JsonObject, nodeId: string, reference?: ContentReference): JsonObject {
  const next = structuredClone(document);
  const node = record(record(record(next.spec).nodes)[nodeId]);
  if (reference) {
    next.apiVersion = "darkstar.local/v1alpha3";
    node.prompt = { ...reference };
  } else {
    delete node.prompt;
  }
  return next;
}

export function linkOutputTemplate(document: JsonObject, nodeId: string, outputId: string, reference?: ContentReference): JsonObject {
  const next = structuredClone(document);
  const spec = record(next.spec);
  const node = record(record(spec.nodes)[nodeId]);
  const output = record(record(node.outputs)[outputId]);
  const artifact = record(output.artifact);
  output.artifact = artifact;
  const oldBinding = typeof artifact.templateInput === "string" ? artifact.templateInput : undefined;
  const bindings = record(node.inputs);
  node.inputs = bindings;
  const oldSource = oldBinding ? String(record(bindings[oldBinding]).from ?? "") : "";
  if (oldBinding && !Object.entries(record(node.outputs)).some(([id, raw]) => id !== outputId && record(record(raw).artifact).templateInput === oldBinding)) {
    // An output owns its dedicated template binding. Keep shared bindings used
    // by other outputs, but do not supply superseded templates to an attempt.
    delete bindings[oldBinding];
  }
  if (oldSource.startsWith("run.input.")) {
    const resourceId = oldSource.slice(10);
    const resources = record(spec.inputs);
    const shared = Object.values(record(spec.nodes)).some((raw) => Object.values(record(record(raw).inputs)).some((input) => record(input).from === oldSource));
    if (!shared && record(record(resources[resourceId]).resource).kind === "template_reference") {
      delete resources[resourceId];
    }
  }
  if (!reference) {
    delete artifact.templateInput;
    return next;
  }
  next.apiVersion = "darkstar.local/v1alpha3";
  const inputs = record(spec.inputs);
  spec.inputs = inputs;
  for (const [name, raw] of Object.entries(bindings)) {
    const source = String(record(raw).from ?? "");
    const resource = record(record(inputs[source.slice(10)]).resource);
    if (source.startsWith("run.input.") && resource.kind === "template_reference" && JSON.stringify(resource.reference) === JSON.stringify(reference)) {
      artifact.templateInput = name;
      return next;
    }
  }
  const stem = `${nodeId}_${outputId}_template`.slice(0, 58);
  let input = stem;
  for (let i = 2; Object.hasOwn(inputs, input); i++) {
    const resource = record(record(inputs[input]).resource);
    if (resource.kind === "template_reference" && JSON.stringify(resource.reference) === JSON.stringify(reference)) {
      break;
    }
    input = `${stem}_${i}`;
  }
  const bindingStem = `${outputId}_template`.slice(0, 58);
  let binding = bindingStem;
  for (let i = 2; Object.hasOwn(bindings, binding); i++) {
    if (record(bindings[binding]).from === `run.input.${input}`) {
      break;
    }
    binding = `${bindingStem}_${i}`;
  }
  inputs[input] = { type: "template", resource: { kind: "template_reference", reference: { ...reference } } };
  bindings[binding] = { type: "template", from: `run.input.${input}`, required: true };
  artifact.templateInput = binding;
  return next;
}

export function setOptionalContext(document: JsonObject, nodeId: string, input: "open_items" | "deferred_work", source: string): JsonObject {
  const next = structuredClone(document);
  const node = record(record(record(next.spec).nodes)[nodeId]);
  const inputs = record(node.inputs);
  node.inputs = inputs;
  if (!source) {
    delete inputs[input];
    return next;
  }
  const old = record(inputs[input]);
  const declaration = source.startsWith("run.input.")
    ? record(record(record(next.spec).inputs)[source.slice("run.input.".length)])
    : record(record(record(record(record(next.spec).nodes)[source.split(".")[1]]).outputs)[source.split(".")[3]]);
  inputs[input] = { ...old, from: source, type: declaration.type ?? "open_items", required: false };
  delete record(inputs[input]).default;
  return next;
}

export function contextSources(document: JsonObject, nodeId: string): { from: string; label: string }[] {
  const spec = record(document.spec);
  const sources: { from: string; label: string }[] = [];
  const suitable = (declaration: JsonObject) => ["open_items", "markdown"].includes(String(declaration.type));
  for (const [id, raw] of Object.entries(record(spec.inputs))) {
    if (suitable(record(raw))) {
      sources.push({ from: `run.input.${id}`, label: `${String(record(raw).description || id)} · work input` });
    }
  }
  for (const [id, raw] of Object.entries(record(spec.nodes))) {
    if (id === nodeId) {
      continue;
    }
    for (const [output, declaration] of Object.entries(record(record(raw).outputs))) {
      if (suitable(record(declaration))) {
        sources.push({ from: `node.${id}.output.${output}`, label: `${id} / ${output}` });
      }
    }
  }
  return sources;
}
