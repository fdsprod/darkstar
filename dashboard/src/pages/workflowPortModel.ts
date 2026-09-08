import dagre from "@dagrejs/dagre";
import type { EditorGraph, JsonObject, ValueType, WorkflowLayout } from "./workflowEditorModel";

export type Port =
  | { kind: "execution"; nodeId: string; portId: "execute"; direction: "input" }
  | { kind: "execution"; nodeId: string; portId: string; direction: "output" }
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
export const portLabel = (port: Port) => port.kind === "run_input" ? `Resource ${port.portId} (${port.valueType})` : `${port.nodeId}.${port.portId}${port.kind === "data" ? ` (${port.valueType}${port.required ? ", required" : ", optional"})` : " (execution)"}`;
export const sourceReference = (port: Exclude<Port, { kind: "execution" }>) => port.kind === "run_input" ? `run.input.${port.portId}` : `node.${port.nodeId}.output.${port.portId}`;

/** Rebuildable projection: the workflow document owns every binding and transition. */
export function derivePortGraph(document: JsonObject, graph: EditorGraph): PortGraph {
  const spec = record(document.spec), nodes = record(spec.nodes);
  const ports: Port[] = Object.entries(record(spec.inputs)).map(([portId, raw]) => ({ kind: "run_input", portId, direction: "output", valueType: record(raw).type as ValueType }));
  for (const node of graph.nodes) {
    ports.push({kind:"execution",nodeId:node.id,portId:"execute",direction:"input"});
    for (const portId of node.type === "gate" ? ["true","false"] : ["complete"]) ports.push({kind:"execution",nodeId:node.id,portId,direction:"output"});
    for (const direction of ["input", "output"] as const) for (const [portId, raw] of Object.entries(record(record(nodes[node.id])[`${direction}s`]))) {
      ports.push({ kind: "data", nodeId: node.id, direction, portId, valueType: record(raw).type as ValueType, required: record(raw).required !== false });
    }
  }
  const edges: PortEdge[] = graph.edges.map((edge) => ({ kind: "execution", id: edge.id, source: { kind: "execution", nodeId: edge.from, portId: record(nodes[edge.from]).type === "gate" ? gateBranch(record((record(nodes[edge.from]).transitions as JsonObject[]).find(t => t.id === edge.transitionId))) : "complete", direction: "output" }, target: { kind: "execution", nodeId: edge.to, portId: "execute", direction: "input" } }));
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
  for (const [id,value] of Object.entries(record(spec.inputs))) if(record(record(value).resource).kind === "artifact") ports.push({kind:"data",nodeId:"$input:"+id,portId:"$write",direction:"input",valueType:"string",required:false});
  for (const node of graph.nodes) ports.push({ kind: "data", nodeId: node.id, portId: "$new", direction: "input", valueType: "object", required: false });
  ports.push({kind:"execution",nodeId:"$start",portId:"complete",direction:"output"});
  const entry = String(record(spec.routeDefaults).entry);
  if (entry && nodes[entry]) edges.unshift({kind:"execution",id:"$start",source:{kind:"execution",nodeId:"$start",portId:"complete",direction:"output"},target:{kind:"execution",nodeId:entry,portId:"execute",direction:"input"}});
  return { ports, edges, findings };
}

export function connectionError(source: Port, target: Port): string | undefined {
  if (source.direction !== "output" || target.direction !== "input") return "Connections must run from an output port to an input port.";
  if (source.kind === "execution" || target.kind === "execution") {
    if (source.kind !== "execution" || target.kind !== "execution") return "Execution ports cannot connect to data ports.";
    return source.nodeId === target.nodeId ? "Self transitions require a bounded repair path in Structure view." : undefined;
  }
  if (target.kind === "data" && target.portId === "$new") return undefined;
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
  if(currentTarget.nodeId.startsWith("$input:") && currentTarget.portId === "$write") {
    if(currentSource.kind !== "data")return {kind:"invalid",message:"An artifact producer must be an action output."};
    const id=currentTarget.nodeId.slice(7), spec=record(next.spec), declaration=record(record(spec.inputs)[id]);
    const output=record(record(record(nodes[currentSource.nodeId]).outputs)[currentSource.portId]);
    output.artifact={filename:record(declaration.resource).filename};
    for(const raw of Object.values(nodes))for(const binding of Object.values(record(record(raw).inputs)))if(record(binding).from === "run.input."+id)record(binding).from=sourceReference(currentSource);
    delete record(spec.inputs)[id];return {kind:"changed",document:next};
  }
  const targetNode = record(nodes[currentTarget.nodeId]);
  if (currentTarget.portId === "$new") {
    const inputs = record(targetNode.inputs); targetNode.inputs = inputs;
    const stem = currentSource.portId.replace(/[^a-z0-9_]/g, "_"); let id = stem;
    for (let i = 2; Object.hasOwn(inputs,id); i++) id = stem + "_" + i;
    inputs[id] = { from: sourceReference(currentSource), type: currentSource.valueType };
    return { kind: "changed", document: next };
  }
  const binding = record(record(targetNode.inputs)[currentTarget.portId]);
  binding.from = sourceReference(currentSource); delete binding.pointer;
  return { kind: "changed", document: next };
}

