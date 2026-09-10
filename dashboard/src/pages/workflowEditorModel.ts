import type { components } from "../api/schema.generated";

type Schemas = components["schemas"];
export type JsonObject = Record<string, unknown>;
export type JsonValue = null | boolean | number | string | JsonValue[] | { [key: string]: JsonValue };
export type WorkflowNodeType = "extension" | "workspace_prepare" | "workspace_validate" | "reasoning" | "implementation" | "gate" | "command" | "approval" | "subworkflow" | "point_execution" | "routing";
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
  | ({ kind: "checking" } & DraftEvidence)
  | ({ kind: "valid"; semanticDigest: string } & DraftEvidence)
  | ({ kind: "invalid"; semanticDigest?: string; findings: Schemas["WorkflowAuthoringFinding"][] } & DraftEvidence)
  | ({ kind: "error"; message: string } & DraftEvidence);
export interface DraftEvidence { draftId: string; revision: number; documentDigest: string }
export type PublishState =
  | { kind: "closed" }
  | ({ kind: "confirm"; version: string; semanticDigest: string } & DraftEvidence)
  | ({ kind: "publishing"; version: string; semanticDigest: string } & DraftEvidence)
  | { kind: "published"; result: Schemas["WorkflowDraftPublishResult"] }
  | ({ kind: "conflict" | "invalid" | "error"; version: string; semanticDigest: string; message: string } & DraftEvidence);
export type RoutePreviewState =
  | { kind: "closed" }
  | ({ kind: "loading" } & DraftEvidence)
  | { kind: "available"; preview: Schemas["WorkflowDraftPreview"] }
  | ({ kind: "error"; message: string } & DraftEvidence);

export type PredicateOperand = { kind: "reference"; ref: string } | { kind: "literal"; value: JsonValue };
export type WorkflowPredicate =
  | { kind: "constant"; value: boolean }
  | { kind: "comparison"; operator: "eq" | "ne" | "lt" | "lte" | "gt" | "gte"; left: PredicateOperand; right: PredicateOperand }
  | { kind: "present"; ref: string }
  | { kind: "group"; operator: "all" | "any"; predicates: WorkflowPredicate[] }
  | { kind: "not"; predicate: WorkflowPredicate }
  | { kind: "unsupported"; raw: JsonValue };
export type CheckpointConfig =
  | { mode: "none" | "acknowledge" }
  | { mode: "approve"; maxRevisions?: number }
  | { mode: "approve_on_change"; when: WorkflowPredicate; maxRevisions?: number }
  | { mode: "external"; externalCondition: string };
export type NodeExecutor =
  | { type:"extension"; ref:{id:string;version:string;digest:string}; configuration:JsonObject }
  | {type:"workspace_prepare"; repositoryInput:string; checkout:{mode:"current_checkout"}|{mode:"new_worktree";baseRef:string;branch:string}}
  | {type:"workspace_validate";workspaceInput:string;checks:string[][]}
  | { type: "implementation"; taskInput: string; workspaceInput?: string; instructions: string }
  | { type: "reasoning"; agent: string; instructions?: string; skills: string[]; tools: string[] }
  | { type: "gate"; policy: string; condition: WorkflowPredicate }
  | { type: "command"; argv: string[]; cwd?: string; timeoutSeconds?: number }
  | { type: "approval"; actor: string; externalCondition?: string; evidenceOutput?: string }
  | { type: "subworkflow"; workflow: { name: string; version: string; digest?: string; path?: string }; entry: string; terminals: string[]; inputs: Record<string, string>; outputs: Record<string, string> }
  | { type: "point_execution"; planInput: string; approval: "none" | "every" | "risk" | "combined"; riskTags: string[]; validation: "each" | "combined" | "each_and_combined"; publishing: "after_story_validation" | "after_each_point" }
  | { type: "routing"; agent: string; branches: Array<{ name: string; transition: string }>; routeOutput: string; rationaleOutput: string; adviceOutput: string; missingInformationOutput: string; assumptionsOutput: string; confirmationOutput: string };
export type ValueType = `schema:${string}` | "null" | "boolean" | "integer" | "number" | "string" | "array" | "object" | "task" | "repository" | "workspace" | "template" | "markdown" | "open_items" | "decision_log";
export interface BindingConfig { id: string; from: string; pointer?: string; type: ValueType; required: boolean; default?: JsonValue; description?: string; raw?: JsonObject }
export interface OutputConfig { id: string; type: ValueType; schema?: string; description?: string; required: boolean; raw?: JsonObject }
export type ValidatorConfig = {kind:"extension"; extension:Omit<Extract<NodeExecutor,{type:"extension"}>,"type">; raw?:JsonObject} | { kind: "schema"; output: string; schema: string; raw?: JsonObject } | { kind: "command"; command: string[]; raw?: JsonObject };
export interface RetryConfig { maxAttempts: number; on: Array<"provider_unavailable" | "provider_rate_limit" | "process_failure" | "validator_failure" | "timeout" | "interrupted">; raw?: JsonObject }
export interface ReadinessConfig {
  recommendedEvidence: Array<{ role: string; description: string; raw?: JsonObject }>;
  policyGates: Array<{ policy: string; enforcement: "advisory" | "blocking" | "external"; description: string; raw?: JsonObject }>;
  invariants: string[];
  remedies: Array<{ code: string; target: string; action: "supply_input" | "revise_artifact" | "clarify_decision" | "install_capability" | "rerun_validation"; description: string; raw?: JsonObject }>;
  raw?: JsonObject;
}
export type NodeDefinitionRef = {scope:"built_in";name:string;version:string;digest:string}|{scope:"project"|"user";owner:string;name:string;version:string;digest:string};
export interface AuthoringNode { id: string; type: WorkflowNodeType; displayName: string; entry: boolean; terminal: boolean; definition?: NodeDefinitionRef; checkpoint: CheckpointConfig; executor: NodeExecutor; inputs: BindingConfig[]; outputs: OutputConfig[]; validators: ValidatorConfig[]; retry?: RetryConfig; permissions: string[]; readiness?: ReadinessConfig; transitionMode: "exclusive" | "fanout"; join?: { mode: "one" | "all"; from: string[] } }
export type SharedNodeChange =
  | { kind: "inputs"; value: BindingConfig[] }
  | { kind: "outputs"; value: OutputConfig[] }
  | { kind: "validators"; value: ValidatorConfig[] }
  | { kind: "retry"; value?: RetryConfig }
  | { kind: "permissions"; value: string[] }
  | { kind: "readiness"; value?: ReadinessConfig }
  | { kind: "transition_mode"; value: "exclusive" | "fanout" }
  | { kind: "join"; value?: { mode: "one" | "all"; from: string[] } };
export interface TransitionConfig { compositeId: string; index: number; from: string; id: string; to: string; kind: "normal" | "bounded"; when?: WorkflowPredicate; maxTraversals?: number; enabledByDefault: boolean }
export type NodeRenamePreview = { kind: "invalid"; message: string } | { kind: "ready"; from: string; to: string; references: number };
export type NodeRemovalPreview = { kind: "blocked"; message: string; references: string[] } | { kind: "ready"; nodeId: string; incidentTransitionIds: string[] };

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
export interface VisualEdge { id: string; transitionId: string; from: string; to: string; kind: WorkflowEdgeKind; conditional: boolean; maxTraversals?: number }
export interface EditorGraph { nodes: VisualNode[]; edges: VisualEdge[]; entry?: string; terminals?: string[]; bindings?: {source:string;target:string}[] }

