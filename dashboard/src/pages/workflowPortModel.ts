import type { EditorGraph, JsonObject, ValueType, WorkflowLayout } from "./workflowEditorModel";

export type Port =
  | { kind: "execution"; nodeId: string; portId: "execute"; direction: "input" }
  | { kind: "execution"; nodeId: string; portId: "complete"; direction: "output" }
  | { kind: "data"; nodeId: string; portId: string; direction: "input" | "output"; valueType: ValueType; required: boolean }
  | { kind: "run_input"; portId: string; direction: "output"; valueType: ValueType };
export type PortEdge =
  | { kind: "execution"; id: string; source: Extract<Port, { kind: "execution" }>; target: Extract<Port, { kind: "execution" }> }
  | { kind: "data"; id: string; source: Exclude<Port, { kind: "execution" }>; target: Extract<Port, { kind: "data" }> };
export interface PortFinding { nodeId: string; field: string; code: string; message: string }
export interface PortGraph { ports: Port[]; edges: PortEdge[]; findings: PortFinding[] }
const record = (value: unknown): JsonObject => value !== null && typeof value === "object" && !Array.isArray(value) ? value as JsonObject : {};
const valueTypes = new Set(["null", "boolean", "integer", "number", "string", "array", "object"]);
const compatible = (source: ValueType, target: ValueType) => valueTypes.has(source) && valueTypes.has(target) && (source === target || source === "integer" && target === "number");
export const portKey = (port: Port) => JSON.stringify(port.kind === "run_input" ? [port.kind, port.portId] : [port.kind, port.nodeId, port.direction, port.portId]);
export const portLabel = (port: Port) => port.kind === "run_input" ? `Run input ${port.portId} (${port.valueType})` : `${port.nodeId}.${port.portId}${port.kind === "data" ? ` (${port.valueType}${port.required ? ", required" : ", optional"})` : " (execution)"}`;
export const sourceReference = (port: Exclude<Port, { kind: "execution" }>) => port.kind === "run_input" ? `run.input.${port.portId}` : `node.${port.nodeId}.output.${port.portId}`;

/** Rebuildable projection: the workflow document owns every binding and transition. */
export function derivePortGraph(document: JsonObject, graph: EditorGraph): PortGraph {
  const spec = record(document.spec), nodes = record(spec.nodes);
  const ports: Port[] = Object.entries(record(spec.inputs)).map(([portId, raw]) => ({ kind: "run_input", portId, direction: "output", valueType: record(raw).type as ValueType }));
  for (const node of graph.nodes) {
    ports.push({ kind: "execution", nodeId: node.id, portId: "execute", direction: "input" }, { kind: "execution", nodeId: node.id, portId: "complete", direction: "output" });
    for (const direction of ["input", "output"] as const) for (const [portId, raw] of Object.entries(record(record(nodes[node.id])[`${direction}s`]))) {
      ports.push({ kind: "data", nodeId: node.id, direction, portId, valueType: record(raw).type as ValueType, required: record(raw).required !== false });
    }
  }
  const edges: PortEdge[] = graph.edges.map((edge) => ({ kind: "execution", id: edge.id, source: { kind: "execution", nodeId: edge.from, portId: "complete", direction: "output" }, target: { kind: "execution", nodeId: edge.to, portId: "execute", direction: "input" } }));
  const findings: PortFinding[] = [];
  for (const edge of graph.edges) if (!Object.hasOwn(nodes, edge.to)) {
    const transitions = record(nodes[edge.from]).transitions;
    const index = Array.isArray(transitions) ? transitions.findIndex((raw) => record(raw).id === edge.transitionId && record(raw).to === edge.to) : -1;
    findings.push({ nodeId: edge.from, field: `transitions.${index}.to`, code: "WF_REFERENCE_MISSING", message: `unknown transition target "${edge.to}"` });
  }
  for (const target of ports) {
    if (target.kind !== "data" || target.direction !== "input") continue;
    const binding = record(record(record(nodes[target.nodeId]).inputs)[target.portId]);
    const source = ports.find((port) => port.kind !== "execution" && port.direction === "output" && sourceReference(port) === binding.from);
    if (!source || source.kind === "execution") findings.push({ nodeId: target.nodeId, field: `inputs.${target.portId}.from`, code: "WF_REFERENCE_MISSING", message: `unknown binding source ${JSON.stringify(binding.from ?? "")}` });
    else {
      edges.push({ kind: "data", id: `binding:${target.nodeId}:${target.portId}`, source, target });
      if (!binding.pointer && !compatible(source.valueType, target.valueType)) findings.push({ nodeId: target.nodeId, field: `inputs.${target.portId}`, code: "WF_BINDING_INCOMPATIBLE", message: `binding "${target.portId}" expects ${target.valueType} from ${source.valueType}` });
    }
  }
  return { ports, edges, findings };
}

