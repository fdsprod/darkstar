import { useEffect, useMemo, useRef, useState, type FormEvent, type KeyboardEvent } from "react";

import { ApiRequestError, apiClient } from "../api/client";
import type { components } from "../api/schema.generated";
import { tabKeyTarget } from "../accessibility/keyboard";
import { useDashboardState } from "../state/DashboardStateProvider";
import { formatDate, SummaryFact } from "./WorkDetailPage";
import { humanize, shortIdentifier } from "./runDetailModel";
import { buildWorkflowPreviewRequest, decodeAuthoredWorkflow, previewImpact, readinessAdvice, sortWorkflowVersions, type AuthoredWorkflow } from "./workflowPreviewModel";

type Schemas = components["schemas"];
type WorkflowTab = "preview" | "graph" | "readiness" | "definition";
const workflowTabs: WorkflowTab[] = ["preview", "graph", "readiness", "definition"];

export function WorkflowsPage() {
  const { state } = useDashboardState();
  const [catalog, setCatalog] = useState<Schemas["WorkflowVersionSummary"][]>([]);
  const [selectedKey, setSelectedKey] = useState("");
  const [query, setQuery] = useState("");
  const [catalogState, setCatalogState] = useState<"loading" | "ready" | "error">("loading");
  const [definition, setDefinition] = useState<Schemas["WorkflowDefinition"]>();
  const [graph, setGraph] = useState<Schemas["WorkflowGraph"]>();
  const [detailError, setDetailError] = useState("");
  const [tab, setTab] = useState<WorkflowTab>("preview");
  const tabRefs = useRef<Array<HTMLButtonElement | null>>([]);
  function onTabKeyDown(event: KeyboardEvent<HTMLButtonElement>, index: number) {
    const target = tabKeyTarget(index, event.key, workflowTabs.length);
    if (target === undefined) return;
    event.preventDefault(); setTab(workflowTabs[target]); tabRefs.current[target]?.focus();
  }

  useEffect(() => {
    const abort = new AbortController();
    setCatalogState((current) => current === "ready" ? "ready" : "loading");
    void apiClient.listWorkflows(undefined, abort.signal).then((values) => {
      const sorted = sortWorkflowVersions(values);
      setCatalog(sorted); setCatalogState("ready");
      setSelectedKey((current) => sorted.some((item) => workflowKey(item) === current) ? current : sorted[0] ? workflowKey(sorted[0]) : "");
    }).catch(() => { if (!abort.signal.aborted) setCatalogState("error"); });
    return () => abort.abort();
  }, [state.cursor]);

  const selected = catalog.find((item) => workflowKey(item) === selectedKey);
  useEffect(() => {
    if (!selected) { setDefinition(undefined); setGraph(undefined); return; }
    const abort = new AbortController();
    setDefinition(undefined); setGraph(undefined); setDetailError("");
    void Promise.all([
      apiClient.showWorkflow(selected.name, selected.version, abort.signal),
      apiClient.graphWorkflow(selected.name, selected.version, abort.signal),
    ]).then(([nextDefinition, nextGraph]) => { setDefinition(nextDefinition); setGraph(nextGraph); })
      .catch(() => { if (!abort.signal.aborted) setDetailError("The selected immutable workflow definition is temporarily unavailable."); });
    return () => abort.abort();
  }, [selectedKey]);

  const decoded = definition ? decodeAuthoredWorkflow(definition.document) : undefined;
  const visibleCatalog = catalog.filter((item) => `${item.name} ${item.version} ${item.sourceScope}`.toLowerCase().includes(query.trim().toLowerCase()));

  return <div className="page workflows-page">
    <header className="page-header workflows-header"><div><p className="eyebrow">Configuration</p><h1>Workflows</h1><p className="page-header__description">Inspect installed immutable workflow versions and preview a validated route without changing a run.</p></div><span className="preview-only-badge">Read-only catalog</span></header>
    {catalogState === "error" && <div className="board-notice board-notice--error" role="alert"><strong>Workflow catalog unavailable.</strong><span>Check daemon health and try again.</span></div>}
    <div className="workflow-layout">
      <aside className="workflow-catalog" aria-label="Installed workflow versions">
        <label className="workflow-search"><span className="sr-only">Search installed workflows</span><input value={query} onChange={(event) => setQuery(event.target.value)} placeholder="Filter workflows…" /></label>
        <div className="workflow-catalog__heading"><span>Installed versions</span><strong>{catalog.length}</strong></div>
        {catalogState === "loading" && <p className="workflow-catalog__empty" aria-live="polite">Loading installed workflows…</p>}
        {catalogState === "ready" && visibleCatalog.length === 0 && <p className="workflow-catalog__empty">{catalog.length ? "No versions match this filter." : "No workflows are installed. Install one with the CLI first."}</p>}
        <div className="workflow-version-list">{visibleCatalog.map((item) => <button type="button" key={workflowKey(item)} className="workflow-version" aria-pressed={workflowKey(item) === selectedKey} onClick={() => setSelectedKey(workflowKey(item))}><span className="workflow-version__mark" aria-hidden="true">◇</span><span><strong>{item.name}</strong><small>v{item.version} · {humanize(item.sourceScope)}</small></span></button>)}</div>
      </aside>

      <section className="workflow-workspace">
        {!selected ? <WorkflowEmpty /> : <>
          <header className="workflow-detail-header"><div><p className="eyebrow">Installed workflow</p><h2>{decoded?.displayName ?? selected.name}</h2><p>{selected.name} · v{selected.version}</p></div><span className={`scope-badge scope-badge--${selected.sourceScope}`}>{selected.sourceScope}</span></header>
          <section className="workflow-meta" aria-label="Selected workflow version"><SummaryFact label="Digest" value={shortIdentifier(selected.digest)} mono /><SummaryFact label="Source" value={selected.sourceReference} /><SummaryFact label="Installed" value={formatDate(selected.installedAt)} /><SummaryFact label="API version" value={decoded?.apiVersion ?? "Loading…"} /></section>
          <div className="workflow-tabs" role="tablist" aria-label="Workflow inspection">{workflowTabs.map((value, index) => <WorkflowTabButton key={value} id={value} current={tab} set={setTab} buttonRef={(element) => { tabRefs.current[index] = element; }} onKeyDown={(event) => onTabKeyDown(event, index)}>{value === "preview" ? "Route preview" : value === "readiness" ? "Authored readiness" : value[0].toUpperCase() + value.slice(1)}</WorkflowTabButton>)}</div>
          {detailError ? <div className="workflow-detail-error" role="alert">{detailError}</div> : !definition || !graph ? <div className="workflow-detail-loading" aria-busy="true">Loading immutable definition…</div> : <>
            <div id="workflow-panel-preview" role="tabpanel" tabIndex={tab === "preview" ? 0 : -1} aria-labelledby="workflow-tab-preview" hidden={tab !== "preview"}>{tab === "preview" && <PreviewTab selected={selected} graph={graph} decoded={decoded} />}</div>
            <div id="workflow-panel-graph" role="tabpanel" tabIndex={tab === "graph" ? 0 : -1} aria-labelledby="workflow-tab-graph" hidden={tab !== "graph"}>{tab === "graph" && <GraphTab graph={graph} decoded={decoded} />}</div>
            <div id="workflow-panel-readiness" role="tabpanel" tabIndex={tab === "readiness" ? 0 : -1} aria-labelledby="workflow-tab-readiness" hidden={tab !== "readiness"}>{tab === "readiness" && <ReadinessTab decoded={decoded} />}</div>
            <div id="workflow-panel-definition" role="tabpanel" tabIndex={tab === "definition" ? 0 : -1} aria-labelledby="workflow-tab-definition" hidden={tab !== "definition"}>{tab === "definition" && <DefinitionTab selected={selected} decoded={decoded} />}</div>
          </>}
        </>}
      </section>
    </div>
  </div>;
}