const nodeTypes: readonly WorkflowNodeType[] = ["extension", "workspace_prepare", "workspace_validate", "reasoning", "implementation", "gate", "command", "approval", "subworkflow", "point_execution", "routing"];
const identifier = /^[a-z][a-z0-9_]{0,63}$/;
const workflowName = /^[a-z][a-z0-9._/-]{0,127}$/;
const semanticVersion = /^[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?$/;
const outputSource = /^node\.[a-z][a-z0-9_]{0,63}\.output\.[a-z][a-z0-9_]{0,63}$/;

export function isNodeType(value: unknown): value is WorkflowNodeType { return typeof value === "string" && nodeTypes.includes(value as WorkflowNodeType); }
export function nodeTypeAvailability(type: WorkflowNodeType, catalog?: Schemas["WorkflowAuthoringCatalog"]): { available: boolean; message?: string } {
  if (type === "reasoning") return catalog?.agents.status === "known" && catalog.agents.items.length > 0 ? { available: true } : { available: false, message: "Agent profiles are unavailable; existing configured nodes remain preserved." };
  if (type === "gate") return catalog?.policies.status === "known" && catalog.policies.items.length > 0 ? { available: true } : { available: false, message: "Gate policies are unavailable; existing configured nodes remain preserved." };
  if (type === "subworkflow") return catalog?.workflows.status === "known" && catalog.workflows.items.length > 0 ? { available: true } : { available: false, message: "No active installed sub-workflow is available." };
  return { available: true };
}
export function nodeExecutorComplete(value: NodeExecutor): boolean {
  switch (value.type) {
    case "extension": return /^[a-z][a-z0-9.-]*\/[a-z][a-z0-9._-]*$/.test(value.ref.id) && semanticVersion.test(value.ref.version) && /^[a-f0-9]{64}$/.test(value.ref.digest);
    case "reasoning": return value.agent.trim() !== "";
    case "workspace_prepare": return identifier.test(value.repositoryInput) && (value.checkout.mode === "current_checkout" || Boolean(value.checkout.baseRef.trim() && value.checkout.branch.trim()));
    case "workspace_validate": return identifier.test(value.workspaceInput) && value.checks.length > 0 && value.checks.every(argv=>argv.length>0 && Boolean(argv[0].trim()));
    case "implementation": return identifier.test(value.taskInput) && (!value.workspaceInput || identifier.test(value.workspaceInput));
    case "gate": return value.policy.trim() !== "" && value.condition.kind !== "unsupported";
    case "command": return value.argv.length > 0 && value.argv.every((item) => item.trim() !== "");
    case "approval": return value.actor.trim() !== "" && (value.actor !== "external" || Boolean(value.externalCondition?.trim()) && identifier.test(value.evidenceOutput ?? ""));
    case "subworkflow": return workflowName.test(value.workflow.name) && semanticVersion.test(value.workflow.version) && /^[0-9a-f]{64}$/.test(value.workflow.digest ?? "")
      && (value.workflow.path === undefined || value.workflow.path.trim() !== "") && identifier.test(value.entry) && value.terminals.length > 0
      && value.terminals.every((item) => identifier.test(item)) && new Set(value.terminals).size === value.terminals.length
      && Object.entries(value.inputs).every(([child, parent]) => identifier.test(child) && identifier.test(parent))
      && Object.entries(value.outputs).every(([parent, source]) => identifier.test(parent) && outputSource.test(source));
    case "point_execution": return identifier.test(value.planInput) && (value.approval !== "risk" || value.riskTags.length > 0 && value.riskTags.every((item) => item.trim() !== ""));
    case "routing": return value.agent.trim() !== "" && value.branches.length > 0 && value.branches.every((branch) => identifier.test(branch.name) && identifier.test(branch.transition)) && new Set(value.branches.map((branch) => branch.name)).size === value.branches.length && new Set(value.branches.map((branch) => branch.transition)).size === value.branches.length && [value.routeOutput,value.rationaleOutput,value.adviceOutput,value.missingInformationOutput,value.assumptionsOutput,value.confirmationOutput].every((id)=>identifier.test(id));
  }
}
export function edgeIdentity(from: string, transitionId: string, to: string) { return `${from}\u001f${transitionId}\u001f${to}`; }

export function createStarterDocument(name: string): JsonObject {
  return {
    apiVersion: "darkstar.local/v1alpha2",
    kind: "Workflow",
    metadata: { name, version: "0.1.0", displayName: humanizeIdentifier(name.split("/").at(-1) || name) },
    spec: {
      routeDefaults: { entry: "start", terminals: ["start"] },
      nodes: { start: createNode("command", "Start", { entry: true, terminal: true }) },
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
  const rowHeight = Math.max(220, ...Object.values(nodesRecord).map((raw) => 220 + 28 * Math.max(Object.keys(record(record(raw).inputs)).length, Object.keys(record(record(raw).outputs)).length)));
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
      position: layout.nodes[id] ?? { x: 72 + (index % 3) * 330, y: 64 + Math.floor(index / 3) * rowHeight },
    });
    const transitions = Array.isArray(node.transitions) ? node.transitions : [];
    for (const rawTransition of transitions) {
      const transition = record(rawTransition);
      if (typeof transition.id !== "string" || typeof transition.to !== "string") continue;
      if (transitions.map(record).filter((candidate) => candidate.id === transition.id && candidate.to === transition.to).length !== 1) continue;
      const kind: WorkflowEdgeKind = transition.kind === "bounded" ? "bounded_repair" : transition.when ? "conditional" : type === "subworkflow" ? "subworkflow" : "normal";
      edges.push({ id: edgeIdentity(id, transition.id, transition.to), transitionId: transition.id, from: id, to: transition.to, kind, conditional: transition.when !== undefined, ...(typeof transition.maxTraversals === "number" ? { maxTraversals: transition.maxTraversals } : {}) });
    }
    index += 1;
  }
  return { nodes, edges, terminals: stringArray(record(record(record(document).spec).routeDefaults).terminals), entry: String(record(record(record(document).spec).routeDefaults).entry ?? ""), bindings: Object.entries(workflowNodes(record(document))).flatMap(([id,raw]) => Object.values(record(record(raw).inputs)).map(binding=>({source:String(record(binding).from),target:id}))) };
}

export function addNode(document: JsonObject, type: WorkflowNodeType, requestedId?: string): { document: JsonObject; nodeId: string } {
  const next = clone(document); const nodes = workflowNodes(next);
  if (type === "routing" || type === "implementation" || type === "workspace_prepare" || type === "workspace_validate") next.apiVersion = "darkstar.local/v1alpha3";
  const base = sanitizeIdentifier(requestedId || type); const nodeId = uniqueIdentifier(base, new Set(Object.keys(nodes)));
  nodes[nodeId] = createNode(type, humanizeIdentifier(nodeId));
  if (type === "implementation") {
    const spec = record(next.spec), inputs = record(spec.inputs);
    const task = Object.keys(inputs).find(id => record(inputs[id]).type === "task") ?? uniqueIdentifier("task", new Set(Object.keys(inputs)));
    if (!inputs[task]) inputs[task] = { type: "task", resource: { kind: "task" } };
    spec.inputs = inputs; const node = record(nodes[nodeId]); node.inputs = {workspace:{type:"workspace",from:"node.prepare.output.workspace",required:true}, task: { from: `run.input.${task}`, type: "task", required: true } };
  }
  if (type === "workspace_prepare") {const spec=record(next.spec),inputs=record(spec.inputs); const repository=Object.keys(inputs).find(id=>record(inputs[id]).type==="repository")??uniqueIdentifier("repository",new Set(Object.keys(inputs)));if(!inputs[repository])inputs[repository]={type:"repository",resource:{kind:"repository"}};spec.inputs=inputs;record(nodes[nodeId]).inputs={repository:{type:"repository",from:`run.input.${repository}`}};}
  if (type === "point_execution") {
    const spec = record(next.spec); const inputs = record(spec.inputs); const planInput = uniqueIdentifier("plan", new Set(Object.keys(inputs)));
    const point = record(nodes[nodeId]); point.inputs = { [planInput]: { from: `run.input.${planInput}`, type: "object", required: true } }; point.points = { ...record(point.points), planInput };
    inputs[planInput] = { type: "object", description: "Implementation point plan" }; spec.inputs = inputs;
  }
  return { document: next, nodeId };
}

export function useNodeDefinition(document: JsonObject, nodeId: string, definition: NodeDefinitionRef): JsonObject {
  const next = clone(document); const node = record(workflowNodes(next)[nodeId]);
  if (!Object.keys(node).length || !decodeNodeDefinitionRef(definition)) return next;
  next.apiVersion = "darkstar.local/v1alpha3";
  node.definition = clone(definition);
  return next;
}

export function removeNode(document: JsonObject, nodeId: string): JsonObject {
  const preview = previewNodeRemoval(document, nodeId); const next = clone(document); const nodes = workflowNodes(next);
  if (preview.kind !== "ready") return next;
  delete nodes[nodeId];
  for (const raw of Object.values(nodes)) {
    const node = record(raw);
    if (Array.isArray(node.transitions)) node.transitions = node.transitions.filter((candidate) => record(candidate).to !== nodeId);
  }
  return next;
}

