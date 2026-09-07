import { useRef, useState, type PointerEvent } from "react";
import type { EditorGraph, EditorSelection, JsonObject, WorkflowLayout } from "./workflowEditorModel";
import { autoLayoutGraph, connectionError, graphBounds, graphNodeHeight, portKey, portLabel, type Port, type PortGraph } from "./workflowPortModel";

type CommonProps = { graph: EditorGraph; ports: PortGraph; onConnect(source: Port, target: Port): void; onFocus(nodeId: string, field?: string): void };
export function WorkflowPortConnections({ ports, onConnect, onFocus }: CommonProps) {
  const [sourceKey, setSource] = useState(""); const [targetKey, setTarget] = useState("");
  const sources = ports.ports.filter((port) => port.kind !== "execution" && port.direction === "output");
  const targets = ports.ports.filter((port) => port.kind === "data" && port.direction === "input");
  const source = sources.find((port) => portKey(port) === sourceKey), target = targets.find((port) => portKey(port) === targetKey);
  const error = source && target ? connectionError(source, target) : undefined;
  return <section className="workflow-port-connections" aria-label="Typed data bindings"><details><summary>Data bindings · {ports.edges.filter((edge) => edge.kind === "data").length}</summary>
    <form onSubmit={(event) => { event.preventDefault(); if (source && target && !error) onConnect(source, target); }}>
      <label>Data output<select value={source ? sourceKey : ""} onChange={(event) => setSource(event.target.value)}><option value="">Choose output</option>{sources.map((port) => <option key={portKey(port)} value={portKey(port)}>{portLabel(port)}</option>)}</select></label>
      <label>Data input<select value={target ? targetKey : ""} onChange={(event) => setTarget(event.target.value)}><option value="">Choose input</option>{targets.map((port) => <option key={portKey(port)} value={portKey(port)}>{portLabel(port)}</option>)}</select></label>
      <button type="submit" disabled={!source || !target || Boolean(error)}>Connect data ports</button>
    </form>{error && <p role="alert">{error}</p>}<p className="inspector-help">Reconnecting replaces the input source with the selected whole value. Configure pointers and remove bindings in the input inspector.</p>
    <ul>{ports.edges.filter((edge) => edge.kind === "data").map((edge) => <li key={edge.id}><button type="button" onClick={() => onFocus(edge.target.nodeId, `inputs.${edge.target.portId}`)}>{portLabel(edge.source)} → {portLabel(edge.target)}</button></li>)}</ul>
  </details>{ports.findings.length > 0 && <div role="status"><strong>{ports.findings.length} incomplete or invalid port connections</strong>{ports.findings.map((finding) => <button className="port-finding" key={`${finding.nodeId}:${finding.field}`} type="button" onClick={() => onFocus(finding.nodeId, finding.field)}>{finding.nodeId}: {finding.code} — {finding.message}</button>)}</div>}</section>;
}