function WorkflowTabButton({ id, current, set, buttonRef, onKeyDown, children }: { id: WorkflowTab; current: WorkflowTab; set(value: WorkflowTab): void; buttonRef(value: HTMLButtonElement | null): void; onKeyDown(event: KeyboardEvent<HTMLButtonElement>): void; children: React.ReactNode }) {
  return <button ref={buttonRef} id={`workflow-tab-${id}`} role="tab" tabIndex={current === id ? 0 : -1} aria-selected={current === id} aria-controls={`workflow-panel-${id}`} type="button" onKeyDown={onKeyDown} onClick={() => set(id)}>{children}</button>;
}

function PreviewTab({ selected, graph, decoded }: { selected: Schemas["WorkflowVersionSummary"]; graph: Schemas["WorkflowGraph"]; decoded?: AuthoredWorkflow }) {
  const [from, setFrom] = useState("");
  const [terminals, setTerminals] = useState<string[]>([]);
  const [requiredNodes, setRequiredNodes] = useState<string[]>([]);
  const [runInputsText, setRunInputsText] = useState("{}");
  const [preview, setPreview] = useState<Schemas["WorkflowRoutePreview"]>();
  const [submitted, setSubmitted] = useState<Schemas["WorkflowPreviewRequest"]>();
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);

  const entryNodes = graph.nodes.filter((node) => node.entry);
  const terminalNodes = graph.nodes.filter((node) => node.terminal);
  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault(); setError("");
    let request: Schemas["WorkflowPreviewRequest"];
    try { request = buildWorkflowPreviewRequest({ from, until: terminals, requiredNodes, runInputsText }); }
    catch (cause) { setError(cause instanceof Error ? cause.message : "The preview request is invalid."); return; }
    setLoading(true);
    try {
      const result = await apiClient.previewWorkflowRoute(selected.name, selected.version, request);
      setSubmitted(request); setPreview(result);
    } catch (cause) {
      setSubmitted(request); setPreview(undefined); setError(previewError(cause));
    } finally { setLoading(false); }
  }

  return <section className="workflow-panel workflow-preview-panel">
    <div className="preview-callout"><strong>Preview only</strong><span>This validates a candidate route against the installed version. It does not create a run, record a choice, or modify workflow state.</span></div>
    <div className="preview-grid"><form className="route-preview-form" onSubmit={(event) => void submit(event)}>
      <div className="section-heading"><div><p className="eyebrow">Candidate request</p><h2>Route boundaries and context</h2></div></div>
      <label className="field"><span>Entry node</span><select value={from} onChange={(event) => setFrom(event.target.value)}><option value="">Workflow default{decoded?.routeDefaults?.entry ? ` · ${decoded.routeDefaults.entry}` : ""}</option>{entryNodes.map((node) => <option key={node.id} value={node.id}>{node.id}</option>)}</select></label>
      <ChoiceGrid label="Terminal boundary" help="Leave all clear to use the workflow default." values={terminalNodes.map((node) => node.id)} selected={terminals} onChange={setTerminals} />
      <ChoiceGrid label="Policy-required nodes" help="These nodes must remain inside the candidate route." values={graph.nodes.map((node) => node.id)} selected={requiredNodes} onChange={setRequiredNodes} />
      <label className="field"><span>Run inputs · JSON object</span><textarea rows={7} spellCheck={false} value={runInputsText} onChange={(event) => setRunInputsText(event.target.value)} aria-describedby="run-input-help" /><small id="run-input-help">Used for this preview request only; values are not persisted by the dashboard.</small></label>
      {error && <p className="form-error" role="alert">{error}</p>}
      <button className="button button--primary preview-submit" type="submit" disabled={loading}>{loading ? "Validating…" : "Preview route"}</button>
    </form><PreviewResult preview={preview} submitted={submitted} /></div>
    <div className="api-parity-note"><strong>Live decisions stay with the run.</strong><span>This remains a stateless preview. Open a run's Readiness view to review and record its latest durable assessment; route effects remain separate.</span></div>
  </section>;
}