export function previewNodeRemoval(document: unknown, nodeId: string): NodeRemovalPreview {
  const nodes = workflowNodes(document); if (!(nodeId in nodes)) return { kind: "blocked", message: `Node ${nodeId} does not exist.`, references: [] };
  if (Object.keys(nodes).length <= 1) return { kind: "blocked", message: "A workflow must retain at least one node.", references: ["spec.nodes"] };
  const references: string[] = []; const defaults = routeDefaults(document);
  if (defaults.entry === nodeId) references.push("spec.routeDefaults.entry");
  if (Array.isArray(defaults.terminals) && defaults.terminals.includes(nodeId)) references.push("spec.routeDefaults.terminals");
  for (const [profileID, profileValue] of Object.entries(record(record(record(document).spec).profiles))) { const profile = record(profileValue); if (profile.entry === nodeId) references.push(`spec.profiles.${profileID}.entry`); if (Array.isArray(profile.terminals) && profile.terminals.includes(nodeId)) references.push(`spec.profiles.${profileID}.terminals`); }
  const incident = new Set<string>();
  for (const [candidateID, candidateValue] of Object.entries(nodes)) { const candidate = record(candidateValue); for (const transitionValue of Array.isArray(candidate.transitions) ? candidate.transitions as unknown[] : []) { const transition = record(transitionValue); if ((candidateID === nodeId || transition.to === nodeId) && typeof transition.id === "string") incident.add(transition.id); } for (const bindingValue of Object.values(record(candidate.inputs))) if (typeof record(bindingValue).from === "string" && String(record(bindingValue).from).startsWith(`node.${nodeId}.output.`)) references.push(`spec.nodes.${candidateID}.inputs`); for (const remedyValue of Array.isArray(record(candidate.readiness).remedies) ? record(candidate.readiness).remedies as unknown[] : []) if (record(remedyValue).target === nodeId) references.push(`spec.nodes.${candidateID}.readiness.remedies`); }
  for (const [candidateID, candidateValue] of Object.entries(nodes)) { const join = record(record(candidateValue).join); if (Array.isArray(join.from) && join.from.some((id) => incident.has(String(id)))) references.push(`spec.nodes.${candidateID}.join.from`); }
  if (references.length) return { kind: "blocked", message: `Remove ${references.length} authoritative reference${references.length === 1 ? "" : "s"} before deleting ${nodeId}.`, references: [...new Set(references)].sort() };
  return { kind: "ready", nodeId, incidentTransitionIds: [...incident].sort() };
}

export function previewNodeRename(document: unknown, from: string, to: string, layoutValue?: unknown): NodeRenamePreview {
  const nodes = workflowNodes(document);
  if (!identifier.test(to)) return { kind: "invalid", message: "Node IDs must start with a letter and contain only lowercase letters, numbers, and underscores." };
  if (!(from in nodes)) return { kind: "invalid", message: `Node ${from} no longer exists.` };
  if (from !== to && to in nodes) return { kind: "invalid", message: `Node ${to} already exists.` };
  let references = 1;
  const rawDocument = record(document); const defaults = record(record(rawDocument.spec).routeDefaults);
  if (defaults.entry === from) references += 1;
  if (Array.isArray(defaults.terminals)) references += defaults.terminals.filter((value) => value === from).length;
  for (const profileValue of Object.values(record(record(rawDocument.spec).profiles))) { const profile = record(profileValue); if (profile.entry === from) references += 1; if (Array.isArray(profile.terminals)) references += profile.terminals.filter((value) => value === from).length; }
  for (const rawNode of Object.values(nodes)) {
    const node = record(rawNode);
    if (Array.isArray(node.transitions)) references += node.transitions.filter((value) => record(value).to === from).length;
    references += Object.values(record(node.inputs)).filter((value) => typeof record(value).from === "string" && String(record(value).from).startsWith(`node.${from}.output.`)).length;
    const readiness = record(node.readiness); if (Array.isArray(readiness.remedies)) references += readiness.remedies.filter((value) => record(value).target === from).length;
  }
  if (normalizeLayout(layoutValue).nodes[from]) references += 1;
  return { kind: "ready", from, to, references };
}

export function renameNode(document: JsonObject, layoutValue: unknown, from: string, to: string): { document: JsonObject; layout: WorkflowLayout; preview: NodeRenamePreview } {
  const preview = previewNodeRename(document, from, to, layoutValue); const layout = normalizeLayout(layoutValue);
  if (preview.kind !== "ready" || from === to) return { document: clone(document), layout, preview };
  const next = clone(document); const nodes = workflowNodes(next); const entries = Object.entries(nodes).map(([id, value]) => [id === from ? to : id, value] as const); record(next.spec).nodes = Object.fromEntries(entries);
  const defaults = routeDefaults(next); if (defaults.entry === from) defaults.entry = to; if (Array.isArray(defaults.terminals)) defaults.terminals = defaults.terminals.map((value) => value === from ? to : value);
  for (const profileValue of Object.values(record(record(next.spec).profiles))) { const profile = record(profileValue); if (profile.entry === from) profile.entry = to; if (Array.isArray(profile.terminals)) profile.terminals = profile.terminals.map((value) => value === from ? to : value); }
  for (const rawNode of Object.values(workflowNodes(next))) {
    const node = record(rawNode);
    if (Array.isArray(node.transitions)) for (const transitionValue of node.transitions) { const transition = record(transitionValue); if (transition.to === from) transition.to = to; }
    for (const bindingValue of Object.values(record(node.inputs))) { const binding = record(bindingValue); if (typeof binding.from === "string" && binding.from.startsWith(`node.${from}.output.`)) binding.from = `node.${to}.output.${binding.from.slice(`node.${from}.output.`.length)}`; }
    const readiness = record(node.readiness); if (Array.isArray(readiness.remedies)) for (const remedyValue of readiness.remedies) { const remedy = record(remedyValue); if (remedy.target === from) remedy.target = to; }
  }
  const positions = { ...layout.nodes }; if (positions[from]) { positions[to] = positions[from]; delete positions[from]; }
  return { document: next, layout: { ...layout, nodes: positions }, preview };
}

export function connectNodes(document: JsonObject, from: string, to: string, kind: AuthoredTransitionKind = "normal"): JsonObject {
  const next = clone(document); const nodes = workflowNodes(next);
  const source = record(nodes[from]);
  if (!nodes[from] || !nodes[to] || from === to && kind !== "bounded_repair") return next;
  const transitions = Array.isArray(source.transitions) ? source.transitions : [];
  const used = transitionIDs(nodes);
  const id = uniqueIdentifier(sanitizeIdentifier(`${from}_to_${to}`), used);
  const transition: JsonObject = { id, to };
  if (kind === "conditional") transition.when = { const: true };
  if (kind === "bounded_repair") { transition.kind = "bounded"; transition.maxTraversals = 1; transition.when = { const: true }; }
  source.transitions = [...transitions, transition];
  return next;
}

