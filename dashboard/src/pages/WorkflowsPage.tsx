import { useEffect, useMemo, useRef, useState, type FormEvent, type KeyboardEvent } from "react";

import { ApiRequestError } from "../api/client";
import type { components } from "../api/schema.generated";
import { tabKeyTarget } from "../accessibility/keyboard";
import { useRouter } from "../app/router";
import { PageHeader } from "../components/PageStructure";
import { WorkflowPortCanvas, WorkflowPortConnections } from "./WorkflowPortGraph";
import { bindPorts, connectionError, derivePortGraph, type Port } from "./workflowPortModel";
import { WorkflowAuthoringInspector } from "./WorkflowAuthoringInspector";
import { humanize, shortIdentifier } from "./runDetailModel";
import { workflowEditorApi } from "./workflowEditorApi";
import {
  addNode, connectNodes, createStarterDocument, deriveEditorGraph, draftRevision, findingTarget, inspectNode, moveNode, normalizeLayout, nodeTypeAvailability, persistenceLabel, useNodeDefinition,
  removeEdge, removeNode, removeNodeLayout, renameNode, reorderNode, updateNodeExecutor, validationMatches,
  type AuthoredTransitionKind, type EditorGraph, type EditorSelection, type EditorView, type JsonObject, type PersistenceState,
  type PublishState, type RoutePreviewState, type ValidationState, type WorkflowNodeType,
} from "./workflowEditorModel";

type Schemas = components["schemas"];
type LibraryItem = { kind: "draft"; draft: Schemas["WorkflowDraft"] } | { kind: "installed"; version: Schemas["WorkflowVersionSummary"] } | { kind: "archived"; archive: Schemas["WorkflowArchive"] };
const editorViews: EditorView[] = ["canvas", "structure"];
const palette: { type: WorkflowNodeType; label: string; description: string }[] = [
  { type: "reasoning", label: "Reasoning", description: "Agent work" }, { type: "command", label: "Command", description: "Local command" },
  { type: "gate", label: "Gate", description: "Conditional branch" }, { type: "approval", label: "Approval", description: "Human checkpoint" },
  { type: "subworkflow", label: "Sub-workflow", description: "Pinned workflow call" }, { type: "point_execution", label: "Point execution", description: "Story point runner" },
  { type: "routing", label: "Route assessment", description: "Choose one declared downstream branch" },
];

