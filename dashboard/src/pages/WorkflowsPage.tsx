import { useEffect, useMemo, useRef, useState, type FormEvent, type KeyboardEvent, type PointerEvent as ReactPointerEvent } from "react";

import { ApiRequestError } from "../api/client";
import type { components } from "../api/schema.generated";
import { tabKeyTarget } from "../accessibility/keyboard";
import { useRouter } from "../app/router";
import { PageHeader } from "../components/PageStructure";
import { humanize, shortIdentifier } from "./runDetailModel";
import { workflowEditorApi } from "./workflowEditorApi";
import {
  addNode, connectNodes, createStarterDocument, deriveEditorGraph, draftRevision, moveNode, persistenceLabel,
  removeEdge, removeNode, reorderNode, updateNode,
  type AuthoredTransitionKind, type EditorGraph, type EditorSelection, type EditorView, type JsonObject, type PersistenceState,
  type ValidationState, type VisualEdge, type VisualNode, type WorkflowEdgeKind, type WorkflowNodeType,
} from "./workflowEditorModel";

type Schemas = components["schemas"];
type LibraryItem = { kind: "draft"; draft: Schemas["WorkflowDraft"] } | { kind: "installed"; version: Schemas["WorkflowVersionSummary"] } | { kind: "archived"; archive: Schemas["WorkflowArchive"] };
const editorViews: EditorView[] = ["canvas", "structure"];
const palette: { type: WorkflowNodeType; label: string; description: string }[] = [
  { type: "reasoning", label: "Reasoning", description: "Agent work" }, { type: "command", label: "Command", description: "Local command" },
  { type: "gate", label: "Gate", description: "Conditional branch" }, { type: "approval", label: "Approval", description: "Human checkpoint" },
  { type: "subworkflow", label: "Sub-workflow", description: "Pinned workflow call" }, { type: "point_execution", label: "Point execution", description: "Story point runner" },
];