export function inspectNode(document: unknown, nodeId: string): AuthoringNode | undefined {
  const raw = record(workflowNodes(document)[nodeId]);
  const checkpoint = decodeCheckpoint(raw.checkpoint);
  if (!isNodeType(raw.type) || !supportedSharedSections(raw) || !supportedExecutor(raw) || !checkpoint) return undefined;
  const common = {
    id: nodeId,
    type: raw.type,
    displayName: typeof raw.displayName === "string" ? raw.displayName : humanizeIdentifier(nodeId),
    entry: raw.entry === true,
    terminal: raw.terminal === true,
    ...(decodeNodeDefinitionRef(raw.definition)?{definition:decodeNodeDefinitionRef(raw.definition)}:{}),
    checkpoint,
    inputs: decodeBindings(raw.inputs),
    outputs: decodeOutputs(raw.outputs),
    validators: decodeValidators(raw.validators),
    ...(decodeRetry(raw.retry) ? { retry: decodeRetry(raw.retry) } : {}),
    permissions: stringArray(raw.permissions),
    ...(decodeReadiness(raw.readiness) ? { readiness: decodeReadiness(raw.readiness) } : {}),
    transitionMode: raw.transitionMode === "fanout" ? "fanout" as const : "exclusive" as const,
    ...(decodeJoin(raw.join) ? { join: decodeJoin(raw.join) } : {}),
  };
  switch (raw.type) {
    case "extension": { const value=record(raw.extension); const ref=record(value.ref); return {...common,executor:{type:"extension",ref:{id:stringValue(ref.id),version:stringValue(ref.version),digest:stringValue(ref.digest)},configuration:clone(record(value.configuration))}}; }
    case "reasoning": {
      const value = record(raw.reasoning);
      return { ...common, executor: { type: "reasoning", agent: stringValue(value.agent), instructions:stringValue(value.instructions), skills: stringArray(value.skills), tools: stringArray(value.tools) } };
    }
    case "gate": {
      const value = record(raw.gate);
      return { ...common, executor: { type: "gate", policy: stringValue(value.policy), condition: decodePredicate(value.condition) } };
    }
    case "command": {
      const value = record(raw.command);
      return { ...common, executor: { type: "command", argv: stringArray(value.argv), ...(typeof value.cwd === "string" ? { cwd: value.cwd } : {}), ...(positiveInteger(value.timeoutSeconds) ? { timeoutSeconds: value.timeoutSeconds as number } : {}) } };
    }
    case "approval": {
      const value = record(raw.approval);
      return { ...common, executor: { type: "approval", actor: stringValue(value.actor), ...(typeof value.externalCondition === "string" ? { externalCondition: value.externalCondition } : {}), ...(typeof value.evidenceOutput === "string" ? { evidenceOutput: value.evidenceOutput } : {}) } };
    }
    case "subworkflow": {
      const value = record(raw.call); const workflow = record(value.workflow);
      return { ...common, executor: { type: "subworkflow", workflow: { name: stringValue(workflow.name), version: stringValue(workflow.version), ...(typeof workflow.digest === "string" ? { digest: workflow.digest } : {}), ...(typeof workflow.path === "string" ? { path: workflow.path } : {}) }, entry: stringValue(value.entry), terminals: stringArray(value.terminals), inputs: stringRecord(value.inputs), outputs: stringRecord(value.outputs) } };
    }
    case "workspace_prepare": { const value=record(raw.workspacePrepare); return {...common,executor:{type:"workspace_prepare",repositoryInput:stringValue(value.repositoryInput),checkout:clone(value.checkout) as Extract<NodeExecutor,{type:"workspace_prepare"}>["checkout"]}}; }
    case "workspace_validate": {const value=record(raw.workspaceValidate);return {...common,executor:{type:"workspace_validate",workspaceInput:stringValue(value.workspaceInput),checks:clone(value.checks) as string[][]}};}
    case "implementation": { const value = record(raw.implementation); return { ...common, executor: { type: "implementation", taskInput: stringValue(value.taskInput), instructions: stringValue(value.instructions), ...(typeof value.workspaceInput === "string" ? {workspaceInput:value.workspaceInput} : {}) } }; }
    case "point_execution": {
      const value = record(raw.points);
      return { ...common, executor: { type: "point_execution", planInput: stringValue(value.planInput), approval: enumValue(value.approval, ["none", "every", "risk", "combined"], "none"), riskTags: stringArray(value.riskTags), validation: enumValue(value.validation, ["each", "combined", "each_and_combined"], "combined"), publishing: enumValue(value.publishing, ["after_story_validation", "after_each_point"], "after_story_validation") } };
    }
    case "routing": { const value=record(raw.routing); return { ...common, executor:{ type:"routing", agent:stringValue(value.agent), branches:(Array.isArray(value.branches)?value.branches:[]).map(record).map((branch)=>({name:stringValue(branch.name),transition:stringValue(branch.transition)})), routeOutput:stringValue(value.routeOutput), rationaleOutput:stringValue(value.rationaleOutput), adviceOutput:stringValue(value.adviceOutput), missingInformationOutput:stringValue(value.missingInformationOutput), assumptionsOutput:stringValue(value.assumptionsOutput), confirmationOutput:stringValue(value.confirmationOutput) } }; }
  }
}

export function inspectTransition(document: unknown, compositeId: string): TransitionConfig | undefined {
  const [from, id, to] = compositeId.split("\u001f");
  const source = record(workflowNodes(document)[from]);
  const transitions = (Array.isArray(source.transitions) ? source.transitions : []).map(record); const matches = transitions.filter((candidate) => candidate.id === id && candidate.to === to);
  if (matches.length !== 1) return undefined;
  const raw = matches[0];
  if (!identifier.test(id) || !identifier.test(to) || raw.kind !== undefined && raw.kind !== "normal" && raw.kind !== "bounded" || raw.enabledByDefault !== undefined && typeof raw.enabledByDefault !== "boolean") return undefined;
  if (raw.kind === "bounded" ? !positiveInteger(raw.maxTraversals) || Number(raw.maxTraversals) > 10_000 : raw.maxTraversals !== undefined) return undefined;
  return { compositeId, index: transitions.indexOf(raw), from, id, to, kind: raw.kind === "bounded" ? "bounded" : "normal", ...(raw.when !== undefined ? { when: decodePredicate(raw.when) } : {}), ...(positiveInteger(raw.maxTraversals) ? { maxTraversals: raw.maxTraversals as number } : {}), enabledByDefault: raw.enabledByDefault !== false };
}

export function updateNodeExecutor(document: JsonObject, nodeId: string, executor: NodeExecutor): JsonObject {
  const next = clone(document); const node = record(workflowNodes(next)[nodeId]);
  if (node.type !== executor.type) return next;
  switch (executor.type) {
    case "extension": node.extension={ref:{...executor.ref},configuration:clone(executor.configuration)}; next.apiVersion="darkstar.local/v1alpha3"; break;
    case "reasoning": node.reasoning = { ...record(node.reasoning), agent: executor.agent, instructions:executor.instructions ?? "", skills: uniqueStrings(executor.skills), tools: uniqueStrings(executor.tools) }; break;
    case "workspace_prepare": node.workspacePrepare={repositoryInput:executor.repositoryInput,checkout:clone(executor.checkout)};break;
    case "workspace_validate":node.workspaceValidate={workspaceInput:executor.workspaceInput,checks:clone(executor.checks)};break;
    case "implementation": node.implementation = { taskInput: executor.taskInput, instructions: executor.instructions, ...(executor.workspaceInput ? {workspaceInput:executor.workspaceInput} : {}) }; break;
    case "gate": node.gate = { ...record(node.gate), policy: executor.policy, condition: encodePredicate(executor.condition) }; break;
    case "command": node.command = { ...record(node.command), argv: executor.argv, ...(executor.cwd ? { cwd: executor.cwd } : {}), ...(executor.timeoutSeconds ? { timeoutSeconds: executor.timeoutSeconds } : {}) }; removeEmptyOptional(record(node.command), "cwd", executor.cwd); removeEmptyOptional(record(node.command), "timeoutSeconds", executor.timeoutSeconds); break;
    case "approval": {
      node.approval = { ...record(node.approval), actor: executor.actor };
      const value = record(node.approval);
      if (executor.actor === "external") {
        value.externalCondition = executor.externalCondition ?? "condition/name"; value.evidenceOutput = executor.evidenceOutput || "approval_evidence";
        const outputs = record(node.outputs); outputs[String(value.evidenceOutput)] = { ...record(outputs[String(value.evidenceOutput)]), type: "object", schema: "darkstar/approval-evidence/v1" }; node.outputs = outputs;
      }
      else { delete value.externalCondition; delete value.evidenceOutput; }
      break;
    }
    case "subworkflow": node.call = { ...record(node.call), workflow: { ...record(record(node.call).workflow), ...executor.workflow }, entry: executor.entry, terminals: uniqueStrings(executor.terminals), inputs: { ...executor.inputs }, outputs: { ...executor.outputs } }; break;
    case "point_execution": {
      if (!identifier.test(executor.planInput)) break;
      const inputs = record(node.inputs); const selected = record(inputs[executor.planInput]); if (!isPlanValueType(selected.type)) break;
      node.points = { ...record(node.points), planInput: executor.planInput, approval: executor.approval, riskTags: executor.approval === "risk" ? uniqueStrings(executor.riskTags) : [], validation: executor.validation, publishing: executor.publishing };
      break;
    }
    case "routing": node.routing={...record(node.routing),agent:executor.agent,branches:executor.branches.map((branch)=>({...branch})),routeOutput:executor.routeOutput,rationaleOutput:executor.rationaleOutput,adviceOutput:executor.adviceOutput,missingInformationOutput:executor.missingInformationOutput,assumptionsOutput:executor.assumptionsOutput,confirmationOutput:executor.confirmationOutput}; break;
  }
  return next;
}

export function updateNodeCheckpoint(document: JsonObject, nodeId: string, checkpoint: CheckpointConfig): JsonObject {
  const next = clone(document); const node = record(workflowNodes(next)[nodeId]);
  node.checkpoint = encodeCheckpoint(checkpoint);
  return next;
}

export function updateNodeShared(document: JsonObject, nodeId: string, change: SharedNodeChange): JsonObject {
  const next = clone(document); const node = record(workflowNodes(next)[nodeId]);
  switch (change.kind) {
    case "inputs": if (validUniqueIDs(change.value.map((value) => value.id))) node.inputs = Object.fromEntries(change.value.map((value) => [value.id, encodeBinding(value)])); break;
    case "outputs": if (validUniqueIDs(change.value.map((value) => value.id))) node.outputs = Object.fromEntries(change.value.map((value) => [value.id, encodeOutput(value)])); break;
    case "validators": { const outputs = new Set(Object.keys(record(node.outputs))); if (change.value.every((value) => value.kind === "extension" ? nodeExecutorComplete({type:"extension",...value.extension}) : value.kind === "command" ? value.command.length > 0 : outputs.has(value.output) && value.schema.trim() !== "")) node.validators = change.value.map((value) => value.kind === "extension" ? {extension:clone(value.extension)} : value.kind === "schema" ? { ...value.raw, output: value.output, schema: value.schema } : { ...value.raw, command: value.command }); break; }
    case "retry": if (change.value) node.retry = { ...change.value.raw, maxAttempts: clampInteger(change.value.maxAttempts, 1, 100), on: uniqueStrings(change.value.on) }; else delete node.retry; break;
    case "permissions": node.permissions = uniqueStrings(change.value); break;
    case "readiness": if (change.value) node.readiness = encodeReadiness(change.value); else delete node.readiness; break;
    case "transition_mode": node.transitionMode = change.value; break;
    case "join": if (change.value && uniqueStrings(change.value.from).length >= 2) node.join = { mode: change.value.mode, from: uniqueStrings(change.value.from) }; else if (!change.value) delete node.join; break;
  }
  if (JSON.stringify(next) === JSON.stringify(document) || !inspectNode(next, nodeId)) return document;
  return next;
}