function PreviewResult({ preview, submitted }: { preview?: Schemas["WorkflowRoutePreview"]; submitted?: Schemas["WorkflowPreviewRequest"] }) {
  if (!submitted) return <section className="route-preview-result route-preview-result--empty"><span aria-hidden="true">◇</span><h2>No preview yet</h2><p>Choose route boundaries and submit an explicit preview request.</p></section>;
  if (!preview) return <section className="route-preview-result route-preview-result--empty"><h2>Candidate not validated</h2><p>The submitted request is preserved below so it can be adjusted and retried.</p><RequestEvidence request={submitted} /></section>;
  const route = preview.route;
  const summary = previewImpact(route);
  return <section className="route-preview-result"><div className="section-heading"><div><p className="eyebrow">Validated candidate</p><h2>Frozen-route preview</h2></div><span className="preview-only-badge">Not persisted</span></div>
    <div className="preview-result-facts"><SummaryFact label="Entry" value={summary.entry} mono /><SummaryFact label="Terminals" value={summary.terminalNodeIds.join(", ") || "None"} mono /><SummaryFact label="Included" value={String(summary.includedNodeIds.length)} /><SummaryFact label="Excluded" value={String(summary.excludedNodes.length)} /></div>
    <RouteNodeList route={route} />
    <ResultCollection title="Enabled transitions" empty="No transitions are required for this candidate." items={route.transitions.map((transition) => <div key={transition.id}><code>{transition.id}</code><span>{transition.from} → {transition.to}</span></div>)} />
    <ResultCollection title="Excluded nodes" empty="No authored nodes are excluded." items={route.excludedNodes.map((node) => <div key={node.id}><code>{node.id}</code><span>{humanize(node.reason)}</span></div>)} />
    <ResultCollection title="Input requirements" empty="No unresolved input requirements were reported." items={route.inputRequirements.map((requirement) => <div key={`${requirement.node}:${requirement.input}`}><code>{requirement.node}.{requirement.input}</code><span>{requirement.code} · {requirement.source}</span></div>)} />
    <RequestEvidence request={submitted} />
  </section>;
}