export function WorkflowsPage() {
  const { search, navigate } = useRouter();
  const params = useMemo(() => new URLSearchParams(search), [search]);
  const [library, setLibrary] = useState<Schemas["WorkflowLibrary"]>({ versions: [], drafts: [], archives: [] });
  const [catalog, setCatalog] = useState<Schemas["WorkflowAuthoringCatalog"]>();
  const [definitions, setDefinitions] = useState<Schemas["NodeDefinition"][]>([]);
  const [definitionQuery, setDefinitionQuery] = useState("");
  const [definitionScope, setDefinitionScope] = useState<"" | "built_in" | "project" | "user">("");
  const [definitionLifecycle, setDefinitionLifecycle] = useState<"active" | "archived">("active");
  const [definitionName, setDefinitionName] = useState("nodes/custom");
  const [loadState, setLoadState] = useState<"loading" | "ready" | "error">("loading");
  const [query, setQuery] = useState("");
  const [draft, setDraft] = useState<Schemas["WorkflowDraft"]>();
  const [document, setDocument] = useState<JsonObject>();
  const [layout, setLayout] = useState<JsonObject>({});
  const [persistence, setPersistence] = useState<PersistenceState>();
  const [validation, setValidation] = useState<ValidationState>({ kind: "not_run" });
  const [publish, setPublish] = useState<PublishState>({ kind: "closed" });
  const [routePreview, setRoutePreview] = useState<RoutePreviewState>({ kind: "closed" });
  const [focusRequest, setFocusRequest] = useState(0);
  const [announcement, setAnnouncement] = useState("");
  const [connectFrom, setConnectFrom] = useState<string>();
  const [newOpen, setNewOpen] = useState(false);
  const [newName, setNewName] = useState("workflow/new-workflow");
  const [newScope, setNewScope] = useState<"user" | "project">("user");
  const [advancedOpen, setAdvancedOpen] = useState(false);
  const [advancedSource, setAdvancedSource] = useState("");
  const [advancedError, setAdvancedError] = useState("");
  const [busy, setBusy] = useState("");
  const tabRefs = useRef<Array<HTMLButtonElement | null>>([]);
  const [paletteQuery, setPaletteQuery] = useState("");
  const validationRequestRef = useRef(0);
  const previewRequestRef = useRef(0);
  const publishRequestRef = useRef(0);
  const semanticGenerationRef = useRef(0);
  const saveRequestRef = useRef(0);
  const currentDraftRef = useRef<{ id: string; revision: number; documentDigest: string } | undefined>(undefined);
  const currentDocumentRef = useRef<JsonObject | undefined>(undefined);
  const inspectorRef = useRef<HTMLElement | null>(null);
  const pageRef = useRef<HTMLDivElement | null>(null);
  const modalRestoreRef = useRef<HTMLElement | null>(null);
  const pendingFindingFocus = useRef<{ nodeId?: string; field?: string } | undefined>(undefined);
  const itemParam = params.get("item") ?? "";
  const view: EditorView = editorViews.includes(params.get("view") as EditorView) ? params.get("view") as EditorView : "structure";
  const selection = parseSelection(params);
  currentDraftRef.current = draft ? { id: draft.id, revision: draft.revision, documentDigest: draft.documentDigest } : undefined;
  currentDocumentRef.current = document;

  const refresh = async (signal?: AbortSignal) => { const next = await workflowEditorApi.library(signal); setLibrary(next); setLoadState("ready"); return next; };
  useEffect(() => { const abort = new AbortController(); void refresh(abort.signal).catch(() => { if (!abort.signal.aborted) setLoadState("error"); }); return () => abort.abort(); }, []);
  useEffect(() => { const abort = new AbortController(); void workflowEditorApi.catalog(abort.signal).then(setCatalog).catch(() => { /* form inputs remain available, but no server reference suggestions are invented */ }); return () => abort.abort(); }, []);
  const refreshDefinitions = async (signal?: AbortSignal) => { const values = await workflowEditorApi.definitions({ query: definitionQuery || undefined, scope: definitionScope || undefined, lifecycle: definitionLifecycle }, signal); setDefinitions(values); return values; };
  useEffect(() => { const abort = new AbortController(); const timer = window.setTimeout(() => void refreshDefinitions(abort.signal).catch(() => { if (!abort.signal.aborted) setAnnouncement("Node definitions could not be loaded."); }), 150); return () => { abort.abort(); window.clearTimeout(timer); }; }, [definitionQuery, definitionScope, definitionLifecycle]);

  const items = useMemo<LibraryItem[]>(() => [
    ...library.drafts.map((value) => ({ kind: "draft" as const, draft: value })),
    ...library.versions.map((value) => ({ kind: "installed" as const, version: value })),
    ...library.archives.map((value) => ({ kind: "archived" as const, archive: value })),
  ], [library]);
  const selected = items.find((item) => itemKey(item) === itemParam) ?? items.find((item) => item.kind === "draft") ?? items[0];
  const selectedKey = selected ? itemKey(selected) : "";

  useEffect(() => {
    if (selected?.kind !== "draft") { validationRequestRef.current += 1; previewRequestRef.current += 1; publishRequestRef.current += 1; semanticGenerationRef.current += 1; saveRequestRef.current += 1; setDraft(undefined); setDocument(undefined); setPersistence(undefined); setValidation({ kind: "not_run" }); setPublish({ kind: "closed" }); setRoutePreview({ kind: "closed" }); return; }
    if (draft?.id === selected.draft.id) return;
    setDraft(selected.draft); setDocument(structuredClone(selected.draft.document) as JsonObject); setLayout(structuredClone(selected.draft.layout) as JsonObject);
    validationRequestRef.current += 1; previewRequestRef.current += 1; publishRequestRef.current += 1; semanticGenerationRef.current += 1; saveRequestRef.current += 1; setPersistence({ kind: "clean", revision: selected.draft.revision }); setValidation({ kind: "not_run" }); setPublish({ kind: "closed" }); setRoutePreview({ kind: "closed" }); setConnectFrom(undefined);
  }, [selectedKey, draft?.id]);

  useEffect(() => {
    if (!draft || !document || persistence?.kind !== "dirty") return;
    const timer = window.setTimeout(() => {
      const revision = persistence.revision; const request = ++saveRequestRef.current; const draftId = draft.id; setPersistence({ kind: "saving", revision });
      void workflowEditorApi.save({ id: draft.id, expectedRevision: revision, document, layout }).then((saved) => {
        if (request !== saveRequestRef.current || currentDraftRef.current?.id !== draftId) return;
        setDraft(saved); setLibrary((current) => ({ ...current, drafts: current.drafts.map((item) => item.id === saved.id ? saved : item) }));
        setPersistence({ kind: "clean", revision: saved.revision }); setAnnouncement(`Draft saved at revision ${saved.revision}.`);
      }).catch(async (cause: unknown) => {
        if (request !== saveRequestRef.current || currentDraftRef.current?.id !== draftId) return;
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
  useEffect(() => { const pending = pendingFindingFocus.current; if (!pending) return; pendingFindingFocus.current = undefined; window.requestAnimationFrame(() => { const fields = [...(inspectorRef.current?.querySelectorAll<HTMLElement>("[data-workflow-field]") ?? [])].sort((left, right) => (right.dataset.workflowField?.length ?? 0) - (left.dataset.workflowField?.length ?? 0)); const target = fields.find((field) => pending.field === field.dataset.workflowField || pending.field?.startsWith(`${field.dataset.workflowField}.`))?.querySelector<HTMLElement>("input, select, textarea, button") ?? inspectorRef.current?.querySelector<HTMLElement>("input, select, textarea, button"); for (let parent = target?.parentElement; parent; parent = parent.parentElement) if (parent instanceof HTMLDetailsElement) parent.open = true; target?.focus(); }); }, [selection, document, focusRequest]);
  const modalActive = newOpen || advancedOpen || routePreview.kind === "available" || routePreview.kind === "error" || ["confirm", "publishing", "conflict", "invalid", "error"].includes(publish.kind);
  useEffect(() => {
    if (!modalActive) return;
    const root = pageRef.current; const dialog = root?.querySelector<HTMLElement>("[role=dialog]"); if (!root || !dialog) return;
    modalRestoreRef.current = globalThis.document.activeElement instanceof HTMLElement ? globalThis.document.activeElement : null;
    const inerted = [...root.children].filter((child) => !child.contains(dialog)) as HTMLElement[]; inerted.forEach((child) => { child.inert = true; });
    const focusable = () => [...dialog.querySelectorAll<HTMLElement>('button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])')];
    focusable()[0]?.focus();
    const trap = (event: globalThis.KeyboardEvent) => { if (event.key !== "Tab") return; const items = focusable(); if (!items.length) { event.preventDefault(); dialog.focus(); return; } const first = items[0], last = items.at(-1)!; if (event.shiftKey && globalThis.document.activeElement === first) { event.preventDefault(); last.focus(); } else if (!event.shiftKey && globalThis.document.activeElement === last) { event.preventDefault(); first.focus(); } };
    dialog.addEventListener("keydown", trap);
    return () => { dialog.removeEventListener("keydown", trap); inerted.forEach((child) => { child.inert = false; }); modalRestoreRef.current?.focus(); };
  }, [modalActive]);

  const graph = useMemo(() => document ? deriveEditorGraph(document, layout, validation.kind === "invalid" ? validation.findings : []) : undefined, [document, layout, validation]);
  const portGraph = useMemo(() => document && graph ? derivePortGraph(document, graph) : undefined, [document, graph]);
  const selectedNode = selection.kind === "node" ? graph?.nodes.find((node) => node.id === selection.nodeId) : undefined;
  const selectedEdge = selection.kind === "edge" ? graph?.edges.find((edge) => edge.id === selection.edgeId) : undefined;
  const visibleItems = items.filter((item) => itemSearch(item).includes(query.trim().toLowerCase()));
  const currentEvidence = draft && persistence ? { draftId: draft.id, revision: draftRevision(persistence), documentDigest: draft.documentDigest } : undefined;
  const validatedCurrentDocument = currentEvidence ? validationMatches(validation, currentEvidence) : false;

  const setParams = (changes: Record<string, string | undefined>, replace = false) => { const next = new URLSearchParams(params); for (const [key, value] of Object.entries(changes)) value === undefined ? next.delete(key) : next.set(key, value); navigate(`/workflows?${next}`, { replace }); };
  const selectItem = (item: LibraryItem) => setParams({ item: itemKey(item), selection: undefined, view: item.kind === "draft" ? view : "structure" });
  const select = (next: EditorSelection) => setParams({ selection: serializeSelection(next) }, true);
  const changeDocument = (next: JsonObject, message: string, edgeId?: string) => { if (!persistence || persistence.kind === "conflict" || persistence.kind === "saving" || publish.kind === "publishing") return; validationRequestRef.current += 1; previewRequestRef.current += 1; publishRequestRef.current += 1; semanticGenerationRef.current += 1; setDocument(next); setPersistence({ kind: "dirty", revision: draftRevision(persistence) }); setValidation({ kind: "not_run" }); setPublish({ kind: "closed" }); setRoutePreview({ kind: "closed" }); if (edgeId) select({ kind: "edge", edgeId }); setAnnouncement(message); };
  const changeLayout = (next: JsonObject, message: string) => { if (!persistence || persistence.kind === "conflict" || persistence.kind === "saving" || publish.kind === "publishing") return; setLayout(next); setPersistence({ kind: "dirty", revision: draftRevision(persistence) }); setAnnouncement(message); };

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
    if (!draft || !persistence || persistence.kind !== "clean") return; const evidence = { draftId: draft.id, revision: persistence.revision, documentDigest: draft.documentDigest }; const request = ++validationRequestRef.current; setValidation({ kind: "checking", ...evidence });
    try {
      const report = await workflowEditorApi.validate({ id: draft.id, expectedRevision: evidence.revision });
      if (request !== validationRequestRef.current || currentDraftRef.current?.id !== evidence.draftId) return;
      if (report.draftId !== evidence.draftId || report.revision !== evidence.revision || report.documentDigest !== evidence.documentDigest) { setValidation({ kind: "error", ...evidence, message: "Validation evidence did not match the requested draft revision." }); return; }
      if (report.findings.length) setValidation({ kind: "invalid", ...evidence, ...(report.digest ? { semanticDigest: report.digest } : {}), findings: report.findings });
      else if (report.digest) setValidation({ kind: "valid", ...evidence, semanticDigest: report.digest });
      else setValidation({ kind: "error", ...evidence, message: "Validation passed without a canonical semantic digest." });
      setAnnouncement(report.findings.length ? `${report.findings.length} validation findings.` : "Draft validation passed for the saved semantic document.");
    } catch (cause) { if (request === validationRequestRef.current) setValidation({ kind: "error", ...evidence, message: cause instanceof Error ? cause.message : "Validation could not be completed." }); }
  }
  async function previewDraft() {
    if (!draft || !persistence || persistence.kind !== "clean") return; const evidence = { draftId: draft.id, revision: persistence.revision, documentDigest: draft.documentDigest }; const request = ++previewRequestRef.current; setRoutePreview({ kind: "loading", ...evidence });
    try { const preview = await workflowEditorApi.preview({ id: draft.id, expectedRevision: evidence.revision, range: {}, context: {} }); if (request !== previewRequestRef.current || currentDraftRef.current?.id !== evidence.draftId || preview.draftId !== evidence.draftId || preview.revision !== evidence.revision || preview.documentDigest !== evidence.documentDigest) return; setRoutePreview({ kind: "available", preview }); setAnnouncement(`Stateless route preview includes ${preview.route.nodes.length} nodes.`); }
    catch (cause) { if (request === previewRequestRef.current && currentDraftRef.current?.id === evidence.draftId) setRoutePreview({ kind: "error", ...evidence, message: cause instanceof Error ? cause.message : "Route preview failed." }); }
  }
  function selectFinding(finding: Schemas["WorkflowAuthoringFinding"]) { if (!document) return; const target = findingTarget(document, finding); pendingFindingFocus.current = { nodeId: finding.nodeId, field: target.field }; setFocusRequest((value) => value + 1); select(target.selection); }
  function openPublish() {
    if (!draft || !persistence || persistence.kind !== "clean") return;
    const evidence = { draftId: draft.id, revision: persistence.revision, documentDigest: draft.documentDigest };
    if (!validationMatches(validation, evidence)) { setAnnouncement("Validate this saved semantic document before publishing."); return; }
    setPublish({ kind: "confirm", ...evidence, semanticDigest: validation.semanticDigest, version: suggestedVersion(draft, library.versions) });
  }
  async function publishDraft() {
    if (!draft || !persistence || persistence.kind !== "clean" || publish.kind !== "confirm") return;
    const currentEvidence = { draftId: draft.id, revision: persistence.revision, documentDigest: draft.documentDigest };
    if (!validationMatches(validation, currentEvidence) || publish.draftId !== draft.id || publish.documentDigest !== draft.documentDigest) { setPublish({ ...publish, kind: "error", message: "The draft changed. Validate the current semantic document again." }); return; }
    const request = { ...publish, revision: persistence.revision }; const requestToken = ++publishRequestRef.current; const generation = semanticGenerationRef.current; const expected = { name: draft.name, version: request.version, scope: draft.scope === "project" ? "project" : "user", reference: draft.scopeReference }; setPublish({ ...request, kind: "publishing" });
    try {
      const result = await workflowEditorApi.publish({ id: draft.id, version: request.version, expectedRevision: request.revision });
      if (requestToken !== publishRequestRef.current || generation !== semanticGenerationRef.current || currentDraftRef.current?.id !== request.draftId || currentDraftRef.current.documentDigest !== request.documentDigest) return;
      if (result.draftId !== request.draftId || result.draftRevision !== request.revision || result.sourceValidationDigest !== request.semanticDigest || result.published.name !== expected.name || result.published.version !== expected.version || result.published.sourceScope !== expected.scope || result.published.sourceReference !== expected.reference || !/^[0-9a-f]{64}$/.test(result.published.digest)) { setPublish({ ...request, kind: "error", message: "Publish returned evidence for a different validation, draft, or workflow identity." }); return; }
      setPublish({ kind: "published", result }); await refresh(); setAnnouncement(`${result.published.name} ${result.published.version} ${result.disposition === "already_installed" ? "was already installed" : "was published"} with digest ${shortIdentifier(result.published.digest)}.`);
    } catch (cause) {
      if (requestToken !== publishRequestRef.current || generation !== semanticGenerationRef.current) return;
      const message = cause instanceof Error ? cause.message : "Publishing failed.";
      if (cause instanceof ApiRequestError && cause.status === 409) { try { const remote = await workflowEditorApi.draft(draft.id); setPersistence({ kind: "conflict", revision: persistence.revision, remote }); } catch { /* local draft remains mounted */ } setPublish({ ...request, kind: "conflict", message: "The draft revision changed on the server. Local work is retained; resolve the conflict and validate again." }); }
      else if (cause instanceof ApiRequestError && cause.status === 422) { setPublish({ ...request, kind: "invalid", message: "Server validation rejected this publish. The draft remains editable; validate again for field findings." }); }
      else setPublish({ ...request, kind: "error", message });
    }
  }
  async function chooseSubworkflow(nodeId: string, version: Schemas["WorkflowVersionSummary"]) {
    if (!draft || !document) return; const draftId = draft.id; const generation = semanticGenerationRef.current;
    try {
      const definition = await workflowEditorApi.show(version.name, version.version);
      if (currentDraftRef.current?.id !== draftId || generation !== semanticGenerationRef.current || definition.version.digest !== version.digest) return;
      const currentDocument = currentDocumentRef.current; if (!currentDocument) return; const node = inspectNode(currentDocument, nodeId); if (!node || node.executor.type !== "subworkflow") return;
      const defaults = definition.document.spec.routeDefaults;
      changeDocument(updateNodeExecutor(currentDocument, nodeId, { ...node.executor, workflow: { name: version.name, version: version.version, digest: version.digest }, entry: defaults.entry, terminals: [...defaults.terminals] }), `Pinned ${nodeId} to ${version.name} ${version.version}.`);
    } catch { setAnnouncement("The selected installed sub-workflow definition could not be loaded."); }
  }
  function openAdvanced() { if (!document) return; setAdvancedSource(JSON.stringify(document, null, 2)); setAdvancedError(""); setAdvancedOpen(true); }
  function applyAdvanced() { try { const parsed = JSON.parse(advancedSource) as unknown; if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) throw new Error("The advanced document must be one JSON object."); changeDocument(parsed as JsonObject, "Advanced JSON applied. Server validation is required before publish."); setAdvancedOpen(false); } catch (cause) { setAdvancedError(cause instanceof Error ? cause.message : "Invalid JSON document."); } }
  function renameSelectedNode(from: string, to: string) { if (!document || !persistence || persistence.kind === "saving" || persistence.kind === "conflict" || publish.kind === "publishing") return; const result = renameNode(document, layout, from, to); if (result.preview.kind !== "ready") { setAnnouncement(result.preview.message); return; } validationRequestRef.current += 1; previewRequestRef.current += 1; publishRequestRef.current += 1; semanticGenerationRef.current += 1; setDocument(result.document); setLayout(result.layout); setPersistence({ kind: "dirty", revision: draftRevision(persistence) }); setValidation({ kind: "not_run" }); setPublish({ kind: "closed" }); setRoutePreview({ kind: "closed" }); select({ kind: "node", nodeId: to }); setAnnouncement(`Renamed ${from} to ${to} and rewrote ${result.preview.references} references atomically.`); }
  async function add(type: WorkflowNodeType) {
    if (!document) return;
    let result = addNode(document, type); let next = result.document;
    if (type === "reasoning") { const agent = catalog?.agents.status === "known" ? catalog.agents.items[0] : undefined; if (!agent) return; const node = inspectNode(next, result.nodeId); if (!node || node.executor.type !== "reasoning") return; next = updateNodeExecutor(next, result.nodeId, { ...node.executor, agent }); }
    if (type === "gate") { const policy = catalog?.policies.status === "known" ? catalog.policies.items[0] : undefined; if (!policy) return; const node = inspectNode(next, result.nodeId); if (!node || node.executor.type !== "gate") return; next = updateNodeExecutor(next, result.nodeId, { ...node.executor, policy }); }
    if (type === "subworkflow") {
      const version = catalog?.workflows.status === "known" ? catalog.workflows.items[0] : undefined; if (!version) return;
      try {
        const definition = await workflowEditorApi.show(version.name, version.version); if (definition.version.digest !== version.digest || currentDocumentRef.current !== document) return;
        const spec = next.spec as JsonObject; const rootInputs = (spec.inputs && typeof spec.inputs === "object" ? spec.inputs : {}) as JsonObject; const childInputs = definition.document.spec.inputs as Record<string, { type: string; schema?: string; description?: string }> | undefined; const rawNode = ((spec.nodes as JsonObject)[result.nodeId]) as JsonObject; const nodeInputs: JsonObject = {}; const mappings: Record<string, string> = {};
        for (const [childID, declaration] of Object.entries(childInputs ?? {})) { let parentID = childID; for (let suffix = 2; Object.hasOwn(rootInputs, parentID) && (rootInputs[parentID] as JsonObject).type !== declaration.type; suffix += 1) parentID = `${childID}_${suffix}`; if (!Object.hasOwn(rootInputs, parentID)) rootInputs[parentID] = structuredClone(declaration); nodeInputs[parentID] = { from: `run.input.${parentID}`, type: declaration.type, required: true }; mappings[childID] = parentID; }
        spec.inputs = rootInputs; rawNode.inputs = nodeInputs; const node = inspectNode(next, result.nodeId); if (!node || node.executor.type !== "subworkflow") return; next = updateNodeExecutor(next, result.nodeId, { ...node.executor, workflow: { name: version.name, version: version.version, digest: version.digest }, entry: definition.document.spec.routeDefaults.entry, terminals: [...definition.document.spec.routeDefaults.terminals], inputs: mappings, outputs: {} });
      } catch { setAnnouncement("The authoritative sub-workflow definition could not be loaded; no node was added."); return; }
    }
    if (!inspectNode(next, result.nodeId)) { setAnnouncement(`The ${humanize(type)} node could not be created from authoritative catalog data.`); return; }
    changeDocument(next, `${humanize(type)} node added.`); select({ kind: "node", nodeId: result.nodeId });
  }
  async function createDefinitionFromSelection() {
    if (!selectedNode || !document) return; const authored = inspectNode(document, selectedNode.id); if (!authored) return; setBusy("definition-create");
    try {
      const inputs = Object.fromEntries(authored.inputs.map((item) => [item.id, { type: item.type }]));
      const outputs = Object.fromEntries(authored.outputs.map((item) => [item.id, { type: item.type, ...(item.schema ? { schema: item.schema } : {}), required: item.required }]));
      const created = await workflowEditorApi.createDefinition({ scope: "user", owner: "local-user", name: definitionName.trim(), version: "1.0.0", displayName: authored.displayName, description: `Reusable ${authored.executor.type} node`, inputs, outputs, configurationSchema: { type: "object" }, implementation: authored.executor.type === "routing" ? "routing" : "executor", requiredCapabilities: [] });
      await refreshDefinitions(); changeDocument(useNodeDefinition(document!, selectedNode.id, created.ref), `${created.displayName} created and pinned to ${created.ref.version}.`);
    } catch (cause) { setAnnouncement(cause instanceof Error ? cause.message : "Node definition creation failed."); } finally { setBusy(""); }
  }
  async function duplicateDefinition(value: Schemas["NodeDefinition"]) { setBusy("definition-duplicate"); try { const created = await workflowEditorApi.duplicateDefinition({ source: value.ref, scope: "user", owner: "local-user", name: `${value.ref.name}-copy`, version: value.ref.version }); await refreshDefinitions(); setAnnouncement(`${created.displayName} duplicated as an editable user definition.`); } catch (cause) { setAnnouncement(cause instanceof Error ? cause.message : "Node definition duplication failed."); } finally { setBusy(""); } }
  async function versionDefinition(value: Schemas["NodeDefinition"]) { setBusy("definition-version"); try { const created = await workflowEditorApi.versionDefinition({ source: value.ref, version: nextPatch(value.ref.version) }); await refreshDefinitions(); setAnnouncement(`${created.displayName} ${created.ref.version} created.`); } catch (cause) { setAnnouncement(cause instanceof Error ? cause.message : "Node definition versioning failed."); } finally { setBusy(""); } }
  async function archiveDefinition(value: Schemas["NodeDefinition"]) { setBusy("definition-archive"); try { await workflowEditorApi.archiveDefinition({ ref: value.ref }); await refreshDefinitions(); setAnnouncement(`${value.displayName} ${value.ref.version} archived. Existing workflow references remain exact.`); } catch (cause) { setAnnouncement(cause instanceof Error ? cause.message : "Node definition archival failed."); } finally { setBusy(""); } }
  function focusNode(nodeId: string, field?: string) { if (connectFrom && !field && document) { if (connectFrom === nodeId) { setAnnouncement("Self transitions require a bounded repair path in Structure view."); return; } changeDocument(connectNodes(document, connectFrom, nodeId), "Execution ports connected."); setConnectFrom(undefined); return; } pendingFindingFocus.current = { nodeId, field }; setFocusRequest((value) => value + 1); select(document && field ? findingTarget(document, { nodeId, field, code: "PORT_FIELD", severity: "error", message: "" }).selection : { kind: "node", nodeId }); }
  function connectPorts(source: Port, target: Port) {
    if (!document || !portGraph) return;
    const error = connectionError(source, target); if (error) { setAnnouncement(error); return; }
    if (source.kind === "execution" && target.kind === "execution") changeDocument(connectNodes(document, source.nodeId, target.nodeId), "Execution ports connected.");
    else { const result = bindPorts(document, source, target, portGraph.ports); if (result.kind === "invalid") setAnnouncement(result.message); else changeDocument(result.document, "Data ports connected."); }
  }
  function onViewKeyDown(event: KeyboardEvent<HTMLButtonElement>, index: number) { const target = tabKeyTarget(index, event.key, editorViews.length); if (target === undefined) return; event.preventDefault(); setParams({ view: editorViews[target] }); tabRefs.current[target]?.focus(); }

  return <div ref={pageRef} className="page workflows-page workflow-authoring-page" onKeyDown={(event) => { if (event.key === "Escape") { if (newOpen) setNewOpen(false); if (advancedOpen) setAdvancedOpen(false); if (routePreview.kind !== "loading") setRoutePreview({ kind: "closed" }); if (publish.kind !== "publishing" && publish.kind !== "closed") setPublish({ kind: "closed" }); setConnectFrom(undefined); select({ kind: "none" }); } }}>
    <PageHeader className="workflows-header" eyebrow="Configuration" title="Workflow authoring" description="Build drafts visually while installed workflow versions remain immutable." breadcrumbs={[{ label: "Workflows" }]} status={persistence ? <span title={"message" in persistence ? persistence.message : undefined} className={`editor-save-state editor-save-state--${persistence.kind}`}>{persistenceLabel(persistence)}</span> : undefined} actions={<><button className="button" type="button" disabled={!draft || persistence?.kind !== "clean"} onClick={() => void previewDraft()}>{routePreview.kind === "loading" ? "Previewing…" : "Preview route"}</button><button className="button" type="button" disabled={!draft || persistence?.kind !== "clean"} onClick={() => void validateDraft()}>{validation.kind === "checking" ? "Validating…" : "Validate draft"}</button><button className="button" type="button" disabled={!document || persistence?.kind === "saving" || persistence?.kind === "conflict"} onClick={openAdvanced}>Advanced JSON</button><button className="button button--primary" type="button" disabled={!validatedCurrentDocument || persistence?.kind !== "clean"} title={!validatedCurrentDocument ? "Validate the current saved semantic document before publishing" : undefined} onClick={openPublish}>Publish</button></>} />
    <p className="sr-only" aria-live="polite">{announcement}</p>
    {loadState === "error" && <div className="board-notice board-notice--error" role="alert">Workflow library unavailable. Check daemon health and retry.</div>}
    <div className="workflow-editor-shell">
      <aside className="workflow-editor-left" aria-label="Workflow library and node palette">
        <div className="workflow-editor-left__actions"><button className="button button--primary button--compact" type="button" onClick={() => setNewOpen(true)}>New</button></div>
        <label className="workflow-search"><span className="sr-only">Search workflows by name, version, scope, or status</span><input type="search" value={query} onChange={(event) => setQuery(event.target.value)} placeholder="Search workflows…" /></label>
        <div className="workflow-library-list" aria-busy={loadState === "loading"}>{visibleItems.map((item) => <article key={itemKey(item)} className={`workflow-library-item${selectedKey === itemKey(item) ? " workflow-library-item--selected" : ""}`}><button type="button" onClick={() => selectItem(item)}><strong>{itemName(item)}</strong><span>{itemVersion(item)} · {itemScope(item)}</span><small>{humanize(item.kind)}</small></button><div>{item.kind === "installed" && <button type="button" disabled={Boolean(busy)} onClick={() => void duplicateInstalled(item)}>Duplicate as draft</button>}{item.kind === "installed" && <button type="button" disabled={item.version.sourceScope === "default" || Boolean(busy)} title={item.version.sourceScope === "default" ? "Built-in workflows are immutable" : undefined} onClick={() => void archiveInstalled(item)}>Archive</button>}</div></article>)}</div>
        {draft && <section className="node-palette" aria-labelledby="node-palette-title"><h2 id="node-palette-title">Add node</h2><label className="field"><span>Search nodes</span><input type="search" value={paletteQuery} onChange={(event) => setPaletteQuery(event.target.value)} /></label>{palette.filter((item) => `${item.label} ${item.description}`.toLowerCase().includes(paletteQuery.toLowerCase().trim())).map((item) => { const availability = nodeTypeAvailability(item.type, catalog); return <button type="button" key={item.type} disabled={!availability.available} title={availability.message} onClick={() => void add(item.type)}><strong>{item.label}</strong><span>{availability.message ?? item.description}</span></button>; })}</section>}
        {draft && <section className="node-palette" aria-labelledby="definition-library-title"><h2 id="definition-library-title">Reusable node definitions</h2><label className="field"><span>Search definitions</span><input type="search" value={definitionQuery} onChange={(event) => setDefinitionQuery(event.target.value)} /></label><label className="field"><span>Scope</span><select value={definitionScope} onChange={(event) => setDefinitionScope(event.target.value as typeof definitionScope)}><option value="">All scopes</option><option value="built_in">Built-in</option><option value="project">Project</option><option value="user">User</option></select></label><label className="field"><span>Lifecycle</span><select value={definitionLifecycle} onChange={(event) => setDefinitionLifecycle(event.target.value as typeof definitionLifecycle)}><option value="active">Active</option><option value="archived">Archived</option></select></label>{selectedNode && <div><label className="field"><span>Definition name</span><input value={definitionName} pattern="[a-z][a-z0-9._/-]{0,127}" onChange={(event) => setDefinitionName(event.target.value)} /></label><button type="button" disabled={Boolean(busy) || !definitionName.trim()} onClick={() => void createDefinitionFromSelection()}>Create from selected node</button></div>}{definitions.map((value) => { const immutable = value.ref.scope === "built_in"; return <article key={`${value.ref.scope}:${"owner" in value.ref ? value.ref.owner : ""}:${value.ref.name}:${value.ref.version}:${value.ref.digest}`} className="workflow-library-item"><strong>{value.displayName}</strong><span>{value.ref.version} · {humanize(value.ref.scope)} · {value.compatibility}</span><small>{value.implementation} · {(value.usage ?? []).length} workflow use{(value.usage ?? []).length === 1 ? "" : "s"}</small><div><button type="button" disabled={!selectedNode || value.lifecycle !== "active" || Boolean(busy)} onClick={() => { if (selectedNode && document) changeDocument(useNodeDefinition(document, selectedNode.id, value.ref), `${selectedNode.id} now uses ${value.ref.name} ${value.ref.version}.`); }}>Use</button><button type="button" disabled={Boolean(busy)} onClick={() => void duplicateDefinition(value)}>Duplicate</button><button type="button" disabled={immutable || value.lifecycle !== "active" || Boolean(busy)} title={immutable ? "Built-in definitions are immutable; duplicate one to derive an editable definition." : undefined} onClick={() => void versionDefinition(value)}>New version</button><button type="button" disabled={immutable || value.lifecycle !== "active" || Boolean(busy)} title={immutable ? "Built-in definitions are immutable." : undefined} onClick={() => void archiveDefinition(value)}>Archive</button></div></article>; })}</section>}
      </aside>

      <section className="workflow-editor-center" aria-label="Workflow editor">
        {selected?.kind === "installed" ? <InstalledSummary item={selected} onDuplicate={() => void duplicateInstalled(selected)} /> : selected?.kind === "archived" ? <ArchivedSummary item={selected} /> : !draft || !document || !graph ? <div className="workflow-editor-empty"><h2>{loadState === "loading" ? "Loading workflow library…" : "Create or select a draft"}</h2><p>Start a workflow without editing YAML, or duplicate an installed version into an editable draft.</p></div> : <>
          <header className="workflow-editor-toolbar"><div><strong>{draft.name}</strong><span>Draft · revision {draft.revision} · {draft.scope}</span></div><div className="workflow-view-tabs" role="tablist" aria-label="Editor view">{editorViews.map((item, index) => <button key={item} ref={(element) => { tabRefs.current[index] = element; }} type="button" role="tab" id={`workflow-editor-tab-${item}`} aria-selected={view === item} aria-controls={`workflow-editor-panel-${item}`} tabIndex={view === item ? 0 : -1} onKeyDown={(event) => onViewKeyDown(event, index)} onClick={() => setParams({ view: item })}>{humanize(item)}</button>)}</div></header>
          {portGraph && <WorkflowPortConnections graph={graph} ports={portGraph} onConnect={connectPorts} onFocus={focusNode} />}
          <ValidationSlot state={validation} currentRevision={draft.revision} onSelect={selectFinding} />
          <div id="workflow-editor-panel-canvas" role="tabpanel" aria-labelledby="workflow-editor-tab-canvas" tabIndex={view === "canvas" ? 0 : -1} hidden={view !== "canvas"}>{view === "canvas" && <WorkflowPortCanvas graph={graph} ports={portGraph!} layout={normalizeLayout(layout)} selection={selection} onSelect={select} onConnect={connectPorts} onFocus={focusNode} onLayout={changeLayout} />}</div>
          <div id="workflow-editor-panel-structure" role="tabpanel" aria-labelledby="workflow-editor-tab-structure" tabIndex={view === "structure" ? 0 : -1} hidden={view !== "structure"}>{view === "structure" && <StructureView graph={graph} selection={selection} onSelect={(value) => { if (value.kind === "node" && connectFrom) { changeDocument(connectNodes(document, connectFrom, value.nodeId), "Execution ports connected."); setConnectFrom(undefined); } else if (value.kind === "node") focusNode(value.nodeId); else select(value); }} onMove={(id, direction) => changeDocument(reorderNode(document, id, direction), `Reordered ${id}.`)} onRemoveNode={(id) => { changeDocument(removeNode(document, id), `Removed ${id}.`); select({ kind: "none" }); }} onRemoveEdge={(id) => changeDocument(removeEdge(document, id), "Transition removed.")} onConnect={(from, to, kind) => changeDocument(connectNodes(document, from, to, kind), `Connected ${from} to ${to}.`)} />}</div>
        </>}
      </section>

      <aside ref={inspectorRef} className="workflow-editor-inspector" aria-label="Contextual inspector">
        {draft && document && graph ? <WorkflowAuthoringInspector document={document} layout={layout} node={selectedNode} edge={selectedEdge} versions={catalog?.workflows.status === "known" ? catalog.workflows.items : []} catalog={catalog} findings={validation.kind === "invalid" ? validation.findings : []} connecting={connectFrom} onStartConnect={(id) => setConnectFrom(id)} onCancelConnect={() => setConnectFrom(undefined)} onChange={changeDocument} onChooseSubworkflow={(id, version) => void chooseSubworkflow(id, version)} onRenameNode={renameSelectedNode} onRemoveNode={(id) => { const next = removeNode(document, id); if (JSON.stringify(next) === JSON.stringify(document)) { setAnnouncement(`Cannot remove ${id} while an authoritative reference remains.`); return; } setLayout(removeNodeLayout(layout, id)); changeDocument(next, `Removed ${id}.`); select({ kind: "none" }); }} onRemoveEdge={(id) => { changeDocument(removeEdge(document, id), "Transition removed."); select({ kind: "none" }); }} onNudge={(id, dx, dy) => { const node = graph.nodes.find((item) => item.id === id); if (node) changeLayout(moveNode(layout, id, { x: node.position.x + dx, y: node.position.y + dy }), `Moved ${id}.`); }} /> : <div className="inspector-placeholder"><h2>Inspector</h2><p>Select an editable draft, node, or transition to inspect it.</p></div>}
      </aside>
    </div>
    {persistence?.kind === "conflict" && <ConflictBanner remote={persistence.remote} onUseRemote={() => { setDraft(persistence.remote); setDocument(structuredClone(persistence.remote.document) as JsonObject); setLayout(structuredClone(persistence.remote.layout) as JsonObject); setPersistence({ kind: "clean", revision: persistence.remote.revision }); setAnnouncement("Remote revision loaded. Local changes were replaced by your explicit choice."); }} onKeepLocal={() => setPersistence({ kind: "dirty", revision: persistence.remote.revision })} />}
    {newOpen && <div className="workflow-modal" role="presentation"><section role="dialog" aria-modal="true" aria-labelledby="new-workflow-title"><h2 id="new-workflow-title">New workflow draft</h2><form onSubmit={(event) => void createDraft(event)}><label className="field"><span>Workflow name</span><input required pattern="[a-z][a-z0-9._/-]{0,127}" autoFocus value={newName} onChange={(event) => setNewName(event.target.value)} /></label><label className="field"><span>Scope</span><select value={newScope} onChange={(event) => setNewScope(event.target.value as "user" | "project")}><option value="user">User</option><option value="project">Project</option></select></label><footer><button className="button" type="button" onClick={() => setNewOpen(false)}>Cancel</button><button className="button button--primary" type="submit" disabled={busy === "create"}>{busy === "create" ? "Creating…" : "Create draft"}</button></footer></form></section></div>}
    {advancedOpen && <div className="workflow-modal" role="presentation"><section className="workflow-modal--wide" role="dialog" aria-modal="true" aria-labelledby="advanced-workflow-title"><h2 id="advanced-workflow-title">Advanced workflow JSON</h2><p>This lossless escape hatch edits the same draft. It cannot bypass autosave, authoritative validation, or exact-revision publishing.</p><label className="field"><span>Workflow document JSON</span><textarea rows={20} spellCheck={false} value={advancedSource} onChange={(event) => setAdvancedSource(event.target.value)} /></label>{advancedError && <p role="alert">{advancedError}</p>}<footer><button className="button" type="button" onClick={() => setAdvancedOpen(false)}>Cancel</button><button className="button button--primary" type="button" onClick={applyAdvanced}>Apply to draft</button></footer></section></div>}
    {(routePreview.kind === "available" || routePreview.kind === "error") && <div className="workflow-modal" role="presentation"><section className="workflow-modal--wide" role="dialog" aria-modal="true" aria-labelledby="preview-workflow-title"><h2 id="preview-workflow-title">Stateless route preview</h2>{routePreview.kind === "error" ? <p role="alert">{routePreview.message}</p> : <><p>This preview does not save or publish. It is bound to draft revision {routePreview.preview.revision}, document <code>{shortIdentifier(routePreview.preview.documentDigest)}</code>, and canonical route digest <code>{shortIdentifier(routePreview.preview.digest)}</code>.</p><dl className="workflow-preview-boundaries"><dt>Entry</dt><dd>{routePreview.preview.route.entry}</dd><dt>Terminals</dt><dd>{routePreview.preview.route.terminals.join(", ")}</dd></dl><h3>Included nodes</h3><ol>{routePreview.preview.route.nodes.map((node) => <li key={node.id}>{node.id}</li>)}</ol><h3>Transitions</h3><ul>{routePreview.preview.route.transitions.map((transition) => <li key={`${transition.from}:${transition.id}:${transition.to}`}>{transition.from} → {transition.to} ({transition.id})</li>)}</ul>{routePreview.preview.route.inputRequirements.length > 0 && <><h3>Required run inputs</h3><ul>{routePreview.preview.route.inputRequirements.map((input) => <li key={`${input.node}:${input.input}`}>{input.node}.{input.input} from {input.source} ({input.code})</li>)}</ul></>}</>}<footer><button className="button button--primary" type="button" onClick={() => setRoutePreview({ kind: "closed" })}>Close preview</button></footer></section></div>}
    {(publish.kind === "confirm" || publish.kind === "publishing" || publish.kind === "conflict" || publish.kind === "invalid" || publish.kind === "error") && <div className="workflow-modal" role="presentation"><section role="dialog" aria-modal="true" aria-labelledby="publish-workflow-title"><h2 id="publish-workflow-title">Publish immutable workflow</h2><p>Publishing uses draft <code>{publish.draftId}</code>, saved document <code>{shortIdentifier(publish.documentDigest)}</code>, canonical validation <code>{shortIdentifier(publish.semanticDigest)}</code>, and current revision {publish.revision}.</p><label className="field"><span>New semantic version</span><input required pattern="[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?" value={publish.version} disabled={publish.kind === "publishing"} onChange={(event) => setPublish({ ...publish, version: event.target.value })} /></label>{"message" in publish && <p role="alert">{publish.message}</p>}<footer><button className="button" type="button" disabled={publish.kind === "publishing"} onClick={() => setPublish({ kind: "closed" })}>Cancel</button><button className="button button--primary" type="button" disabled={publish.kind === "publishing"} onClick={() => void publishDraft()}>{publish.kind === "publishing" ? "Publishing…" : "Publish exact revision"}</button></footer></section></div>}
    {publish.kind === "published" && <div className="workflow-publish-result" role="status"><strong>{publish.result.published.name} {publish.result.published.version}</strong><span>{publish.result.disposition === "created" ? "Published" : "Already installed"} · {shortIdentifier(publish.result.published.digest)}</span><button type="button" onClick={() => { setPublish({ kind: "closed" }); setParams({ item: `installed:${publish.result.published.name}:${publish.result.published.version}:${publish.result.published.digest}`, selection: undefined }); }}>Open immutable version</button></div>}
  </div>;
}

function StructureView({ graph, selection, onSelect, onMove, onRemoveNode, onRemoveEdge, onConnect }: { graph: EditorGraph; selection: EditorSelection; onSelect(value: EditorSelection): void; onMove(id: string, direction: -1 | 1): void; onRemoveNode(id: string): void; onRemoveEdge(id: string): void; onConnect(from: string, to: string, kind: AuthoredTransitionKind): void }) {
  const [from, setFrom] = useState(graph.nodes[0]?.id ?? ""); const [to, setTo] = useState(graph.nodes[1]?.id ?? graph.nodes[0]?.id ?? ""); const [kind, setKind] = useState<AuthoredTransitionKind>("normal");
  return <div className="structure-view"><section><h2>Ordered nodes</h2><ol>{graph.nodes.map((node, index) => <li key={node.id} className={selection.kind === "node" && selection.nodeId === node.id ? "is-selected" : ""}><button type="button" onClick={() => onSelect({ kind: "node", nodeId: node.id })}><strong>{node.displayName}</strong><code>{node.id}</code><span>{humanize(node.type)} · {node.checkpoint} checkpoint{node.subworkflow ? ` · ${node.subworkflow}` : ""}</span><small>{node.entry ? "Entry " : ""}{node.terminal ? "Terminal " : ""}{node.validationCount} validation items</small></button><div><button type="button" aria-label={`Move ${node.id} earlier`} disabled={index === 0} onClick={() => onMove(node.id, -1)}>↑</button><button type="button" aria-label={`Move ${node.id} later`} disabled={index === graph.nodes.length - 1} onClick={() => onMove(node.id, 1)}>↓</button><button type="button" aria-label={`Remove ${node.id}`} onClick={() => onRemoveNode(node.id)}>Remove</button></div></li>)}</ol></section><section><h2>Transitions</h2><form onSubmit={(event) => { event.preventDefault(); onConnect(from, to, kind); }}><label>From<select value={from} onChange={(event) => setFrom(event.target.value)}>{graph.nodes.map((node) => <option key={node.id}>{node.id}</option>)}</select></label><label>To<select value={to} onChange={(event) => setTo(event.target.value)}>{graph.nodes.map((node) => <option key={node.id}>{node.id}</option>)}</select></label><label>Path type<select value={kind} onChange={(event) => setKind(event.target.value as AuthoredTransitionKind)}><option value="normal">Normal</option><option value="conditional">Conditional branch</option><option value="bounded_repair">Bounded repair</option></select></label><button className="button button--compact" type="submit">Add transition</button></form><div className="transition-table" role="table" aria-label="Workflow transitions">{graph.edges.map((edge) => <div role="row" key={edge.id}><button role="cell" type="button" onClick={() => onSelect({ kind: "edge", edgeId: edge.id })}>{edge.from} → {edge.to}</button><span role="cell">{humanize(edge.kind)}</span><button role="cell" type="button" onClick={() => onRemoveEdge(edge.id)}>Remove</button></div>)}</div></section></div>;
}

function ValidationSlot({ state, currentRevision, onSelect }: { state: ValidationState; currentRevision: number; onSelect(finding: Schemas["WorkflowAuthoringFinding"]): void }) {
  if (state.kind === "not_run") return <div className="validation-slot">Validation not run for this semantic document.</div>;
  if (state.kind === "checking") return <div className="validation-slot" aria-live="polite">Validating saved revision {state.revision}…</div>;
  if (state.kind === "valid") return <div className="validation-slot validation-slot--valid">Validation passed · semantic document {shortIdentifier(state.documentDigest)} · canonical {shortIdentifier(state.semanticDigest)}{state.revision !== currentRevision ? ` · layout is now revision ${currentRevision}` : ""}</div>;
  if (state.kind === "error") return <div className="validation-slot validation-slot--error" role="alert">{state.message}</div>;
  const groups = [
    { label: "Global", values: state.findings.filter((finding) => !finding.nodeId) },
    { label: "Nodes and fields", values: state.findings.filter((finding) => finding.nodeId && !finding.edgeId && !finding.field?.startsWith("transitions.")) },
    { label: "Transitions", values: state.findings.filter((finding) => finding.edgeId || finding.field?.startsWith("transitions.")) },
  ].filter((group) => group.values.length);
  return <details className="validation-slot validation-slot--error" open><summary>{state.findings.length} authoritative validation findings</summary>{groups.map((group) => <section key={group.label}><h3>{group.label}</h3>{group.values.map((finding, index) => <button key={`${finding.code}:${finding.location ?? index}`} type="button" onClick={() => onSelect(finding)}><strong>{finding.code}</strong><span>{finding.field ? `${finding.field}: ` : ""}{finding.message}</span></button>)}</section>)}</details>;
}
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
function suggestedVersion(draft: Schemas["WorkflowDraft"], versions: readonly Schemas["WorkflowVersionSummary"][]) { const candidates = versions.filter((version) => version.name === draft.name).map((version) => /^([0-9]+)\.([0-9]+)\.([0-9]+)$/.exec(version.version)).filter((value): value is RegExpExecArray => Boolean(value)).sort((left, right) => Number(right[1]) - Number(left[1]) || Number(right[2]) - Number(left[2]) || Number(right[3]) - Number(left[3])); const latest = candidates[0]; return latest ? `${latest[1]}.${latest[2]}.${Number(latest[3]) + 1}` : "1.0.0"; }
function nextPatch(version: string) { const match = /^(\d+)\.(\d+)\.(\d+)$/.exec(version); return match ? `${match[1]}.${match[2]}.${Number(match[3]) + 1}` : "1.0.0"; }