export function updateTransition(document: JsonObject, compositeId: string, change: { id?: string; to?: string; kind?: "normal" | "bounded"; when?: WorkflowPredicate | null; maxTraversals?: number; enabledByDefault?: boolean }): { document: JsonObject; edgeId: string } {
  const next = clone(document); const [from, originalId, originalTo] = compositeId.split("\u001f"); const source = record(workflowNodes(next)[from]);
  const transitions = Array.isArray(source.transitions) ? source.transitions : [];
  const matches = transitions.map(record).filter((candidate) => candidate.id === originalId && candidate.to === originalTo);
  if (matches.length !== 1) return { document: next, edgeId: compositeId };
  const raw = matches[0];
  const requestedID = change.id === undefined ? originalId : change.id;
  if (!identifier.test(requestedID) || requestedID !== originalId && transitionIDs(workflowNodes(next)).has(requestedID)) return { document: clone(document), edgeId: compositeId };
  const id = requestedID;
  const to = change.to === undefined || !(change.to in workflowNodes(next)) ? originalTo : change.to;
  if (to !== originalTo) repairJoin(record(workflowNodes(next)[originalTo]), new Set([originalId]));
  raw.id = id; raw.to = to;
  if (change.kind !== undefined) {
    raw.kind = change.kind;
    if (change.kind === "bounded") raw.maxTraversals = change.maxTraversals ?? (positiveInteger(raw.maxTraversals) ? raw.maxTraversals : 1);
    else delete raw.maxTraversals;
  } else if (change.maxTraversals !== undefined && raw.kind === "bounded") raw.maxTraversals = Math.max(1, Math.min(10_000, Math.round(change.maxTraversals)));
  if (change.when === null) delete raw.when; else if (change.when !== undefined) raw.when = encodePredicate(change.when);
  if (change.enabledByDefault !== undefined) raw.enabledByDefault = change.enabledByDefault;
  if (id !== originalId) for (const node of Object.values(workflowNodes(next))) { const join = record(record(node).join); if (Array.isArray(join.from)) join.from = join.from.map((value) => value === originalId ? id : value); }
  return { document: next, edgeId: edgeIdentity(from, id, to) };
}