export function WorkflowsPage() {
  const { search, navigate } = useRouter();
  const params = useMemo(() => new URLSearchParams(search), [search]);
  const [library, setLibrary] = useState<Schemas["WorkflowLibrary"]>({ versions: [], drafts: [], archives: [] });
  const [loadState, setLoadState] = useState<"loading" | "ready" | "error">("loading");
  const [query, setQuery] = useState("");
  const [draft, setDraft] = useState<Schemas["WorkflowDraft"]>();
  const [document, setDocument] = useState<JsonObject>();
  const [layout, setLayout] = useState<JsonObject>({});
  const [persistence, setPersistence] = useState<PersistenceState>();
  const [validation, setValidation] = useState<ValidationState>({ kind: "not_run" });
  const [announcement, setAnnouncement] = useState("");
  const [connectFrom, setConnectFrom] = useState<string>();
  const [newOpen, setNewOpen] = useState(false);
  const [newName, setNewName] = useState("workflow/new-workflow");
  const [newScope, setNewScope] = useState<"user" | "project">("user");
  const [busy, setBusy] = useState("");
  const tabRefs = useRef<Array<HTMLButtonElement | null>>([]);
  const dragRef = useRef<{ id: string; offsetX: number; offsetY: number } | undefined>(undefined);
  const itemParam = params.get("item") ?? "";
  const view: EditorView = editorViews.includes(params.get("view") as EditorView) ? params.get("view") as EditorView : "structure";
  const selection = parseSelection(params);

  const refresh = async (signal?: AbortSignal) => { const next = await workflowEditorApi.library(signal); setLibrary(next); setLoadState("ready"); return next; };
  useEffect(() => { const abort = new AbortController(); void refresh(abort.signal).catch(() => { if (!abort.signal.aborted) setLoadState("error"); }); return () => abort.abort(); }, []);

  const items = useMemo<LibraryItem[]>(() => [
    ...library.drafts.map((value) => ({ kind: "draft" as const, draft: value })),
    ...library.versions.map((value) => ({ kind: "installed" as const, version: value })),
    ...library.archives.map((value) => ({ kind: "archived" as const, archive: value })),
  ], [library]);
  const selected = items.find((item) => itemKey(item) === itemParam) ?? items.find((item) => item.kind === "draft") ?? items[0];
  const selectedKey = selected ? itemKey(selected) : "";

  useEffect(() => {
    if (selected?.kind !== "draft") { setDraft(undefined); setDocument(undefined); setPersistence(undefined); return; }
    if (draft?.id === selected.draft.id) return;
    setDraft(selected.draft); setDocument(structuredClone(selected.draft.document) as JsonObject); setLayout(structuredClone(selected.draft.layout) as JsonObject);
    setPersistence({ kind: "clean", revision: selected.draft.revision }); setValidation({ kind: "not_run" }); setConnectFrom(undefined);
  }, [selectedKey, draft?.id]);

  useEffect(() => {
    if (!draft || !document || persistence?.kind !== "dirty") return;
    const timer = window.setTimeout(() => {
      const revision = persistence.revision; setPersistence({ kind: "saving", revision });
      void workflowEditorApi.save({ id: draft.id, expectedRevision: revision, document, layout }).then((saved) => {
        setDraft(saved); setLibrary((current) => ({ ...current, drafts: current.drafts.map((item) => item.id === saved.id ? saved : item) }));
        setPersistence({ kind: "clean", revision: saved.revision }); setAnnouncement(`Draft saved at revision ${saved.revision}.`);
      }).catch(async (cause: unknown) => {
        if (cause instanceof ApiRequestError && (cause.status === 409 || cause.code.includes("conflict"))) {
          try { const remote = await workflowEditorApi.draft(draft.id); setPersistence({ kind: "conflict", revision, remote }); }
          catch { setPersistence({ kind: "error", revision, message: "The conflicting server revision could not be loaded." }); }
        } else if (!navigator.onLine || cause instanceof TypeError) setPersistence({ kind: "offline", revision, message: "Reconnect to retry autosave." });
        else setPersistence({ kind: "error", revision, message: cause instanceof Error ? cause.message : "Autosave failed." });
      });
    }, 700);
    return () => window.clearTimeout(timer);
  }, [draft?.id, document, layout, persistence]);
  useEffect(() => { const online = () => setPersistence((current) => current && (current.kind === "offline" || current.kind === "error") ? { kind: "dirty", revision: current.revision } : current); window.addEventListener("online", online); return () => window.removeEventListener("online", online); }, []);

  const graph = useMemo(() => document ? deriveEditorGraph(document, layout, validation.kind === "invalid" ? validation.findings : []) : undefined, [document, layout, validation]);
  const selectedNode = selection.kind === "node" ? graph?.nodes.find((node) => node.id === selection.nodeId) : undefined;
  const selectedEdge = selection.kind === "edge" ? graph?.edges.find((edge) => edge.id === selection.edgeId) : undefined;
  const visibleItems = items.filter((item) => itemSearch(item).includes(query.trim().toLowerCase()));

  const setParams = (changes: Record<string, string | undefined>, replace = false) => { const next = new URLSearchParams(params); for (const [key, value] of Object.entries(changes)) value === undefined ? next.delete(key) : next.set(key, value); navigate(`/workflows?${next}`, { replace }); };
  const selectItem = (item: LibraryItem) => setParams({ item: itemKey(item), selection: undefined, view: item.kind === "draft" ? view : "structure" });
  const select = (next: EditorSelection) => setParams({ selection: serializeSelection(next) }, true);
  const changeDocument = (next: JsonObject, message: string) => { if (!persistence || persistence.kind === "conflict" || persistence.kind === "saving") return; setDocument(next); setPersistence({ kind: "dirty", revision: draftRevision(persistence) }); setValidation({ kind: "not_run" }); setAnnouncement(message); };
  const changeLayout = (next: JsonObject, message: string) => { if (!persistence || persistence.kind === "conflict" || persistence.kind === "saving") return; setLayout(next); setPersistence({ kind: "dirty", revision: draftRevision(persistence) }); setAnnouncement(message); };

  async function createDraft(event: FormEvent) {
    event.preventDefault(); setBusy("create");
    try { const created = await workflowEditorApi.create({ name: newName.trim(), scope: newScope, scopeReference: newScope === "user" ? "local-user" : "current-project", document: createStarterDocument(newName.trim()), layout: { version: 1, nodes: { start: { x: 100, y: 80 } } } }); await refresh(); setNewOpen(false); setParams({ item: `draft:${created.id}`, view: "structure", selection: "node:start" }); }
    catch { setAnnouncement("Draft creation failed. Check the workflow name and daemon health."); } finally { setBusy(""); }
  }
  async function duplicateInstalled(item: Extract<LibraryItem, { kind: "installed" }>) {
    setBusy("duplicate");
    try { const created = await workflowEditorApi.duplicate({ name: item.version.name, version: item.version.version, newName: `${item.version.name}-copy`, scope: "user", scopeReference: "local-user" }); await refresh(); setParams({ item: `draft:${created.id}`, view: "structure", selection: undefined }); }
    catch { setAnnouncement("The installed workflow could not be duplicated. Rename an existing copy or try again."); } finally { setBusy(""); }
  }
  async function archiveInstalled(item: Extract<LibraryItem, { kind: "installed" }>) { if (item.version.sourceScope === "default") return; setBusy("archive"); try { await workflowEditorApi.archive(item.version.name, item.version.version); await refresh(); setAnnouncement(`${item.version.name} ${item.version.version} archived.`); } catch { setAnnouncement("The workflow version could not be archived."); } finally { setBusy(""); } }
  async function validateDraft() {
    if (!draft || !persistence || persistence.kind !== "clean") return; const revision = persistence.revision; setValidation({ kind: "checking", revision });
    try { const report = await workflowEditorApi.validate({ id: draft.id, expectedRevision: revision }); setValidation(report.findings.length ? { kind: "invalid", revision, findings: report.findings } : { kind: "valid", revision, ...(report.digest ? { digest: report.digest } : {}) }); setAnnouncement(report.findings.length ? `${report.findings.length} validation findings.` : "Draft validation passed."); }
    catch { setValidation({ kind: "error", message: "Validation could not be completed." }); }
  }
  function add(type: WorkflowNodeType) { if (!document) return; const result = addNode(document, type); changeDocument(result.document, `${humanize(type)} node added.`); select({ kind: "node", nodeId: result.nodeId }); }
  function chooseConnectionTarget(nodeId: string) { if (!document || !connectFrom) return; changeDocument(connectNodes(document, connectFrom, nodeId), `Connected ${connectFrom} to ${nodeId}.`); setConnectFrom(undefined); }
  function onViewKeyDown(event: KeyboardEvent<HTMLButtonElement>, index: number) { const target = tabKeyTarget(index, event.key, editorViews.length); if (target === undefined) return; event.preventDefault(); setParams({ view: editorViews[target] }); tabRefs.current[target]?.focus(); }

  return <div className="page workflows-page workflow-authoring-page" onKeyDown={(event) => { if (event.key === "Escape") { if (newOpen) setNewOpen(false); setConnectFrom(undefined); select({ kind: "none" }); } }}>
    <PageHeader className="workflows-header" eyebrow="Configuration" title="Workflow authoring" description="Build drafts visually while installed workflow versions remain immutable." breadcrumbs={[{ label: "Workflows" }]} status={persistence ? <span title={"message" in persistence ? persistence.message : undefined} className={`editor-save-state editor-save-state--${persistence.kind}`}>{persistenceLabel(persistence)}</span> : undefined} actions={<><button className="button" type="button" disabled={!draft || persistence?.kind !== "clean"} onClick={() => void validateDraft()}>{validation.kind === "checking" ? "Validating…" : "Preview validation"}</button><button className="button button--primary" type="button" disabled title="Publishing workflow drafts is staged for DAR-138">Publish · staged</button></>} />
    <p className="sr-only" aria-live="polite">{announcement}</p>
    {loadState === "error" && <div className="board-notice board-notice--error" role="alert">Workflow library unavailable. Check daemon health and retry.</div>}
    <div className="workflow-editor-shell">
      <aside className="workflow-editor-left" aria-label="Workflow library and node palette">
        <div className="workflow-editor-left__actions"><button className="button button--primary button--compact" type="button" onClick={() => setNewOpen(true)}>New</button></div>
        <label className="workflow-search"><span className="sr-only">Search workflows by name, version, scope, or status</span><input type="search" value={query} onChange={(event) => setQuery(event.target.value)} placeholder="Search workflows…" /></label>
        <div className="workflow-library-list" aria-busy={loadState === "loading"}>{visibleItems.map((item) => <article key={itemKey(item)} className={`workflow-library-item${selectedKey === itemKey(item) ? " workflow-library-item--selected" : ""}`}><button type="button" onClick={() => selectItem(item)}><strong>{itemName(item)}</strong><span>{itemVersion(item)} · {itemScope(item)}</span><small>{humanize(item.kind)}</small></button><div>{item.kind === "installed" && <button type="button" disabled={Boolean(busy)} onClick={() => void duplicateInstalled(item)}>Duplicate as draft</button>}{item.kind === "installed" && <button type="button" disabled={item.version.sourceScope === "default" || Boolean(busy)} title={item.version.sourceScope === "default" ? "Built-in workflows are immutable" : undefined} onClick={() => void archiveInstalled(item)}>Archive</button>}</div></article>)}</div>
        {draft && <section className="node-palette" aria-labelledby="node-palette-title"><h2 id="node-palette-title">Add node</h2>{palette.map((item) => <button type="button" key={item.type} onClick={() => add(item.type)}><strong>{item.label}</strong><span>{item.description}</span></button>)}</section>}
      </aside>

      <section className="workflow-editor-center" aria-label="Workflow editor">
        {selected?.kind === "installed" ? <InstalledSummary item={selected} onDuplicate={() => void duplicateInstalled(selected)} /> : selected?.kind === "archived" ? <ArchivedSummary item={selected} /> : !draft || !document || !graph ? <div className="workflow-editor-empty"><h2>{loadState === "loading" ? "Loading workflow library…" : "Create or select a draft"}</h2><p>Start a workflow without editing YAML, or duplicate an installed version into an editable draft.</p></div> : <>
          <header className="workflow-editor-toolbar"><div><strong>{draft.name}</strong><span>Draft · revision {draft.revision} · {draft.scope}</span></div><div className="workflow-view-tabs" role="tablist" aria-label="Editor view">{editorViews.map((item, index) => <button key={item} ref={(element) => { tabRefs.current[index] = element; }} type="button" role="tab" id={`workflow-editor-tab-${item}`} aria-selected={view === item} aria-controls={`workflow-editor-panel-${item}`} tabIndex={view === item ? 0 : -1} onKeyDown={(event) => onViewKeyDown(event, index)} onClick={() => setParams({ view: item })}>{humanize(item)}</button>)}</div></header>
          <ValidationSlot state={validation} onSelect={(finding) => finding.nodeId && select({ kind: "node", nodeId: finding.nodeId })} />
          <div id="workflow-editor-panel-canvas" role="tabpanel" aria-labelledby="workflow-editor-tab-canvas" tabIndex={view === "canvas" ? 0 : -1} hidden={view !== "canvas"}>{view === "canvas" && <GraphCanvas graph={graph} selection={selection} connectFrom={connectFrom} onSelect={select} onConnect={chooseConnectionTarget} onMove={(id, x, y) => changeLayout(moveNode(layout, id, { x, y }), `Moved ${id}.`)} dragRef={dragRef} />}</div>
          <div id="workflow-editor-panel-structure" role="tabpanel" aria-labelledby="workflow-editor-tab-structure" tabIndex={view === "structure" ? 0 : -1} hidden={view !== "structure"}>{view === "structure" && <StructureView graph={graph} selection={selection} onSelect={select} onMove={(id, direction) => changeDocument(reorderNode(document, id, direction), `Reordered ${id}.`)} onRemoveNode={(id) => { changeDocument(removeNode(document, id), `Removed ${id}.`); select({ kind: "none" }); }} onRemoveEdge={(id) => changeDocument(removeEdge(document, id), "Transition removed.")} onConnect={(from, to, kind) => changeDocument(connectNodes(document, from, to, kind), `Connected ${from} to ${to}.`)} />}</div>
        </>}
      </section>

      <aside className="workflow-editor-inspector" aria-label="Contextual inspector">
        {draft && document && graph ? <Inspector node={selectedNode} edge={selectedEdge} connecting={connectFrom} onStartConnect={(id) => setConnectFrom(id)} onCancelConnect={() => setConnectFrom(undefined)} onChange={(id, change) => changeDocument(updateNode(document, id, change), `Updated ${id}.`)} onNudge={(id, dx, dy) => { const node = graph.nodes.find((item) => item.id === id); if (node) changeLayout(moveNode(layout, id, { x: node.position.x + dx, y: node.position.y + dy }), `Moved ${id}.`); }} /> : <div className="inspector-placeholder"><h2>Inspector</h2><p>Select an editable draft, node, or transition to inspect it.</p></div>}
      </aside>
    </div>
    {persistence?.kind === "conflict" && <ConflictBanner remote={persistence.remote} onUseRemote={() => { setDraft(persistence.remote); setDocument(structuredClone(persistence.remote.document) as JsonObject); setLayout(structuredClone(persistence.remote.layout) as JsonObject); setPersistence({ kind: "clean", revision: persistence.remote.revision }); setAnnouncement("Remote revision loaded. Local changes were replaced by your explicit choice."); }} onKeepLocal={() => setPersistence({ kind: "dirty", revision: persistence.remote.revision })} />}
    {newOpen && <div className="workflow-modal" role="presentation"><section role="dialog" aria-modal="true" aria-labelledby="new-workflow-title"><h2 id="new-workflow-title">New workflow draft</h2><form onSubmit={(event) => void createDraft(event)}><label className="field"><span>Workflow name</span><input required pattern="[a-z][a-z0-9._/-]{0,127}" autoFocus value={newName} onChange={(event) => setNewName(event.target.value)} /></label><label className="field"><span>Scope</span><select value={newScope} onChange={(event) => setNewScope(event.target.value as "user" | "project")}><option value="user">User</option><option value="project">Project</option></select></label><footer><button className="button" type="button" onClick={() => setNewOpen(false)}>Cancel</button><button className="button button--primary" type="submit" disabled={busy === "create"}>{busy === "create" ? "Creating…" : "Create draft"}</button></footer></form></section></div>}
  </div>;
}