export function connectionError(source: Port, target: Port): string | undefined {
  if (source.direction !== "output" || target.direction !== "input") return "Connections must run from an output port to an input port.";
  if (source.kind === "execution" || target.kind === "execution") {
    if (source.kind !== "execution" || target.kind !== "execution") return "Execution ports cannot connect to data ports.";
    return source.nodeId === target.nodeId ? "Self transitions require a bounded repair path in Structure view." : undefined;
  }
  if (!compatible(source.valueType, target.valueType)) return `binding "${target.portId}" expects ${target.valueType} from ${source.valueType}`;
  return undefined;
}

/** Reconnects the whole value explicitly, dropping any selector for the previous source. */
export function bindPorts(document: JsonObject, source: Port, target: Port, available: readonly Port[]): { kind: "changed"; document: JsonObject } | { kind: "invalid"; message: string } {
  const currentSource = available.find((port) => portKey(port) === portKey(source));
  const currentTarget = available.find((port) => portKey(port) === portKey(target));
  if (!currentSource || !currentTarget) return { kind: "invalid", message: "A selected port no longer exists. Select current ports." };
  const error = connectionError(currentSource, currentTarget);
  if (error) return { kind: "invalid", message: error };
  if (currentSource.kind === "execution" || currentTarget.kind !== "data") return { kind: "invalid", message: "Select a data output and a data input." };
  const next = structuredClone(document), nodes = record(record(next.spec).nodes);
  const binding = record(record(record(nodes[currentTarget.nodeId]).inputs)[currentTarget.portId]);
  binding.from = sourceReference(currentSource); delete binding.pointer;
  return { kind: "changed", document: next };
}

export function graphNodeHeight(ports: readonly Port[], nodeId: string) { return 124 + 28 * Math.max(...["input", "output"].map((direction) => ports.filter((port) => port.kind !== "run_input" && port.nodeId === nodeId && port.direction === direction).length)); }
export function graphBounds(graph: EditorGraph, ports: readonly Port[]) {
  const minX = Math.min(0, ...graph.nodes.map((node) => node.position.x)) - (ports.some((port) => port.kind === "run_input") ? 300 : 30);
  const minY = Math.min(0, ...graph.nodes.map((node) => node.position.y)) - 30;
  return { x: minX, y: minY, width: Math.max(560, ...graph.nodes.map((node) => node.position.x + 290)) - minX, height: Math.max(320, ports.filter((port) => port.kind === "run_input").length * 28 + 100, ...graph.nodes.map((node) => node.position.y + graphNodeHeight(ports, node.id) + 30)) - minY };
}
export function autoLayoutGraph(layout: WorkflowLayout, graph: EditorGraph, ports: readonly Port[]): WorkflowLayout {
  // Stable breadth-first ranks; cycles/disconnected nodes are placed once, never chased.
  const remaining = new Set(graph.nodes.map((node) => node.id)), ordered: string[] = [];
  const queue = graph.nodes.filter((node) => node.entry || !graph.edges.some((edge) => edge.to === node.id)).map((node) => node.id);
  while (remaining.size) {
    const id = queue.shift() ?? remaining.values().next().value!;
    if (!remaining.delete(id)) continue;
    ordered.push(id); queue.push(...graph.edges.filter((edge) => edge.from === id).map((edge) => edge.to));
  }
  const rowHeight = Math.max(220, ...graph.nodes.map((node) => graphNodeHeight(ports, node.id) + 40));
  return { ...layout, nodes: Object.fromEntries(ordered.map((id, index) => [id, { x: 40 + index % 3 * 330, y: 40 + Math.floor(index / 3) * rowHeight }])) };
}
