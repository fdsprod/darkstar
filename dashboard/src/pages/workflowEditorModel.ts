import type { components } from "../api/schema.generated";

type Schemas = components["schemas"];
export type JsonObject = Record<string, unknown>;
export type WorkflowNodeType = "reasoning" | "gate" | "command" | "approval" | "subworkflow" | "point_execution";
export type WorkflowEdgeKind = "normal" | "conditional" | "bounded_repair" | "subworkflow";
export type AuthoredTransitionKind = Exclude<WorkflowEdgeKind, "subworkflow">;
export type EditorView = "canvas" | "structure";
export type EditorSelection = { kind: "none" } | { kind: "node"; nodeId: string } | { kind: "edge"; edgeId: string };
export type CanvasInteraction = { kind: "idle" } | { kind: "connecting"; fromNodeId: string } | { kind: "dragging"; nodeId: string; origin: NodePosition };
export type PersistenceState =
  | { kind: "clean"; revision: number }
  | { kind: "dirty"; revision: number }
  | { kind: "saving"; revision: number }
  | { kind: "conflict"; revision: number; remote: Schemas["WorkflowDraft"] }
  | { kind: "offline"; revision: number; message: string }
  | { kind: "error"; revision: number; message: string };
export type ValidationState =
  | { kind: "not_run" }
  | { kind: "checking"; revision: number }
  | { kind: "valid"; revision: number; digest?: string }
  | { kind: "invalid"; revision: number; findings: Schemas["WorkflowAuthoringFinding"][] }
  | { kind: "error"; message: string };

export interface NodePosition { x: number; y: number }
export interface WorkflowLayout { version: 1; nodes: Record<string, NodePosition>; viewport?: { x: number; y: number; zoom: number }; [key: string]: unknown }
export interface VisualNode {
  id: string;
  type: WorkflowNodeType;
  displayName: string;
  entry: boolean;
  terminal: boolean;
  validationCount: number;
  checkpoint: string;
  subworkflow?: string;
  position: NodePosition;
}
export interface VisualEdge { id: string; transitionId: string; from: string; to: string; kind: WorkflowEdgeKind }
export interface EditorGraph { nodes: VisualNode[]; edges: VisualEdge[] }

const nodeTypes: readonly WorkflowNodeType[] = ["reasoning", "gate", "command", "approval", "subworkflow", "point_execution"];
const identifier = /^[a-z][a-z0-9_]{0,63}$/;

export function isNodeType(value: unknown): value is WorkflowNodeType { return typeof value === "string" && nodeTypes.includes(value as WorkflowNodeType); }
export function edgeIdentity(from: string, transitionId: string, to: string) { return `${from}\u001f${transitionId}\u001f${to}`; }

export function createStarterDocument(name: string): JsonObject {
  return {
    apiVersion: "darkstar.local/v1alpha2",
    kind: "Workflow",
    metadata: { name, version: "0.1.0", displayName: humanizeIdentifier(name.split("/").at(-1) || name) },
    spec: {
      routeDefaults: { entry: "start", terminals: ["start"] },
      nodes: { start: createNode("reasoning", "Start", { entry: true, terminal: true }) },
    },
  };
}

export function normalizeLayout(value: unknown): WorkflowLayout {
  const source = record(value);
  const rawNodes = record(source.nodes);
  const nodes: Record<string, NodePosition> = {};
  for (const [id, raw] of Object.entries(rawNodes)) {
    const point = record(raw);
    if (typeof point.x === "number" && Number.isFinite(point.x) && typeof point.y === "number" && Number.isFinite(point.y)) nodes[id] = { x: point.x, y: point.y };
  }
  const rawViewport = record(source.viewport);
  const viewport = typeof rawViewport.x === "number" && typeof rawViewport.y === "number" && typeof rawViewport.zoom === "number"
    ? { x: rawViewport.x, y: rawViewport.y, zoom: rawViewport.zoom } : undefined;
  return { ...source, version: 1, nodes, ...(viewport ? { viewport } : {}) };
}

