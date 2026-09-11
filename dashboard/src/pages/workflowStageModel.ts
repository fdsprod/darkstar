import type { JsonObject } from "./workflowEditorModel";
function record(value: unknown): JsonObject {
  return value && typeof value === "object" && !Array.isArray(value) ? value as JsonObject : {};
}

export interface StagePreset {
  id: string;
  name: string;
  description: string;
  node: JsonObject;
  inputs: Record<string, JsonObject>;
  optionalInputs: Record<string, JsonObject>;
}

export function addStagePreset(document: JsonObject, preset: StagePreset): { document: JsonObject; id: string } {
  const next = structuredClone(document);
  next.apiVersion = "darkstar.local/v1alpha3";
  const spec = record(next.spec);
  const nodes = record(spec.nodes);
  const inputs = record(spec.inputs);
  spec.inputs = inputs;
  let id = preset.id;
  for (let index = 2; Object.hasOwn(nodes, id); index++) {
    id = `${preset.id}_${index}`;
  }
  const node = structuredClone(preset.node);
  for (const [name, declaration] of Object.entries(preset.inputs)) {
    let inputID = name;
    for (let index = 2; Object.hasOwn(inputs, inputID) && JSON.stringify(inputs[inputID]) !== JSON.stringify(declaration); index++) {
      inputID = `${name}_${index}`;
    }
    inputs[inputID] = structuredClone(declaration);
    for (const value of Object.values(record(node.inputs))) {
      const binding = record(value);
      if (binding.from === `run.input.${name}`) {
        binding.from = `run.input.${inputID}`;
      }
    }
  }
  nodes[id] = node;
  spec.nodes = nodes;
  next.spec = spec;
  return { document: next, id };
}