function RouteNodeList({ route }: { route: Schemas["FrozenRoute"] }) {
  return <ol className="workflow-route-nodes" aria-label="Included route nodes">{route.nodes.map((node, index) => <li key={node.id}><span>{index + 1}</span><div><strong>{node.id}</strong><small>{node.id === route.entry ? "Entry" : route.terminals.includes(node.id) ? "Terminal" : "Included"}</small></div></li>)}</ol>;
}

function ResultCollection({ title, empty, items }: { title: string; empty: string; items: React.ReactNode[] }) {
  return <details className="preview-collection"><summary>{title}<span>{items.length}</span></summary>{items.length ? <div className="preview-collection__items">{items}</div> : <p>{empty}</p>}</details>;
}

function RequestEvidence({ request }: { request: Schemas["WorkflowPreviewRequest"] }) {
  return <details className="preview-request-evidence"><summary>Submitted preview request</summary><pre>{JSON.stringify(request, null, 2)}</pre></details>;
}

function ChoiceGrid({ label, help, values, selected, onChange }: { label: string; help: string; values: string[]; selected: string[]; onChange(values: string[]): void }) {
  const chosen = new Set(selected);
  return <fieldset className="workflow-choice-grid"><legend>{label}</legend><p>{help}</p><div>{values.map((value) => <label key={value}><input type="checkbox" checked={chosen.has(value)} onChange={(event) => onChange(event.target.checked ? [...selected, value] : selected.filter((item) => item !== value))} /><span>{value}</span></label>)}</div></fieldset>;
}

function GraphTab({ graph, decoded }: { graph: Schemas["WorkflowGraph"]; decoded?: AuthoredWorkflow }) {
  const authored = new Map(decoded?.nodes.map((node) => [node.id, node]));
  return <section className="workflow-panel"><div className="section-heading"><div><p className="eyebrow">Authored topology</p><h2>Nodes and transitions</h2></div><span className="section-count">{graph.nodes.length}</span></div><div className="workflow-graph-list">{graph.nodes.map((node) => <article key={node.id}><span className="workflow-node-type">{humanize(node.type)}</span><h3>{authored.get(node.id)?.displayName ?? node.id}</h3><code>{node.id}</code><footer>{node.entry && <span>Entry-capable</span>}{node.terminal && <span>Terminal-capable</span>}</footer></article>)}</div><ResultCollection title="Authored edges" empty="No edges are authored." items={graph.edges.map((edge) => <div key={edge.id}><code>{edge.id}</code><span>{edge.from} → {edge.to}</span></div>)} /></section>;
}