export function deriveEditorGraph(document: unknown, layoutValue: unknown, findings: readonly Schemas["WorkflowAuthoringFinding"][] = []): EditorGraph {
  const nodesRecord = workflowNodes(document);
  const layout = normalizeLayout(layoutValue);
  const nodes: VisualNode[] = [];
  const edges: VisualEdge[] = [];
  let index = 0;
  for (const [id, rawNode] of Object.entries(nodesRecord)) {
    const node = record(rawNode);
    const type = isNodeType(node.type) ? node.type : "reasoning";
    const validators = Array.isArray(node.validators) ? node.validators.length : 0;
    const findingCount = findings.filter((finding) => finding.nodeId === id).length;
    const call = record(node.call); const workflow = record(call.workflow);
    nodes.push({
      id, type, displayName: typeof node.displayName === "string" ? node.displayName : humanizeIdentifier(id),
      entry: node.entry === true, terminal: node.terminal === true,
      validationCount: validators + findingCount, checkpoint: checkpointLabel(node.checkpoint),
      ...(type === "subworkflow" && typeof workflow.name === "string" ? { subworkflow: `${workflow.name}@${String(workflow.version ?? "unversioned")}` } : {}),
      position: layout.nodes[id] ?? { x: 72 + (index % 3) * 240, y: 64 + Math.floor(index / 3) * 160 },
    });
    const transitions = Array.isArray(node.transitions) ? node.transitions : [];
    for (const rawTransition of transitions) {
      const transition = record(rawTransition);
      if (typeof transition.id !== "string" || typeof transition.to !== "string") continue;
      const kind: WorkflowEdgeKind = transition.kind === "bounded" ? "bounded_repair" : transition.when ? "conditional" : type === "subworkflow" ? "subworkflow" : "normal";
      edges.push({ id: edgeIdentity(id, transition.id, transition.to), transitionId: transition.id, from: id, to: transition.to, kind });
    }
    index += 1;
  }
  return { nodes, edges };
}

export function addNode(document: JsonObject, type: WorkflowNodeType, requestedId?: string): { document: JsonObject; nodeId: string } {
  const next = clone(document); const nodes = workflowNodes(next);
  const base = sanitizeIdentifier(requestedId || type); const nodeId = uniqueIdentifier(base, new Set(Object.keys(nodes)));
  nodes[nodeId] = createNode(type, humanizeIdentifier(nodeId));
  return { document: next, nodeId };
}

export function removeNode(document: JsonObject, nodeId: string): JsonObject {
  const next = clone(document); const nodes = workflowNodes(next);
  if (!(nodeId in nodes)) return next;
  const removedTransitions = new Set((Array.isArray(record(nodes[nodeId]).transitions) ? record(nodes[nodeId]).transitions as unknown[] : [])
    .map((candidate) => record(candidate).id).filter((value): value is string => typeof value === "string"));
  delete nodes[nodeId];
  for (const raw of Object.values(nodes)) {
    const node = record(raw);
    if (Array.isArray(node.transitions)) node.transitions = node.transitions.filter((candidate) => record(candidate).to !== nodeId);
    const join = record(node.join);
    if (Array.isArray(join.from)) join.from = join.from.filter((transition) => typeof transition !== "string" || !removedTransitions.has(transition));
  }
  const defaults = routeDefaults(next);
  const remaining = Object.keys(nodes);
  if (defaults.entry === nodeId) defaults.entry = remaining[0] ?? "start";
  if (Array.isArray(defaults.terminals)) defaults.terminals = defaults.terminals.filter((id) => id !== nodeId);
  if (remaining.length && (!Array.isArray(defaults.terminals) || defaults.terminals.length === 0)) defaults.terminals = [remaining.at(-1)!];
  return next;
}

export function connectNodes(document: JsonObject, from: string, to: string, kind: AuthoredTransitionKind = "normal"): JsonObject {
  const next = clone(document); const nodes = workflowNodes(next);
  const source = record(nodes[from]);
  if (!nodes[from] || !nodes[to] || from === to && kind !== "bounded_repair") return next;
  const transitions = Array.isArray(source.transitions) ? source.transitions : [];
  const used = new Set(transitions.map((candidate) => record(candidate).id).filter((value): value is string => typeof value === "string"));
  const id = uniqueIdentifier(sanitizeIdentifier(`${from}_to_${to}`), used);
  const transition: JsonObject = { id, to };
  if (kind === "conditional") transition.when = { const: true };
  if (kind === "bounded_repair") { transition.kind = "bounded"; transition.maxTraversals = 1; transition.when = { const: true }; }
  source.transitions = [...transitions, transition];
  return next;
}

export function removeEdge(document: JsonObject, compositeId: string): JsonObject {
  const next = clone(document); const [from, transitionId, to] = compositeId.split("\u001f");
  const source = record(workflowNodes(next)[from]);
  if (Array.isArray(source.transitions)) source.transitions = source.transitions.filter((candidate) => {
    const transition = record(candidate); return transition.id !== transitionId || transition.to !== to;
  });
  return next;
}

export function reorderNode(document: JsonObject, nodeId: string, direction: -1 | 1): JsonObject {
  const next = clone(document); const nodes = workflowNodes(next); const entries = Object.entries(nodes);
  const from = entries.findIndex(([id]) => id === nodeId); const to = from + direction;
  if (from < 0 || to < 0 || to >= entries.length) return next;
  [entries[from], entries[to]] = [entries[to], entries[from]];
  record(record(next.spec).nodes); record(next.spec).nodes = Object.fromEntries(entries);
  return next;
}

