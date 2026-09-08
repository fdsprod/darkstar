import dagre from "@dagrejs/dagre";
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

export const GRAPH_NODE_WIDTH = 340;
export const GRAPH_NODE_HEADER_HEIGHT = 136;
export const GRAPH_PORT_ROW_HEIGHT = 34;
export const PORT_LAYOUT_VERSION = 6;
export const READABLE_GRAPH_VIEWPORT = { x: -24, y: 48, zoom: 0.85 } as const;

export function graphNodeWidth(ports: readonly Port[], node: Pick<EditorGraph["nodes"][number], "id" | "displayName" | "type">) {
  const labels = ports.filter((port) => port.kind !== "run_input" && port.nodeId === node.id).map((port) => `${port.portId} ${"valueType" in port ? port.valueType : "execution"}${"required" in port && port.required ? " *" : ""}`);
  const headerWidth = Math.max(node.displayName.length, node.type.replaceAll("_", " ").length) * 7 + 48;
  const portWidth = Math.max(0, ...labels.map((label) => label.length * 6.5 + 30));
  return Math.ceil(Math.max(GRAPH_NODE_WIDTH, headerWidth, portWidth * 2 + 52));
}
export function graphRunInputWidth(ports: readonly Port[]) { return Math.ceil(Math.max(280, ...ports.filter((port) => port.kind === "run_input").map((port) => `${port.portId} ${port.valueType}${"required" in port && port.required ? " *" : ""}`.length * 6.5 + 44))); }
export function graphNodeHeight(ports: readonly Port[], nodeId: string) { return GRAPH_NODE_HEADER_HEIGHT + GRAPH_PORT_ROW_HEIGHT * Math.max(1, ...["input", "output"].map((direction) => ports.filter((port) => port.kind !== "run_input" && port.nodeId === nodeId && port.direction === direction).length)) + 20; }
export function graphBounds(graph: EditorGraph, ports: readonly Port[]) {
  const hasInputs = ports.some((port) => port.kind === "run_input");
  const minX = Math.min(0, ...graph.nodes.map((node) => node.position.x), hasInputs ? 80 : 0) - 60;
  const minY = Math.min(0, ...graph.nodes.map((node) => node.position.y)) - 60;
  return { x: minX, y: minY, width: Math.max(720, ...graph.nodes.map((node) => node.position.x + graphNodeWidth(ports, node) + 80), hasInputs ? 80 + graphRunInputWidth(ports) + 80 : 0) - minX, height: Math.max(440, ports.filter((port) => port.kind === "run_input").length * 38 + 170, ...graph.nodes.map((node) => node.position.y + graphNodeHeight(ports, node.id) + 80)) - minY };
}
export function autoLayoutGraph(layout: WorkflowLayout, graph: EditorGraph, ports: readonly Port[]): WorkflowLayout {
  // Dagre owns presentation geometry. Bounded repair transitions are deliberately
  // excluded from ranking, then rendered over the resulting forward topology.
  const nodeOrder = new Map(graph.nodes.map((node, index) => [node.id, index]));
  const nodeIds = new Set(nodeOrder.keys());
  const forwardEdges = graph.edges.filter((edge) => edge.kind !== "bounded_repair" && nodeIds.has(edge.from) && nodeIds.has(edge.to));
  const ranked = new dagre.graphlib.Graph({ directed: true, multigraph: true, compound: false });
  ranked.setGraph({ rankdir: "TB", ranker: "network-simplex", align: "UL", ranksep: 112, nodesep: 96, edgesep: 42, marginx: 80, marginy: 80 });
  ranked.setDefaultEdgeLabel(() => ({}));
  for (const node of graph.nodes) ranked.setNode(node.id, { width: graphNodeWidth(ports, node), height: graphNodeHeight(ports, node.id), order: nodeOrder.get(node.id) });
  for (const [index, edge] of forwardEdges.entries()) ranked.setEdge(edge.from, edge.to, { weight: edge.kind === "normal" || edge.kind === "subworkflow" ? 3 : 2, minlen: 1, order: index }, edge.id);
  dagre.layout(ranked);
  const inputOffset = ports.some((port) => port.kind === "run_input") ? graphRunInputWidth(ports) + 120 : 0;
  return { ...layout, portLayoutVersion: PORT_LAYOUT_VERSION, viewport: { ...READABLE_GRAPH_VIEWPORT }, nodes: Object.fromEntries(graph.nodes.map((node) => {
    const placed = ranked.node(node.id) as { x: number; y: number };
    return [node.id, { x: Math.round(placed.x - graphNodeWidth(ports, node) / 2 + inputOffset), y: Math.round(placed.y - graphNodeHeight(ports, node.id) / 2) }];
  })) };
}

/** Replaces coordinates written for earlier, smaller cards with the current ranked layout. */
export function preparePortLayout(layout: WorkflowLayout, graph: EditorGraph, ports: readonly Port[]): WorkflowLayout {
  return layout.portLayoutVersion === PORT_LAYOUT_VERSION ? layout : autoLayoutGraph(layout, graph, ports);
}