export function removeEdge(document: JsonObject, compositeId: string): JsonObject {
  const next = clone(document); const [from, transitionId, to] = compositeId.split("\u001f");
  const source = record(workflowNodes(next)[from]);
  if (Array.isArray(source.transitions)) source.transitions = source.transitions.filter((candidate) => {
    const transition = record(candidate); return transition.id !== transitionId || transition.to !== to;
  });
  for (const node of Object.values(workflowNodes(next))) repairJoin(record(node), new Set([transitionId]));
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
export function removeNodeLayout(layoutValue: unknown, nodeId: string): WorkflowLayout { const layout = normalizeLayout(layoutValue); const nodes = { ...layout.nodes }; delete nodes[nodeId]; return { ...layout, nodes }; }

export function draftRevision(state: PersistenceState) { return state.revision; }
export function persistenceLabel(state: PersistenceState) {
  switch (state.kind) {
    case "clean": return "Saved"; case "dirty": return "Unsaved changes"; case "saving": return "Saving…";
    case "conflict": return "Save conflict"; case "offline": return "Offline · changes retained"; case "error": return "Save failed · changes retained";
  }
}

export function validationMatches(state: ValidationState, evidence: DraftEvidence): state is Extract<ValidationState, { kind: "valid" }> {
  return state.kind === "valid" && state.draftId === evidence.draftId && state.documentDigest === evidence.documentDigest;
}

export function findingTarget(document: unknown, finding: Schemas["WorkflowAuthoringFinding"]): { selection: EditorSelection; field?: string } {
  if (finding.nodeId) {
    const transitions = Array.isArray(record(workflowNodes(document)[finding.nodeId]).transitions) ? record(workflowNodes(document)[finding.nodeId]).transitions as unknown[] : [];
    const match = /^transitions\.(\d+)(?:\.|$)/.exec(finding.field ?? "");
    const indexed = match ? record(transitions[Number(match[1])]) : undefined;
    if (indexed && typeof indexed.id === "string" && typeof indexed.to === "string") { const matches = transitions.map(record).filter((transition) => transition.id === indexed.id && transition.to === indexed.to); if (matches.length === 1) return { selection: { kind: "edge", edgeId: edgeIdentity(finding.nodeId, indexed.id, indexed.to) }, field: finding.field }; return { selection: { kind: "node", nodeId: finding.nodeId }, field: finding.field }; }
    if (finding.edgeId) {
      const raw = transitions.map(record).find((transition) => transition.id === finding.edgeId);
      if (raw && typeof raw.id === "string" && typeof raw.to === "string") return { selection: { kind: "edge", edgeId: edgeIdentity(finding.nodeId, raw.id, raw.to) }, field: finding.field };
    }
    return { selection: { kind: "node", nodeId: finding.nodeId }, field: finding.field };
  }
  return { selection: { kind: "none" }, field: finding.field };
}

export function defaultPredicate(): WorkflowPredicate { return { kind: "constant", value: true }; }

export function encodePredicate(predicate: WorkflowPredicate): unknown {
  switch (predicate.kind) {
    case "constant": return { const: predicate.value };
    case "comparison": return { op: predicate.operator, args: [encodeOperand(predicate.left), encodeOperand(predicate.right)] };
    case "present": return { op: "present", arg: { ref: predicate.ref } };
    case "group": return { op: predicate.operator, args: predicate.predicates.map(encodePredicate) };
    case "not": return { op: "not", arg: encodePredicate(predicate.predicate) };
    case "unsupported": return clone(predicate.raw);
  }
}

export function decodePredicate(value: unknown, depth = 0): WorkflowPredicate {
  if (depth > 32 || !isJsonValue(value)) return { kind: "unsupported", raw: null };
  const raw = record(value);
  if (typeof raw.const === "boolean" && hasOnlyKeys(raw, ["const"])) return { kind: "constant", value: raw.const };
  if (hasOnlyKeys(raw, ["op", "args"]) && ["eq", "ne", "lt", "lte", "gt", "gte"].includes(String(raw.op)) && Array.isArray(raw.args) && raw.args.length === 2) {
    const left = decodeOperand(raw.args[0]); const right = decodeOperand(raw.args[1]);
    if (left && right) return { kind: "comparison", operator: raw.op as "eq" | "ne" | "lt" | "lte" | "gt" | "gte", left, right };
  }
  if (hasOnlyKeys(raw, ["op", "arg"]) && raw.op === "present" && typeof record(raw.arg).ref === "string" && hasOnlyKeys(record(raw.arg), ["ref"])) return { kind: "present", ref: record(raw.arg).ref as string };
  if (hasOnlyKeys(raw, ["op", "args"]) && (raw.op === "all" || raw.op === "any") && Array.isArray(raw.args) && raw.args.length) return { kind: "group", operator: raw.op, predicates: raw.args.map((item) => decodePredicate(item, depth + 1)) };
  if (hasOnlyKeys(raw, ["op", "arg"]) && raw.op === "not" && raw.arg !== undefined) return { kind: "not", predicate: decodePredicate(raw.arg, depth + 1) };
  return { kind: "unsupported", raw: clone(value) };
}

function createNode(type: WorkflowNodeType, displayName: string, flags: { entry?: boolean; terminal?: boolean } = {}): JsonObject {
  const common: JsonObject = { displayName, type, entry: flags.entry ?? false, terminal: flags.terminal ?? false, inputs: {}, outputs: {}, transitions: [] };
  switch (type) {
    case "extension": return {...common,extension:{ref:{id:"custom/operation",version:"1.0.0",digest:""},configuration:{}}};
    case "reasoning": return { ...common, reasoning: { agent: "authoring-agent" } };
    case "gate": return { ...common, outputs: { passed: { type: "boolean" }, gate_evidence: { type: "object" } }, gate: { policy: "authoring-policy", condition: { const: true } } };
    case "workspace_prepare": return {...common,inputs:{repository:{type:"repository",from:"run.input.repository"}},outputs:{workspace:{type:"workspace"}},workspacePrepare:{repositoryInput:"repository",checkout:{mode:"current_checkout"}}};
    case "workspace_validate":return {...common,inputs:{workspace:{type:"workspace",from:"node.prepare.output.workspace"}},outputs:{validation:{type:"object"}},workspaceValidate:{workspaceInput:"workspace",checks:[]}};
    case "implementation": return { ...common, inputs: { task: { from: "run.input.task", type: "task", required: true } }, outputs: { changeset: { type: "object" } }, permissions: ["process.run", "workspace.write"], implementation: { taskInput: "task", workspaceInput:"workspace", instructions: "Perform the requested task in the repository and validate the actual changes." } };
    case "command": return { ...common, command: { argv: ["command"] } };
    case "approval": return { ...common, approval: { actor: "workflow-owner" } };
    case "subworkflow": return { ...common, call: { workflow: { name: "workflow/name", version: "0.1.0" }, entry: "start", terminals: ["finish"], inputs: {}, outputs: {} } };
    case "point_execution": return { ...common, inputs: { plan: { from: "run.input.plan", type: "object", required: true } }, points: { planInput: "plan", approval: "none", riskTags: [], validation: "combined", publishing: "after_story_validation" } };
    case "routing": return { ...common, outputs:{selected_route:{type:"string"},rationale:{type:"string"},advice:{type:"string"},missing_information:{type:"array"},assumptions:{type:"array"},confirmation_required:{type:"boolean"}}, routing:{agent:"authoring-agent",branches:[{name:"assessment",transition:"to_assessment"}],routeOutput:"selected_route",rationaleOutput:"rationale",adviceOutput:"advice",missingInformationOutput:"missing_information",assumptionsOutput:"assumptions",confirmationOutput:"confirmation_required"} };
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
function decodeCheckpoint(value: unknown): CheckpointConfig | undefined {
  if (value === undefined) return { mode: "none" };
  if (value === "none" || value === "acknowledge") return { mode: value };
  if (value === "approve") return { mode: "approve" };
  if (!isPlainRecord(value)) return undefined;
  const raw = record(value);
  if ((raw.mode === "acknowledge" || raw.mode === "none") && hasOnlyKeys(raw, ["mode"])) return { mode: raw.mode };
  if (raw.mode === "approve" && hasOnlyKeys(raw, ["mode", "maxRevisions"]) && (raw.maxRevisions === undefined || nonnegativeInteger(raw.maxRevisions) && Number(raw.maxRevisions) <= 100)) return { mode: "approve", ...(raw.maxRevisions !== undefined ? { maxRevisions: raw.maxRevisions as number } : {}) };
  if (raw.mode === "approve_on_change" && hasOnlyKeys(raw, ["mode", "when", "maxRevisions"]) && raw.when !== undefined && (raw.maxRevisions === undefined || nonnegativeInteger(raw.maxRevisions) && Number(raw.maxRevisions) <= 100)) return { mode: "approve_on_change", when: decodePredicate(raw.when), ...(raw.maxRevisions !== undefined ? { maxRevisions: raw.maxRevisions as number } : {}) };
  if (raw.mode === "external" && hasOnlyKeys(raw, ["mode", "externalCondition"]) && typeof raw.externalCondition === "string" && raw.externalCondition.length > 0) return { mode: "external", externalCondition: raw.externalCondition };
  return undefined;
}
function encodeCheckpoint(value: CheckpointConfig): JsonObject {
  switch (value.mode) {
    case "none": case "acknowledge": return { mode: value.mode };
    case "approve": return { mode: value.mode, ...(value.maxRevisions !== undefined ? { maxRevisions: clampInteger(value.maxRevisions, 0, 100) } : {}) };
    case "approve_on_change": return { mode: value.mode, when: encodePredicate(value.when), ...(value.maxRevisions !== undefined ? { maxRevisions: clampInteger(value.maxRevisions, 0, 100) } : {}) };
    case "external": return { mode: value.mode, externalCondition: value.externalCondition };
  }
}
function encodeOperand(value: PredicateOperand): JsonObject { return value.kind === "reference" ? { ref: value.ref } : { literal: value.value }; }
function decodeOperand(value: unknown): PredicateOperand | undefined { const raw = record(value); if (typeof raw.ref === "string" && Object.keys(raw).length === 1) return { kind: "reference", ref: raw.ref }; return Object.hasOwn(raw, "literal") && isJsonValue(raw.literal) && Object.keys(raw).length === 1 ? { kind: "literal", value: raw.literal } : undefined; }
function isJsonValue(value: unknown): value is JsonValue { if (value === null || typeof value === "boolean" || typeof value === "string" || typeof value === "number" && Number.isFinite(value)) return true; if (Array.isArray(value)) return value.every(isJsonValue); return value !== null && typeof value === "object" && Object.values(value).every(isJsonValue); }
function stringValue(value: unknown) { return typeof value === "string" ? value : ""; }
function stringArray(value: unknown) { return Array.isArray(value) ? value.filter((item): item is string => typeof item === "string") : []; }
function stringRecord(value: unknown) { return Object.fromEntries(Object.entries(record(value)).filter((entry): entry is [string, string] => typeof entry[1] === "string")); }
function decodeNodeDefinitionRef(value: unknown): NodeDefinitionRef|undefined { if(value===undefined)return undefined; const raw=record(value); if((raw.scope!=="built_in"&&raw.scope!=="project"&&raw.scope!=="user")||typeof raw.name!=="string"||!workflowName.test(raw.name)||typeof raw.version!=="string"||!semanticVersion.test(raw.version)||typeof raw.digest!=="string"||!/^[0-9a-f]{64}$/.test(raw.digest))return undefined; if(raw.scope==="built_in")return raw.owner===undefined&&hasOnlyKeys(raw,["scope","name","version","digest"])?raw as NodeDefinitionRef:undefined; return typeof raw.owner==="string"&&raw.owner.length>0&&hasOnlyKeys(raw,["scope","owner","name","version","digest"])?raw as NodeDefinitionRef:undefined; }
function decodeBindings(value: unknown): BindingConfig[] { return Object.entries(record(value)).map(([id, candidate]) => { const raw = record(candidate); return { id, from: stringValue(raw.from), ...(typeof raw.pointer === "string" ? { pointer: raw.pointer } : {}), type: raw.type as ValueType, required: raw.required !== false, ...(raw.required === false && Object.hasOwn(raw, "default") && isJsonValue(raw.default) ? { default: raw.default } : {}), ...(typeof raw.description === "string" ? { description: raw.description } : {}), raw: clone(raw) }; }); }
function decodeOutputs(value: unknown): OutputConfig[] { return Object.entries(record(value)).map(([id, candidate]) => { const raw = record(candidate); return { id, type: raw.type as ValueType, ...(typeof raw.schema === "string" ? { schema: raw.schema } : {}), ...(typeof raw.description === "string" ? { description: raw.description } : {}), required: raw.required !== false, raw: clone(raw) }; }); }
function decodeValidators(value: unknown): ValidatorConfig[] { return (Array.isArray(value) ? value : []).map((candidate): ValidatorConfig => { const raw = record(candidate); if (isPlainRecord(raw.extension)) return {kind:"extension",extension:clone(raw.extension) as Omit<Extract<NodeExecutor,{type:"extension"}>,"type">,raw:clone(raw)}; if (Array.isArray(raw.command)) return { kind: "command", command: stringArray(raw.command), raw: clone(raw) }; return { kind: "schema", output: raw.output as string, schema: raw.schema as string, raw: clone(raw) }; }); }
function decodeRetry(value: unknown): RetryConfig | undefined { const raw = record(value); if (!positiveInteger(raw.maxAttempts) || !Array.isArray(raw.on)) return undefined; return { maxAttempts: raw.maxAttempts as number, on: raw.on as RetryConfig["on"], raw: clone(raw) }; }
function decodeReadiness(value: unknown): ReadinessConfig | undefined { if (value === undefined) return undefined; const raw = record(value); return { recommendedEvidence: (raw.recommendedEvidence as unknown[]).map((item) => record(item)).map((item) => ({ role: item.role as string, description: item.description as string, raw: clone(item) })), policyGates: (raw.policyGates as unknown[]).map((item) => record(item)).map((item) => ({ policy: item.policy as string, enforcement: item.enforcement as ReadinessConfig["policyGates"][number]["enforcement"], description: item.description as string, raw: clone(item) })), invariants: raw.invariants as string[], remedies: (raw.remedies as unknown[]).map((item) => record(item)).map((item) => ({ code: item.code as string, target: item.target as string, action: item.action as ReadinessConfig["remedies"][number]["action"], description: item.description as string, raw: clone(item) })), raw: clone(raw) }; }
function decodeJoin(value: unknown): { mode: "one" | "all"; from: string[] } | undefined { if (value === undefined) return undefined; const raw = record(value); return { mode: raw.mode === "all" ? "all" : "one", from: stringArray(raw.from) }; }
function uniqueStrings(values: readonly string[]) { return [...new Set(values.map((value) => value.trim()).filter(Boolean))]; }
function positiveInteger(value: unknown) { return typeof value === "number" && Number.isInteger(value) && value > 0; }
function nonnegativeInteger(value: unknown) { return typeof value === "number" && Number.isInteger(value) && value >= 0; }
function enumValue<const T extends string>(value: unknown, values: readonly T[], fallback: T): T { return typeof value === "string" && values.includes(value as T) ? value as T : fallback; }
function removeEmptyOptional(target: JsonObject, key: string, value: unknown) { if (value === undefined || value === "") delete target[key]; }
function hasOnlyKeys(value: JsonObject, allowed: readonly string[]) { return Object.keys(value).every((key) => allowed.includes(key)); }
function clampInteger(value: number, minimum: number, maximum: number) { return Math.max(minimum, Math.min(maximum, Math.round(value))); }
function validUniqueIDs(values: readonly string[]) { return values.every((value) => identifier.test(value)) && new Set(values).size === values.length; }
export function isPlanValueType(value: unknown): boolean { return value === "markdown" || value === "object" || typeof value === "string" && /^schema:[a-z][a-z0-9_]{0,63}$/.test(value); }
function supportedSharedSections(node: JsonObject) {
  const values = ["null", "boolean", "integer", "number", "string", "array", "object", "task", "repository", "workspace", "template", "markdown", "open_items", "decision_log"];
  if (!hasOnlyKeys(node, ["displayName", "type", "entry", "terminal", "inputs", "outputs", "readiness", "definition", "reasoning", "gate", "command", "approval", "call", "points", "routing", "implementation", "workspacePrepare", "workspaceValidate", "extension", "validators", "retry", "checkpoint", "transitionMode", "join", "permissions", "transitions"])) return false;
  if (typeof node.entry !== "boolean" || typeof node.terminal !== "boolean" || node.displayName !== undefined && (typeof node.displayName !== "string" || node.displayName.length === 0)) return false;
  if (!isPlainRecord(node.inputs) || !Object.entries(node.inputs).every(([id, value]) => identifier.test(id) && isPlainRecord(value) && hasOnlyKeys(value, ["from", "pointer", "type", "required", "default", "description"]) && typeof value.from === "string" && /^(run\.input\.[a-z][a-z0-9_]{0,63}|node\.[a-z][a-z0-9_]{0,63}\.output\.[a-z][a-z0-9_]{0,63})$/.test(value.from) && (values.includes(String(value.type)) || /^schema:[a-z][a-z0-9_]{0,63}$/.test(String(value.type))) && (value.required === undefined || typeof value.required === "boolean") && (value.pointer === undefined || typeof value.pointer === "string" && /^(?:|(?:\/(?:[^~/]|~[01])*)+)$/.test(value.pointer)) && (value.description === undefined || typeof value.description === "string") && (!Object.hasOwn(value, "default") || value.required === false && isJsonValue(value.default)))) return false;
  if (!isPlainRecord(node.outputs) || !Object.entries(node.outputs).every(([id, value]) => identifier.test(id) && isPlainRecord(value) && hasOnlyKeys(value, ["type", "schema", "description", "required", "artifact", "schemaDefinition"]) && (value.schemaDefinition === undefined || isPlainRecord(value.schemaDefinition)) && (value.artifact === undefined || isPlainRecord(value.artifact) && hasOnlyKeys(value.artifact, ["filename", "templateInput"]) && typeof value.artifact.filename === "string" && value.artifact.filename.length > 0 && (value.artifact.templateInput === undefined || typeof value.artifact.templateInput === "string" && identifier.test(value.artifact.templateInput))) && (values.includes(String(value.type)) || /^schema:[a-z][a-z0-9_]{0,63}$/.test(String(value.type))) && (value.required === undefined || typeof value.required === "boolean") && (value.schema === undefined || typeof value.schema === "string" && value.schema.length > 0) && (value.description === undefined || typeof value.description === "string"))) return false;
  if (node.validators !== undefined && (!Array.isArray(node.validators) || !node.validators.every((value) => (isPlainRecord(value) && isPlainRecord(value.extension) ? hasOnlyKeys(value,["extension"]) && supportedExecutor({type:"extension",extension:value.extension}) : isPlainRecord(value) && hasOnlyKeys(value, Array.isArray(value.command) ? ["command"] : ["output", "schema"]) && (Array.isArray(value.command) && value.command.length > 0 && value.command.every((item) => typeof item === "string")) !== (typeof value.output === "string" && identifier.test(value.output) && typeof value.schema === "string" && value.schema.length > 0))))) return false;
  const retryKinds = ["provider_unavailable", "provider_rate_limit", "process_failure", "validator_failure", "timeout", "interrupted"];
  if (node.retry !== undefined && (!isPlainRecord(node.retry) || !hasOnlyKeys(node.retry, ["maxAttempts", "on"]) || !positiveInteger(node.retry.maxAttempts) || Number(node.retry.maxAttempts) > 100 || !Array.isArray(node.retry.on) || !node.retry.on.every((value) => typeof value === "string" && retryKinds.includes(value)) || new Set(node.retry.on).size !== node.retry.on.length)) return false;
  if (node.readiness !== undefined && !supportedReadiness(node.readiness)) return false;
  if (node.definition !== undefined && !decodeNodeDefinitionRef(node.definition)) return false;
  if (node.transitionMode !== undefined && node.transitionMode !== "exclusive" && node.transitionMode !== "fanout") return false;
  if (node.join !== undefined && (!isPlainRecord(node.join) || node.join.mode !== "one" && node.join.mode !== "all" || !Array.isArray(node.join.from) || node.join.from.length < 2 || !node.join.from.every((value) => typeof value === "string") || new Set(node.join.from).size !== node.join.from.length)) return false;
  if (node.permissions !== undefined && (!Array.isArray(node.permissions) || !node.permissions.every((value) => typeof value === "string") || new Set(node.permissions).size !== node.permissions.length)) return false;
  if (node.transitions !== undefined && (!Array.isArray(node.transitions) || !node.transitions.every((value) => { if (!isPlainRecord(value) || !hasOnlyKeys(value, ["id", "to", "when", "kind", "maxTraversals", "enabledByDefault"]) || !identifier.test(String(value.id)) || !identifier.test(String(value.to)) || value.kind !== undefined && value.kind !== "normal" && value.kind !== "bounded" || value.enabledByDefault !== undefined && typeof value.enabledByDefault !== "boolean") return false; if (value.when !== undefined && decodePredicate(value.when).kind === "unsupported") return false; return value.kind === "bounded" ? positiveInteger(value.maxTraversals) && Number(value.maxTraversals) <= 10_000 : value.maxTraversals === undefined; }))) return false;
  return true;
}
function supportedExecutor(node: JsonObject) {
  const executorFields = ["reasoning", "gate", "command", "approval", "call", "points", "routing", "implementation", "workspacePrepare", "workspaceValidate", "extension"];
  const expected = node.type === "workspace_prepare" ? "workspacePrepare" : node.type === "workspace_validate" ? "workspaceValidate" : node.type === "subworkflow" ? "call" : node.type === "point_execution" ? "points" : String(node.type);
  if (executorFields.some((field) => field !== expected && node[field] !== undefined)) return false;
  switch (node.type) {
    case "extension": { const v=node.extension; if(!isPlainRecord(v)||!hasOnlyKeys(v,["ref","configuration"])||!isPlainRecord(v.ref)||!isPlainRecord(v.configuration)) return false; return hasOnlyKeys(v.ref,["id","version","digest"]) && [v.ref.id,v.ref.version,v.ref.digest].every(x=>typeof x==="string"); }
    case "reasoning": { const value = node.reasoning; return isPlainRecord(value) && hasOnlyKeys(value, ["agent", "skills", "tools", "instructions"]) && typeof value.agent === "string" && value.agent.length > 0 && (value.skills === undefined || Array.isArray(value.skills) && value.skills.every((item) => typeof item === "string") && new Set(value.skills).size === value.skills.length) && (value.tools === undefined || Array.isArray(value.tools) && value.tools.every((item) => typeof item === "string") && new Set(value.tools).size === value.tools.length); }
    case "gate": { const value = node.gate; return isPlainRecord(value) && hasOnlyKeys(value, ["policy", "condition"]) && typeof value.policy === "string" && value.policy.length > 0 && value.condition !== undefined && decodePredicate(value.condition).kind !== "unsupported"; }
    case "command": { const value = node.command; return isPlainRecord(value) && hasOnlyKeys(value, ["argv", "cwd", "timeoutSeconds"]) && Array.isArray(value.argv) && value.argv.length > 0 && value.argv.every((item) => typeof item === "string") && (value.cwd === undefined || typeof value.cwd === "string") && (value.timeoutSeconds === undefined || positiveInteger(value.timeoutSeconds)); }
    case "approval": { const value = node.approval; if (!isPlainRecord(value) || !hasOnlyKeys(value, ["actor", "externalCondition", "evidenceOutput"]) || typeof value.actor !== "string" || value.actor.length === 0) return false; return value.actor === "external" ? typeof value.externalCondition === "string" && value.externalCondition.length > 0 && typeof value.evidenceOutput === "string" && identifier.test(value.evidenceOutput) : value.externalCondition === undefined && value.evidenceOutput === undefined; }
    case "subworkflow": { const value = node.call; if (!isPlainRecord(value) || !hasOnlyKeys(value, ["workflow", "entry", "terminals", "inputs", "outputs"]) || !isPlainRecord(value.workflow) || !hasOnlyKeys(value.workflow, ["name", "version", "digest", "path"])) return false; const reference = value.workflow; return typeof reference.name === "string" && workflowName.test(reference.name) && typeof reference.version === "string" && semanticVersion.test(reference.version) && (reference.digest === undefined || typeof reference.digest === "string" && /^[0-9a-f]{64}$/.test(reference.digest)) && (reference.path === undefined || typeof reference.path === "string" && reference.path.length > 0) && typeof value.entry === "string" && identifier.test(value.entry) && Array.isArray(value.terminals) && value.terminals.length > 0 && value.terminals.every((item) => typeof item === "string" && identifier.test(item)) && new Set(value.terminals).size === value.terminals.length && isPlainRecord(value.inputs) && Object.entries(value.inputs).every(([key, item]) => identifier.test(key) && typeof item === "string" && identifier.test(item)) && isPlainRecord(value.outputs) && Object.entries(value.outputs).every(([key, item]) => identifier.test(key) && typeof item === "string" && outputSource.test(item)); }
    case "workspace_prepare": {const v=node.workspacePrepare;if(!isPlainRecord(v)||!hasOnlyKeys(v,["repositoryInput","checkout"])||typeof v.repositoryInput!=="string"||!isPlainRecord(v.checkout))return false;const c=v.checkout;return c.mode==="current_checkout"?hasOnlyKeys(c,["mode"]):c.mode==="new_worktree"&&hasOnlyKeys(c,["mode","baseRef","branch"])&&typeof c.baseRef==="string"&&typeof c.branch==="string";}
    case "workspace_validate":{const v=node.workspaceValidate;return isPlainRecord(v)&&hasOnlyKeys(v,["workspaceInput","checks"])&&typeof v.workspaceInput==="string"&&Array.isArray(v.checks)&&v.checks.every(a=>Array.isArray(a)&&a.every(x=>typeof x==="string"));}
    case "implementation": { const value = node.implementation; return isPlainRecord(value) && hasOnlyKeys(value, ["taskInput", "instructions", "workspaceInput"]) && (value.workspaceInput === undefined || typeof value.workspaceInput === "string") && typeof value.taskInput === "string" && identifier.test(value.taskInput) && (value.instructions === undefined || typeof value.instructions === "string"); }
    case "point_execution": { const value = node.points; return isPlainRecord(value) && hasOnlyKeys(value, ["planInput", "approval", "riskTags", "validation", "publishing"]) && typeof value.planInput === "string" && identifier.test(value.planInput) && record(node.inputs)[value.planInput] !== undefined && isPlanValueType(record(record(node.inputs)[value.planInput]).type) && ["none", "every", "risk", "combined"].includes(String(value.approval)) && Array.isArray(value.riskTags) && value.riskTags.every((item) => typeof item === "string" && item.length > 0) && new Set(value.riskTags).size === value.riskTags.length && (value.approval === "risk" ? value.riskTags.length > 0 : value.riskTags.length === 0) && ["each", "combined", "each_and_combined"].includes(String(value.validation)) && ["after_story_validation", "after_each_point"].includes(String(value.publishing)); }
    case "routing": { const value=record(node.routing); return hasOnlyKeys(value,["agent","branches","routeOutput","rationaleOutput","adviceOutput","missingInformationOutput","assumptionsOutput","confirmationOutput"]) && typeof value.agent==="string" && value.agent.length>0 && Array.isArray(value.branches) && value.branches.length>0 && value.branches.every((branch)=>isPlainRecord(branch)&&hasOnlyKeys(branch,["name","transition"])&&typeof branch.name==="string"&&identifier.test(branch.name)&&typeof branch.transition==="string"&&identifier.test(branch.transition)) && [value.routeOutput,value.rationaleOutput,value.adviceOutput,value.missingInformationOutput,value.assumptionsOutput,value.confirmationOutput].every((id)=>typeof id==="string"&&identifier.test(id)); }
    default: return false;
  }
}
function supportedReadiness(value: unknown) {
  if (!isPlainRecord(value) || !Array.isArray(value.recommendedEvidence) || !Array.isArray(value.policyGates) || !Array.isArray(value.invariants) || !Array.isArray(value.remedies)) return false;
  return hasOnlyKeys(value, ["recommendedEvidence", "policyGates", "invariants", "remedies"])
    && value.recommendedEvidence.every((item) => isPlainRecord(item) && hasOnlyKeys(item, ["role", "description"]) && typeof item.role === "string" && identifier.test(item.role) && typeof item.description === "string" && item.description.length > 0)
    && value.policyGates.every((item) => isPlainRecord(item) && hasOnlyKeys(item, ["policy", "enforcement", "description"]) && typeof item.policy === "string" && identifier.test(item.policy) && ["advisory", "blocking", "external"].includes(String(item.enforcement)) && typeof item.description === "string" && item.description.length > 0)
    && value.invariants.every((item) => typeof item === "string" && item.length > 0) && new Set(value.invariants).size === value.invariants.length
    && value.remedies.every((item) => isPlainRecord(item) && hasOnlyKeys(item, ["code", "target", "action", "description"]) && typeof item.code === "string" && identifier.test(item.code) && typeof item.target === "string" && identifier.test(item.target) && ["supply_input", "revise_artifact", "clarify_decision", "install_capability", "rerun_validation"].includes(String(item.action)) && typeof item.description === "string" && item.description.length > 0);
}
function encodeReadiness(value: ReadinessConfig): JsonObject { return { ...value.raw, recommendedEvidence: value.recommendedEvidence.map((item) => ({ ...item.raw, role: item.role, description: item.description })), policyGates: value.policyGates.map((item) => ({ ...item.raw, policy: item.policy, enforcement: item.enforcement, description: item.description })), invariants: [...value.invariants], remedies: value.remedies.map((item) => ({ ...item.raw, code: item.code, target: item.target, action: item.action, description: item.description })) }; }
function encodeBinding(value: BindingConfig): JsonObject { const result: JsonObject = { ...value.raw, from: value.from, type: value.type, required: value.required }; setOptional(result, "pointer", value.pointer); setOptional(result, "description", value.description); if (!value.required && value.default !== undefined) result.default = value.default; else delete result.default; return result; }
function encodeOutput(value: OutputConfig): JsonObject { const result: JsonObject = { ...value.raw, type: value.type, required: value.required }; setOptional(result, "schema", value.schema); setOptional(result, "description", value.description); return result; }
function setOptional(target: JsonObject, key: string, value: unknown) { if (value === undefined || value === "") delete target[key]; else target[key] = value; }
function isPlainRecord(value: unknown): value is JsonObject { return value !== null && typeof value === "object" && !Array.isArray(value); }
function transitionIDs(nodes: JsonObject) { const values = new Set<string>(); for (const node of Object.values(nodes)) for (const transition of Array.isArray(record(node).transitions) ? record(node).transitions as unknown[] : []) { const id = record(transition).id; if (typeof id === "string") values.add(id); } return values; }
function repairJoin(node: JsonObject, removed: ReadonlySet<string>) { const join = record(node.join); if (!Array.isArray(join.from)) return; const remaining = join.from.filter((value): value is string => typeof value === "string" && !removed.has(value)); if (remaining.length >= 2) join.from = remaining; else delete node.join; }