function ReadinessTab({ decoded }: { decoded?: AuthoredWorkflow }) {
  if (!decoded) return <DecodeNotice />;
  const nodes = decoded.nodes.filter((node) => node.readiness || node.requiredInputs.length);
  return <section className="workflow-panel"><div className="authored-contract-callout"><strong>Authored contracts · not live readiness</strong><span>These requirements describe the installed workflow. They do not indicate whether a specific run is ready, blocked, or permitted to continue.</span></div>{nodes.length === 0 ? <p className="workflow-detail-loading">This workflow does not declare readable readiness contracts.</p> : <div className="readiness-contracts">{nodes.map((node) => <ReadinessContract key={node.id} node={node} />)}</div>}</section>;
}

function ReadinessContract({ node }: { node: AuthoredWorkflow["nodes"][number] }) {
  const contract = node.readiness;
  return <details className="readiness-contract"><summary><span><strong>{node.displayName ?? node.id}</strong><code>{node.id}</code></span><span>{readinessAdvice(node).length} authored items</span></summary><div className="readiness-contract__body"><ContractList title="Required inputs" values={node.requiredInputs.map((value) => <code key={value}>{value}</code>)} /><ContractList title="Recommended evidence" values={(contract?.recommendedEvidence ?? []).map((item) => <p key={item.role}><strong>{item.role}</strong><span>{item.description}</span></p>)} /><ContractList title="Policy gates" values={(contract?.policyGates ?? []).map((item) => <p key={item.policy}><strong>{item.policy} · {item.enforcement}</strong><span>{item.description}</span></p>)} /><ContractList title="Invariants" values={(contract?.invariants ?? []).map((item) => <p key={item}>{item}</p>)} /><ContractList title="Declared remedies" values={(contract?.remedies ?? []).map((item) => <p key={item.code}><strong>{item.code} · {humanize(item.action)}</strong><span>{item.description} Target: {item.target}.</span></p>)} /></div></details>;
}

function ContractList({ title, values }: { title: string; values: React.ReactNode[] }) { return <section><h3>{title}</h3>{values.length ? <div>{values}</div> : <small>None declared</small>}</section>; }

function DefinitionTab({ selected, decoded }: { selected: Schemas["WorkflowVersionSummary"]; decoded?: AuthoredWorkflow }) {
  if (!decoded) return <DecodeNotice />;
  return <section className="workflow-panel definition-summary"><div className="section-heading"><div><p className="eyebrow">Immutable definition</p><h2>{decoded.displayName ?? selected.name}</h2></div></div><dl><div><dt>Identity</dt><dd>{selected.name} · {selected.version}</dd></div><div><dt>Source</dt><dd>{selected.sourceScope} · {selected.sourceReference}</dd></div><div><dt>Default entry</dt><dd><code>{decoded.routeDefaults?.entry ?? "Not decoded"}</code></dd></div><div><dt>Default terminals</dt><dd>{decoded.routeDefaults?.terminals.map((item) => <code key={item}>{item}</code>) ?? "Not decoded"}</dd></div><div><dt>Nodes</dt><dd>{decoded.nodes.length}</dd></div></dl><h3>Route profiles</h3>{decoded.profiles.length ? <div className="profile-list">{decoded.profiles.map((profile) => <article key={profile.id}><strong>{profile.id}</strong><span>{profile.description ?? "No description"}</span><code>{profile.entry} → {profile.terminals.join(", ")}</code></article>)}</div> : <p className="workflow-detail-loading">No route profiles are declared.</p>}</section>;
}

function DecodeNotice() { return <div className="workflow-detail-error" role="status">This installed definition uses fields the dashboard cannot safely decode. Graph and API-backed route preview remain available.</div>; }
function WorkflowEmpty() { return <div className="workflow-empty"><span aria-hidden="true">◇</span><h2>Select an installed workflow</h2><p>Choose one immutable version to inspect its graph, contracts, and preview routes.</p></div>; }
function workflowKey(value: Schemas["WorkflowVersionSummary"]) { return `${value.name}\u0000${value.version}\u0000${value.digest}`; }
function previewError(cause: unknown) { if (cause instanceof ApiRequestError && cause.status === 422) return "The daemon rejected this candidate route. Review its boundaries, required nodes, and run inputs."; if (cause instanceof ApiRequestError && cause.status === 404) return "This workflow version is no longer installed."; return "The route preview could not be completed. Check daemon health and try again."; }