export function updateNode(document: JsonObject, nodeId: string, change: { displayName?: string; entry?: boolean; terminal?: boolean; checkpoint?: "none" | "acknowledge" | "approve" }): JsonObject {
  const next = clone(document); const node = record(workflowNodes(next)[nodeId]); const defaults = routeDefaults(next);
  if (change.displayName !== undefined) node.displayName = change.displayName || humanizeIdentifier(nodeId);
  if (change.entry !== undefined) {
    node.entry = change.entry;
    if (change.entry) defaults.entry = nodeId;
    else if (defaults.entry === nodeId) {
      const replacement = Object.entries(workflowNodes(next)).find(([id, candidate]) => id !== nodeId && record(candidate).entry === true)
        ?? Object.entries(workflowNodes(next)).find(([id]) => id !== nodeId);
      if (replacement) { record(replacement[1]).entry = true; defaults.entry = replacement[0]; }
      else node.entry = true;
    }
  }
  if (change.terminal !== undefined) {
    node.terminal = change.terminal;
    const terminals = new Set(Array.isArray(defaults.terminals) ? defaults.terminals.filter((id): id is string => typeof id === "string") : []);
    change.terminal ? terminals.add(nodeId) : terminals.delete(nodeId);
    if (terminals.size === 0) {
      const fallback = Object.entries(workflowNodes(next)).find(([id, candidate]) => id !== nodeId && record(candidate).terminal === true)
        ?? Object.entries(workflowNodes(next)).find(([id]) => id !== nodeId);
      if (fallback) { record(fallback[1]).terminal = true; terminals.add(fallback[0]); }
      else { node.terminal = true; terminals.add(nodeId); }
    }
    defaults.terminals = [...terminals];
  }
  if (change.checkpoint !== undefined) node.checkpoint = { mode: change.checkpoint };
  return next;
}

export function moveNode(layoutValue: unknown, nodeId: string, position: NodePosition): WorkflowLayout {
  const layout = normalizeLayout(layoutValue);
  return { ...layout, nodes: { ...layout.nodes, [nodeId]: { x: Math.round(position.x), y: Math.round(position.y) } } };
}

export function draftRevision(state: PersistenceState) { return state.revision; }
export function persistenceLabel(state: PersistenceState) {
  switch (state.kind) {
    case "clean": return "Saved"; case "dirty": return "Unsaved changes"; case "saving": return "Saving…";
    case "conflict": return "Save conflict"; case "offline": return "Offline · changes retained"; case "error": return "Save failed · changes retained";
  }
}

function createNode(type: WorkflowNodeType, displayName: string, flags: { entry?: boolean; terminal?: boolean } = {}): JsonObject {
  const common: JsonObject = { displayName, type, entry: flags.entry ?? false, terminal: flags.terminal ?? false, inputs: {}, outputs: {}, transitions: [] };
  switch (type) {
    case "reasoning": return { ...common, reasoning: { agent: "authoring-agent" } };
    case "gate": return { ...common, gate: { policy: "authoring-policy", condition: { const: true } } };
    case "command": return { ...common, command: { argv: ["command"] } };
    case "approval": return { ...common, approval: { actor: "workflow-owner" } };
    case "subworkflow": return { ...common, call: { workflow: { name: "workflow/name", version: "0.1.0" }, entry: "start", terminals: ["finish"], inputs: {}, outputs: {} } };
    case "point_execution": return { ...common, points: { planInput: "plan", approval: "none", riskTags: [], validation: "combined", publishing: "after_story_validation" } };
  }
}

function workflowNodes(document: unknown): JsonObject { return record(record(record(document).spec).nodes); }
function routeDefaults(document: unknown): JsonObject { return record(record(record(document).spec).routeDefaults); }
function record(value: unknown): JsonObject { return value !== null && typeof value === "object" && !Array.isArray(value) ? value as JsonObject : {}; }
function clone<T>(value: T): T { return structuredClone(value); }
function sanitizeIdentifier(value: string) { const clean = value.toLowerCase().replace(/[^a-z0-9_]+/g, "_").replace(/^_+|_+$/g, "").slice(0, 64); return identifier.test(clean) ? clean : `node_${clean}`.slice(0, 64); }
function uniqueIdentifier(base: string, used: ReadonlySet<string>) { if (!used.has(base)) return base; for (let index = 2; index < 10_000; index += 1) { const suffix = `_${index}`; const candidate = `${base.slice(0, 64 - suffix.length)}${suffix}`; if (!used.has(candidate)) return candidate; } throw new Error("No unique workflow identifier is available."); }
function checkpointLabel(value: unknown) { if (typeof value === "string") return value; const item = record(value); return typeof item.mode === "string" ? item.mode : "none"; }
function humanizeIdentifier(value: string) { return value.replace(/[_.-]+/g, " ").replace(/\b\w/g, (letter) => letter.toUpperCase()); }