export const GRAPH_NODE_WIDTH = 340;
export const GRAPH_NODE_HEADER_HEIGHT = 78;
export const GRAPH_PORT_ROW_HEIGHT = 34;
export const PORT_LAYOUT_VERSION = 7;
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
  ranked.setGraph({ rankdir: "LR", ranker: "network-simplex", align: "UL", ranksep: 112, nodesep: 96, edgesep: 42, marginx: 80, marginy: 80 });
  ranked.setDefaultEdgeLabel(() => ({}));
  for (const node of graph.nodes) ranked.setNode(node.id, { width: graphNodeWidth(ports, node), height: graphNodeHeight(ports, node.id), order: nodeOrder.get(node.id) });
  for (const [index, edge] of forwardEdges.entries()) ranked.setEdge(edge.from, edge.to, { weight: edge.kind === "normal" || edge.kind === "subworkflow" ? 3 : 2, minlen: 1, order: index }, edge.id);
  const resourceSize={width:280,height:150};
  for(const port of ports) {
    if(port.kind==="run_input")ranked.setNode("$input:"+port.portId,{...resourceSize});
    else if(port.kind==="data"&&port.direction==="output") { const id="$output:"+port.nodeId+":"+port.portId;ranked.setNode(id,{...resourceSize});ranked.setEdge(port.nodeId,id,{weight:1},"produce:"+id); }
  }
  const reaches=(source:string,target:string)=>{const seen=new Set<string>();const todo=[source];while(todo.length){const id=todo.pop()!;if(id===target)return true;if(seen.has(id))continue;seen.add(id);for(const edge of forwardEdges)if(edge.from===id)todo.push(edge.to);}return false;};
  for(const [index,binding]of (graph.bindings??[]).entries()) {
    const parts=binding.source.split("."); const source=parts[0]==="run"?"$input:"+parts[2]:"$output:"+parts[1]+":"+parts[3];
    if(ranked.hasNode(source)&&ranked.hasNode(binding.target)&&(parts[0]==="run"||parts[1]!==binding.target&&reaches(parts[1],binding.target)))ranked.setEdge(source,binding.target,{weight:1},"binding:"+index);
  }
  ranked.setNode("$start",{width:240,height:150});
  const entry=graph.entry??graph.nodes.find(node=>node.entry)?.id;
  if(entry&&ranked.hasNode(entry))ranked.setEdge("$start",entry,{weight:5},"start");
  dagre.layout(ranked);
  const nodes = Object.fromEntries(ranked.nodes().map(id=>{const placed=ranked.node(id) as {x:number;y:number;width:number;height:number};return [id,{x:Math.round(placed.x-placed.width/2),y:Math.round(placed.y-placed.height/2)}];}));
  const start=nodes.$start;
  return {...layout,portLayoutVersion:PORT_LAYOUT_VERSION,viewport:{x:40-start.x*.85,y:100-start.y*.85,zoom:.85},nodes};
}

/** Replaces coordinates written for earlier, smaller cards with the current ranked layout. */
export function preparePortLayout(layout: WorkflowLayout, graph: EditorGraph, ports: readonly Port[]): WorkflowLayout {
  return layout.portLayoutVersion === PORT_LAYOUT_VERSION ? layout : autoLayoutGraph(layout, graph, ports);
}

function gateBranch(transition: JsonObject): string { const args=record(transition.when).args; if(Array.isArray(args)){ const literal=args.map(record).find(a=>typeof a.literal === "boolean"); if(literal)return String(literal.literal); }return "true"; }