function GraphCanvas({ graph, selection, connectFrom, onSelect, onConnect, onMove, dragRef }: { graph: EditorGraph; selection: EditorSelection; connectFrom?: string; onSelect(value: EditorSelection): void; onConnect(id: string): void; onMove(id: string, x: number, y: number): void; dragRef: React.MutableRefObject<{ id: string; offsetX: number; offsetY: number } | undefined> }) {
  const nodeMap = new Map(graph.nodes.map((node) => [node.id, node]));
  function down(event: ReactPointerEvent, node: VisualNode) { const target = event.currentTarget as SVGElement; target.setPointerCapture(event.pointerId); dragRef.current = { id: node.id, offsetX: event.clientX - node.position.x, offsetY: event.clientY - node.position.y }; onSelect({ kind: "node", nodeId: node.id }); }
  function move(event: ReactPointerEvent) { const drag = dragRef.current; if (drag) onMove(drag.id, event.clientX - drag.offsetX, event.clientY - drag.offsetY); }
  return <div className="workflow-canvas" tabIndex={0} aria-label="Workflow canvas. Arrow keys pan, or move the selected node." onKeyDown={(event) => { if (!event.key.startsWith("Arrow")) return; event.preventDefault(); const selected = selection.kind === "node" ? nodeMap.get(selection.nodeId) : undefined; const dx = event.key === "ArrowLeft" ? -20 : event.key === "ArrowRight" ? 20 : 0; const dy = event.key === "ArrowUp" ? -20 : event.key === "ArrowDown" ? 20 : 0; if (selected) onMove(selected.id, selected.position.x + dx, selected.position.y + dy); else event.currentTarget.scrollBy({ left: dx, top: dy }); }} onPointerMove={move} onPointerUp={() => { dragRef.current = undefined; }}><svg width="100%" height="100%" viewBox="0 0 900 620" role="img" aria-labelledby="workflow-canvas-title"><title id="workflow-canvas-title">Workflow graph. Every canvas action is also available in Structure view.</title><defs><marker id="edge-arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="6" markerHeight="6" orient="auto"><path d="M 0 0 L 10 5 L 0 10 z" /></marker></defs>{graph.edges.map((edge) => { const from = nodeMap.get(edge.from); const to = nodeMap.get(edge.to); if (!from || !to) return null; return <g key={edge.id} className={`canvas-edge canvas-edge--${edge.kind}`}><path d={`M ${from.position.x + 170} ${from.position.y + 45} C ${from.position.x + 210} ${from.position.y + 45}, ${to.position.x - 40} ${to.position.y + 45}, ${to.position.x} ${to.position.y + 45}`} markerEnd="url(#edge-arrow)" /><title>{edge.kind}: {edge.from} to {edge.to}</title></g>; })}{graph.nodes.map((node) => <g key={node.id} transform={`translate(${node.position.x} ${node.position.y})`} className={`canvas-node${selection.kind === "node" && selection.nodeId === node.id ? " canvas-node--selected" : ""}`} onPointerDown={(event) => down(event, node)}><rect width="170" height="90" rx="10" /><foreignObject width="170" height="90"><button type="button" aria-label={`${node.displayName}, ${humanize(node.type)} node${connectFrom ? ". Select as connection target" : ""}`} onClick={() => connectFrom ? onConnect(node.id) : onSelect({ kind: "node", nodeId: node.id })}><span>{humanize(node.type)}</span><strong>{node.displayName}</strong><small>{node.id}</small><i>{node.entry ? "Entry " : ""}{node.terminal ? "Terminal " : ""}{node.validationCount ? `${node.validationCount} validation` : ""}</i></button></foreignObject></g>)}</svg></div>;
}

function StructureView({ graph, selection, onSelect, onMove, onRemoveNode, onRemoveEdge, onConnect }: { graph: EditorGraph; selection: EditorSelection; onSelect(value: EditorSelection): void; onMove(id: string, direction: -1 | 1): void; onRemoveNode(id: string): void; onRemoveEdge(id: string): void; onConnect(from: string, to: string, kind: AuthoredTransitionKind): void }) {
  const [from, setFrom] = useState(graph.nodes[0]?.id ?? ""); const [to, setTo] = useState(graph.nodes[1]?.id ?? graph.nodes[0]?.id ?? ""); const [kind, setKind] = useState<AuthoredTransitionKind>("normal");
  return <div className="structure-view"><section><h2>Ordered nodes</h2><ol>{graph.nodes.map((node, index) => <li key={node.id} className={selection.kind === "node" && selection.nodeId === node.id ? "is-selected" : ""}><button type="button" onClick={() => onSelect({ kind: "node", nodeId: node.id })}><strong>{node.displayName}</strong><code>{node.id}</code><span>{humanize(node.type)} · {node.checkpoint} checkpoint{node.subworkflow ? ` · ${node.subworkflow}` : ""}</span><small>{node.entry ? "Entry " : ""}{node.terminal ? "Terminal " : ""}{node.validationCount} validation items</small></button><div><button type="button" aria-label={`Move ${node.id} earlier`} disabled={index === 0} onClick={() => onMove(node.id, -1)}>↑</button><button type="button" aria-label={`Move ${node.id} later`} disabled={index === graph.nodes.length - 1} onClick={() => onMove(node.id, 1)}>↓</button><button type="button" aria-label={`Remove ${node.id}`} onClick={() => onRemoveNode(node.id)}>Remove</button></div></li>)}</ol></section><section><h2>Transitions</h2><form onSubmit={(event) => { event.preventDefault(); onConnect(from, to, kind); }}><label>From<select value={from} onChange={(event) => setFrom(event.target.value)}>{graph.nodes.map((node) => <option key={node.id}>{node.id}</option>)}</select></label><label>To<select value={to} onChange={(event) => setTo(event.target.value)}>{graph.nodes.map((node) => <option key={node.id}>{node.id}</option>)}</select></label><label>Path type<select value={kind} onChange={(event) => setKind(event.target.value as AuthoredTransitionKind)}><option value="normal">Normal</option><option value="conditional">Conditional branch</option><option value="bounded_repair">Bounded repair</option></select></label><button className="button button--compact" type="submit">Add transition</button></form><div className="transition-table" role="table" aria-label="Workflow transitions">{graph.edges.map((edge) => <div role="row" key={edge.id}><button role="cell" type="button" onClick={() => onSelect({ kind: "edge", edgeId: edge.id })}>{edge.from} → {edge.to}</button><span role="cell">{humanize(edge.kind)}</span><button role="cell" type="button" onClick={() => onRemoveEdge(edge.id)}>Remove</button></div>)}</div></section></div>;
}

function Inspector({ node, edge, connecting, onStartConnect, onCancelConnect, onChange, onNudge }: { node?: VisualNode; edge?: VisualEdge; connecting?: string; onStartConnect(id: string): void; onCancelConnect(): void; onChange(id: string, change: { displayName?: string; entry?: boolean; terminal?: boolean; checkpoint?: "none" | "acknowledge" | "approve" }): void; onNudge(id: string, dx: number, dy: number): void }) {
  if (edge) return <div><p className="eyebrow">Transition</p><h2>{edge.transitionId}</h2><dl><dt>From</dt><dd>{edge.from}</dd><dt>To</dt><dd>{edge.to}</dd><dt>Path</dt><dd>{humanize(edge.kind)}</dd></dl><p>Use Structure view to remove or recreate this transition.</p></div>;
  if (!node) return <div className="inspector-placeholder"><h2>Inspector</h2><p>Select a node or transition. Press Escape to clear the current selection.</p></div>;
  return <div><p className="eyebrow">{humanize(node.type)} node</p><h2>{node.id}</h2><label className="field"><span>Display name</span><input value={node.displayName} onChange={(event) => onChange(node.id, { displayName: event.target.value })} /></label><div className="inspector-checks"><label><input type="checkbox" checked={node.entry} onChange={(event) => onChange(node.id, { entry: event.target.checked })} /> Entry-capable</label><label><input type="checkbox" checked={node.terminal} onChange={(event) => onChange(node.id, { terminal: event.target.checked })} /> Terminal-capable</label></div><label className="field"><span>Checkpoint behavior</span><select value={node.checkpoint} onChange={(event) => onChange(node.id, { checkpoint: event.target.value as "none" | "acknowledge" | "approve" })}><option value="none">None</option><option value="acknowledge">Acknowledge</option><option value="approve">Approve</option></select></label>{node.subworkflow && <p className="inspector-identity"><strong>Sub-workflow</strong><code>{node.subworkflow}</code></p>}<fieldset className="nudge-controls"><legend>Move on canvas</legend><button type="button" aria-label="Move node left" onClick={() => onNudge(node.id, -20, 0)}>←</button><button type="button" aria-label="Move node up" onClick={() => onNudge(node.id, 0, -20)}>↑</button><button type="button" aria-label="Move node down" onClick={() => onNudge(node.id, 0, 20)}>↓</button><button type="button" aria-label="Move node right" onClick={() => onNudge(node.id, 20, 0)}>→</button></fieldset>{connecting === node.id ? <button className="button" type="button" onClick={onCancelConnect}>Cancel connection</button> : <button className="button" type="button" onClick={() => onStartConnect(node.id)}>Start connection</button>}</div>;
}

function ValidationSlot({ state, onSelect }: { state: ValidationState; onSelect(finding: Schemas["WorkflowAuthoringFinding"]): void }) { if (state.kind === "not_run") return <div className="validation-slot">Validation not run for this revision.</div>; if (state.kind === "checking") return <div className="validation-slot" aria-live="polite">Validating revision {state.revision}…</div>; if (state.kind === "valid") return <div className="validation-slot validation-slot--valid">Validation passed · revision {state.revision}{state.digest ? ` · ${shortIdentifier(state.digest)}` : ""}</div>; if (state.kind === "error") return <div className="validation-slot validation-slot--error" role="alert">{state.message}</div>; return <details className="validation-slot validation-slot--error" open><summary>{state.findings.length} validation findings</summary>{state.findings.map((finding, index) => <button key={`${finding.code}:${index}`} type="button" onClick={() => onSelect(finding)}><strong>{finding.code}</strong><span>{finding.message}</span></button>)}</details>; }
function ConflictBanner({ remote, onUseRemote, onKeepLocal }: { remote: Schemas["WorkflowDraft"]; onUseRemote(): void; onKeepLocal(): void }) { return <section className="workflow-conflict" role="alertdialog" aria-labelledby="workflow-conflict-title"><h2 id="workflow-conflict-title">Another editor saved revision {remote.revision}</h2><p>Your local document and layout are retained. Choose which base to use explicitly.</p><button className="button" type="button" onClick={onUseRemote}>Use remote revision</button><button className="button button--primary" type="button" onClick={onKeepLocal}>Keep local and retry</button></section>; }
function InstalledSummary({ item, onDuplicate }: { item: Extract<LibraryItem, { kind: "installed" }>; onDuplicate(): void }) { return <div className="workflow-editor-empty"><span className="scope-badge">{item.version.sourceScope}</span><h2>{item.version.name}</h2><p>Installed version {item.version.version} is immutable. Duplicate it to make visual edits without changing the installed definition.</p><dl><dt>Digest</dt><dd><code>{shortIdentifier(item.version.digest)}</code></dd><dt>Source</dt><dd>{item.version.sourceReference}</dd></dl><button className="button button--primary" type="button" onClick={onDuplicate}>Duplicate as draft</button></div>; }
function ArchivedSummary({ item }: { item: Extract<LibraryItem, { kind: "archived" }> }) { return <div className="workflow-editor-empty"><h2>{item.archive.name}</h2><p>Version {item.archive.version} was archived. Its immutable history remains visible but it cannot be edited.</p></div>; }
function parseSelection(params: URLSearchParams): EditorSelection { const value = params.get("selection"); if (value?.startsWith("node:")) return { kind: "node", nodeId: value.slice(5) }; if (value?.startsWith("edge:")) return { kind: "edge", edgeId: value.slice(5) }; return { kind: "none" }; }
function serializeSelection(value: EditorSelection) { return value.kind === "none" ? undefined : value.kind === "node" ? `node:${value.nodeId}` : `edge:${value.edgeId}`; }
function itemKey(item: LibraryItem) { return item.kind === "draft" ? `draft:${item.draft.id}` : item.kind === "installed" ? `installed:${item.version.name}:${item.version.version}:${item.version.digest}` : `archived:${item.archive.name}:${item.archive.version}`; }
function itemName(item: LibraryItem) { return item.kind === "draft" ? item.draft.name : item.kind === "installed" ? item.version.name : item.archive.name; }
function itemVersion(item: LibraryItem) { return item.kind === "draft" ? `r${item.draft.revision}${item.draft.baseVersion ? ` from ${item.draft.baseVersion}` : ""}` : item.kind === "installed" ? `v${item.version.version}` : `v${item.archive.version}`; }
function itemScope(item: LibraryItem) { return item.kind === "draft" ? item.draft.scope : item.kind === "installed" ? item.version.sourceScope : "archive"; }
function itemSearch(item: LibraryItem) { return `${itemName(item)} ${itemVersion(item)} ${itemScope(item)} ${item.kind}`.toLowerCase(); }