export function WorkflowPortCanvas({ graph, ports, selection, layout, onConnect, onFocus, onSelect, onLayout }: CommonProps & { selection: EditorSelection; layout: WorkflowLayout; onSelect(selection: EditorSelection): void; onLayout(layout: JsonObject, message: string): void }) {
  const bounds = graphBounds(graph, ports.ports);
  const fitZoom = Math.max(.25, Math.min(3, 900 / bounds.width, 620 / bounds.height));
  const viewport = layout.viewport ?? { x: bounds.x, y: bounds.y, zoom: fitZoom };
  const zoom = Number.isFinite(viewport.zoom) ? Math.max(.25, Math.min(3, viewport.zoom)) : 1;
  const [pending, setPending] = useState<Port>(); const [message, setMessage] = useState("");
  const svgRef = useRef<SVGSVGElement>(null);
  const drag = useRef<{ id: string; x: number; y: number; nodeX: number; nodeY: number } | undefined>(undefined);
  const dimensions = { width: 900 / zoom, height: 620 / zoom };
  function point(event: PointerEvent) { const svg = svgRef.current!; const transform = svg.getScreenCTM()?.inverse(); return transform ? new DOMPoint(event.clientX, event.clientY).matrixTransform(transform) : { x: event.clientX, y: event.clientY }; }
  function setViewport(x: number, y: number, nextZoom = zoom) { onLayout({ ...layout, viewport: { x, y, zoom: Math.max(.25, Math.min(3, nextZoom)) } }, "Updated canvas view."); }
  function choose(port: Port) {
    if (port.direction === "output") { setPending(port); setMessage(`Choose an input for ${portLabel(port)}.`); return; }
    if (!pending) { onFocus(port.nodeId, port.kind === "data" ? `inputs.${port.portId}` : undefined); return; }
    const source = ports.ports.find((candidate) => portKey(candidate) === portKey(pending));
    if (!source) { setPending(undefined); setMessage("The source port no longer exists. Choose an output again."); return; }
    const error = connectionError(source, port); if (error) { setMessage(error); return; }
    onConnect(source, port); setPending(undefined); setMessage("Ports connected.");
  }
  function position(port: Port) {
    if (port.kind === "run_input") return { x: bounds.x + 230, y: bounds.y + 70 + ports.ports.filter((candidate) => candidate.kind === "run_input").findIndex((candidate) => portKey(candidate) === portKey(port)) * 28 };
    const node = graph.nodes.find((candidate) => candidate.id === port.nodeId); if (!node) return undefined;
    const siblings = ports.ports.filter((candidate) => candidate.kind !== "run_input" && candidate.nodeId === node.id && candidate.direction === port.direction);
    return { x: node.position.x + (port.direction === "output" ? 260 : 0), y: node.position.y + 132 + siblings.findIndex((candidate) => portKey(candidate) === portKey(port)) * 28 };
  }
  return <div className="workflow-port-graph" onKeyDown={(event) => { if (event.key === "Escape" && pending) { event.stopPropagation(); setPending(undefined); setMessage("Connection cancelled."); } }}><div className="port-canvas-toolbar" role="group" aria-label="Canvas controls">
    <button type="button" onClick={() => setViewport(bounds.x, bounds.y, fitZoom)}>Zoom to fit</button><button type="button" aria-label="Zoom in" onClick={() => setViewport(viewport.x, viewport.y, zoom * 1.25)}>+</button><button type="button" aria-label="Zoom out" onClick={() => setViewport(viewport.x, viewport.y, zoom / 1.25)}>−</button>
    <button type="button" onClick={() => onLayout({ ...autoLayoutGraph(layout, graph, ports.ports), viewport: undefined }, "Auto-layout applied; execution is unchanged.")}>Auto-layout</button>
    {pending && <button type="button" onClick={() => { setPending(undefined); setMessage("Connection cancelled."); }}>Cancel connection</button>}<span>Solid: execution · Dashed: data</span>
  </div><p className="sr-only" role="status">{message}</p>{message && <p className="port-message">{message}</p>}
  {ports.ports.some((port) => port.kind === "run_input") && <div className="run-input-ports" aria-label="Run input output ports">{ports.ports.filter((port) => port.kind === "run_input").map((port) => <button key={portKey(port)} type="button" onClick={() => choose(port)}>{portLabel(port)} →</button>)}</div>}
  <div className="workflow-canvas workflow-canvas--ports" tabIndex={0} aria-label="Workflow canvas. Arrow keys pan; Shift and arrow keys move the selected node. Escape cancels a connection." onKeyDown={(event) => {
    if (event.target !== event.currentTarget) return;
    if (event.key === "Escape") { setPending(undefined); return; } if (!event.key.startsWith("Arrow")) return; event.preventDefault();
    const dx = event.key === "ArrowLeft" ? -20 : event.key === "ArrowRight" ? 20 : 0, dy = event.key === "ArrowUp" ? -20 : event.key === "ArrowDown" ? 20 : 0;
    const node = selection.kind === "node" ? graph.nodes.find((candidate) => candidate.id === selection.nodeId) : undefined;
    if (event.shiftKey && node) onLayout({ ...layout, nodes: { ...layout.nodes, [node.id]: { x: node.position.x + dx, y: node.position.y + dy } } }, `Moved ${node.id}.`); else setViewport(viewport.x + dx, viewport.y + dy);
  }}><svg ref={svgRef} width="100%" height="100%" viewBox={`${viewport.x} ${viewport.y} ${dimensions.width} ${dimensions.height}`} role="group" aria-label="Typed workflow graph" onPointerMove={(event) => {
    const active = drag.current; if (!active) return; const cursor = point(event);
    onLayout({ ...layout, viewport, nodes: { ...layout.nodes, [active.id]: { x: active.nodeX + cursor.x - active.x, y: active.nodeY + cursor.y - active.y } } }, `Moved ${active.id}.`);
  }} onPointerUp={() => { drag.current = undefined; }} onPointerCancel={() => { drag.current = undefined; }}>
    <defs><marker id="workflow-port-arrow" viewBox="0 0 10 10" refX="13" refY="5" markerWidth="6" markerHeight="6" orient="auto"><path d="M 0 0 L 10 5 L 0 10 z" fill="context-stroke" /></marker></defs>
    {ports.ports.some((port) => port.kind === "run_input") && <g className="canvas-node"><rect x={bounds.x + 20} y={bounds.y + 25} width="210" height={45 + ports.ports.filter((port) => port.kind === "run_input").length * 28} rx="10" /><text className="port-label" x={bounds.x + 30} y={bounds.y + 45}>Run inputs</text>{ports.ports.filter((port) => port.kind === "run_input").map((port) => { const endpoint = position(port)!; return <g key={portKey(port)}><text className="port-label" x={endpoint.x - 12} y={endpoint.y + 4} textAnchor="end"><title>{portLabel(port)}</title>{port.portId.length > 14 ? port.portId.slice(0, 13) + "…" : port.portId}: {port.valueType}</text><circle className="workflow-port workflow-port--data" cx={endpoint.x} cy={endpoint.y} r="7" role="button" tabIndex={0} aria-label={portLabel(port)} onClick={() => choose(port)} onKeyDown={(event) => { if (event.key === "Enter" || event.key === " ") { event.preventDefault(); choose(port); } }} /></g>; })}</g>}
    {ports.edges.map((edge) => { const source = position(edge.source), target = position(edge.target); if (!source || !target) return null; const select = () => edge.kind === "execution" ? onSelect({ kind: "edge", edgeId: edge.id }) : onFocus(edge.target.nodeId, `inputs.${edge.target.portId}`); return <g key={edge.id} className={`canvas-edge canvas-edge--${edge.kind}${edge.kind === "execution" ? ` canvas-edge--${graph.edges.find((item) => item.id === edge.id)?.kind}` : ""}${selection.kind === "edge" && selection.edgeId === edge.id ? " canvas-edge--selected" : ""}`}><path markerEnd="url(#workflow-port-arrow)" d={`M ${source.x} ${source.y} C ${source.x + 60} ${source.y}, ${target.x - 60} ${target.y}, ${target.x} ${target.y}`} tabIndex={0} role="button" aria-label={`${edge.kind}: ${portLabel(edge.source)} to ${portLabel(edge.target)}`} onClick={select} onKeyDown={(event) => { if (event.key === "Enter" || event.key === " ") { event.preventDefault(); select(); } }} /></g>; })}
    {graph.nodes.map((node) => <g key={node.id} className={`canvas-node${selection.kind === "node" && selection.nodeId === node.id ? " canvas-node--selected" : ""}`} transform={`translate(${node.position.x} ${node.position.y})`}>
      <rect width="260" height={graphNodeHeight(ports.ports, node.id)} rx="10" />
      <foreignObject width="260" height="114"><button type="button" onPointerDown={(event) => { const cursor = point(event); event.currentTarget.setPointerCapture(event.pointerId); drag.current = { id: node.id, x: cursor.x, y: cursor.y, nodeX: node.position.x, nodeY: node.position.y }; }} onClick={() => onFocus(node.id)} aria-label={`${node.displayName}, ${node.type} node`}><span>{node.type.replaceAll("_", " ")}</span><strong>{node.displayName}</strong><small>{node.checkpoint} checkpoint · {node.validationCount || ports.findings.some((finding) => finding.nodeId === node.id) ? "Needs validation" : "Draft"}</small><i>{node.entry ? "Entry-capable " : ""}{node.terminal ? "Terminal-capable" : ""}</i></button></foreignObject>
      {ports.ports.filter((port) => port.kind !== "run_input" && port.nodeId === node.id).map((port) => { const endpoint = position(port)!; const isOutput = port.direction === "output"; return <g key={portKey(port)} transform={`translate(${isOutput ? 260 : 0} ${endpoint.y - node.position.y})`}>
        <circle r="7" className={`workflow-port workflow-port--${port.kind}`} role="button" tabIndex={0} aria-label={`${port.direction} ${portLabel(port)}`} onClick={() => choose(port)} onKeyDown={(event) => { if (event.key === "Enter" || event.key === " ") { event.preventDefault(); choose(port); } }} />
        <text x={isOutput ? -12 : 12} y="4" textAnchor={isOutput ? "end" : "start"} className="port-label"><title>{portLabel(port)}</title>{port.portId.length > 8 ? `${port.portId.slice(0, 7)}…` : port.portId}{port.kind === "data" ? `: ${port.valueType}${port.required ? " *" : ""}` : ""}</text>
      </g>; })}
    </g>)}
  </svg></div>
  <nav className="workflow-minimap" aria-label="Workflow minimap"><svg viewBox={`${bounds.x} ${bounds.y} ${bounds.width} ${bounds.height}`} aria-hidden="true">{graph.nodes.map((node) => <rect key={node.id} x={node.position.x} y={node.position.y} width="260" height={graphNodeHeight(ports.ports, node.id)} />)}<rect className="minimap-viewport" x={viewport.x} y={viewport.y} width={dimensions.width} height={dimensions.height} /></svg>{graph.nodes.map((node) => <button key={node.id} type="button" onClick={() => { setViewport(node.position.x - 30, node.position.y - 30, 2); onFocus(node.id); }}>{node.displayName}</button>)}</nav>
  </div>;
}
