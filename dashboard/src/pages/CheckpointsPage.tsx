import { useCallback, useEffect, useMemo, useRef, useState, type FormEvent } from "react";

import { ApiRequestError, apiClient } from "../api/client";
import type { components } from "../api/schema.generated";
import { AppLink, useRouter } from "../app/router";
import { AsyncPanel, DiagnosticsDetails, EmptyState } from "../components/InteractionPatterns";
import { PageHeader } from "../components/PageStructure";
import { useDashboardState } from "../state/DashboardStateProvider";
import { DetailFailure, DetailLoading, formatDate, StatusPill, SummaryFact } from "./WorkDetailPage";
import { attentionFilterForKind, attentionFilters, attentionKindForFilter, attentionWorkspacePresentation, type AttentionFilter } from "./checkpointModel";
import { humanize } from "./runDetailModel";

type Schemas = components["schemas"];
type AttentionItem = Schemas["AttentionCheckpointV2"];
type AttentionKind = Schemas["AttentionKind"];

const attentionKinds: readonly AttentionKind[] = ["workflow_checkpoint", "input_required", "provider_permission", "workflow_control", "external_delivery"];

export function CheckpointsPage() {
  const { state } = useDashboardState();
  const { search, navigate } = useRouter();
  const params = useMemo(() => new URLSearchParams(search), [search]);
  const kind = parseKind(params.get("kind"));
  const filter = attentionFilterForKind(kind);
  const runId = params.get("runId")?.trim() ?? "";
  const cursor = params.get("cursor")?.trim() ?? "";
  const deepItem = params.get("itemId")?.trim() ?? "";
  const [draftRun, setDraftRun] = useState(runId);
  const [page, setPage] = useState<Schemas["AttentionPageV2"]>();
  const [selectedId, setSelectedId] = useState(deepItem);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const signature = useRef("");

  const load = useCallback(async (signal?: AbortSignal) => {
    try {
      const query = { ...(kind ? { kind } : {}), ...(runId ? { runId } : {}), ...(cursor ? { cursor } : {}), limit: 50 };
      const [listed, exact] = await Promise.all([
        apiClient.listAttention(query, signal),
        deepItem ? apiClient.listAttention({ ...(kind ? { kind } : {}), ...(runId ? { runId } : {}), itemId: deepItem, limit: 1 }, signal) : Promise.resolve(undefined),
      ]);
      const exactItem = exact?.items[0];
      const next = exactItem && !listed.items.some((item) => item.id === exactItem.id) ? { ...listed, items: [exactItem, ...listed.items] } : listed;
      const nextSignature = next.items.map((item) => `${item.kind}:${item.id}:${item.resourceVersion}`).join("|");
      if (signature.current && signature.current !== nextSignature) setNotice("Checkpoints refreshed from authoritative events.");
      signature.current = nextSignature;
      setPage(next);
      setSelectedId((current) => deepItem ? (exactItem?.id ?? "") : next.items.some((item) => item.id === current) ? current : next.items[0]?.id ?? "");
      setError("");
    } catch (cause) {
      if (!signal?.aborted) setError(attentionError(cause));
    }
  }, [cursor, deepItem, kind, runId]);

  useEffect(() => { const abort = new AbortController(); void load(abort.signal); return () => abort.abort(); }, [load, state.cursor]);
  useEffect(() => setDraftRun(runId), [runId]);

  function setFilters(event: FormEvent) {
    event.preventDefault();
    const next = new URLSearchParams();
    if (kind) next.set("kind", kind);
    if (draftRun.trim()) next.set("runId", draftRun.trim());
    navigate(`/checkpoints${next.size ? `?${next}` : ""}`);
  }

  function setKind(nextKind: string) {
    const next = new URLSearchParams(params);
    nextKind ? next.set("kind", nextKind) : next.delete("kind");
    next.delete("cursor"); next.delete("itemId");
    navigate(`/checkpoints${next.size ? `?${next}` : ""}`);
  }

  function setAttentionFilter(nextFilter: AttentionFilter) {
    const next = new URLSearchParams(params);
    const nextKind = attentionKindForFilter(nextFilter);
    nextKind ? next.set("kind", nextKind) : next.delete("kind");
    next.delete("cursor"); next.delete("itemId");
    navigate(`/checkpoints${next.size ? `?${next}` : ""}`);
  }

  function choose(item: AttentionItem) {
    setSelectedId(item.id);
    const next = new URLSearchParams(params); next.set("itemId", item.id);
    navigate(`/checkpoints?${next}`);
  }

  function nextPage() {
    if (!page?.nextCursor) return;
    const next = new URLSearchParams(params); next.set("cursor", page.nextCursor); next.delete("itemId");
    navigate(`/checkpoints?${next}`);
  }

  const selected = page?.items.find((item) => item.id === selectedId) ?? (!deepItem ? page?.items[0] : undefined);
  return <div className="page checkpoints-page">
    <PageHeader className="checkpoints-header" eyebrow="Derived operator projection" title="Checkpoints" description="One server-authored queue for workflow decisions, required input, provider authority, controls, and delivery." breadcrumbs={[{ label: "Board", to: "/board" }, { label: "Checkpoints" }]} readOnly="Authoritative sources" />
    <nav className="checkpoint-attention-tabs" aria-label="Attention type">{attentionFilters.map((value) => <button type="button" key={value} aria-current={filter === value ? "page" : undefined} onClick={() => setAttentionFilter(value)}>{humanize(value)}</button>)}</nav>
    <form className="checkpoint-filters" onSubmit={setFilters}>
      <label className="checkpoint-kind-select"><span>Kind</span><select value={kind ?? ""} onChange={(event) => setKind(event.target.value)}><option value="">All unresolved</option>{attentionKinds.map((value) => <option key={value} value={value}>{humanize(value)}</option>)}</select></label>
      <label><span>Run ID</span><input value={draftRun} placeholder="All runs" onChange={(event) => setDraftRun(event.target.value)} /></label>
      <button className="button" type="submit">Apply run filter</button>
      <strong>{page?.totalCount ?? 0} unresolved</strong>
    </form>
    {notice && <AsyncPanel compact state="success" title="Queue updated" message={notice} />}
    {error && page && <AsyncPanel compact state="error" title="Refresh failed" message={error} />}
    {!page && error ? <DetailFailure title="Checkpoints unavailable" message={error} /> : !page ? <DetailLoading label="Loading Checkpoints" /> : <section className="checkpoint-workspace" aria-label="Unified Checkpoints queue">
      <div className="checkpoint-layout">
        <aside className="checkpoint-list" aria-label="Unresolved operator attention">
          {page.items.length ? <ol>{page.items.map((item) => { const presentation = attentionPresentation(item); return <li key={`${item.kind}:${item.id}`}><button type="button" aria-current={selected?.id === item.id ? "true" : undefined} onClick={() => choose(item)}><span className="timeline-marker timeline-marker--waiting" aria-hidden="true" /><span><strong>{presentation.label}</strong><small>{item.context.workTitle} · urgency {item.urgency}</small><small>Updated {formatDate(item.updatedAt)}</small></span><StatusPill status="pending" /></button></li>; })}</ol> : <EmptyState kind="filtered" title="No unresolved attention" message="No authoritative source matches these filters." compact />}
          {page.nextCursor && <button className="button checkpoint-next" type="button" onClick={nextPage}>Next page</button>}
        </aside>
        <section className="checkpoint-detail">{selected ? <AttentionDetail item={selected} refresh={() => load()} /> : deepItem ? <EmptyState kind="unavailable" title="Requested checkpoint is no longer unresolved" message="This exact attention item was resolved, removed, or does not match the current filters. No other queue item has been selected in its place." compact /> : <EmptyState kind="empty" title="Choose a checkpoint" message="Select an unresolved item to inspect its exact subject and legal actions." compact />}</section>
      </div>
    </section>}
  </div>;
}

function AttentionDetail({ item, refresh }: { item: AttentionItem; refresh(): Promise<void> }) {
  const presentation = attentionPresentation(item);
  const workspace = attentionWorkspacePresentation(item.kind);
  return <div className={`checkpoint-document-workspace checkpoint-document-workspace--${item.kind}`}>
    <nav className="checkpoint-contents" aria-label="Checkpoint contents"><p className="eyebrow">Contents</p><a href="#checkpoint-document">{workspace.documentLabel}</a><a href="#checkpoint-binding">Exact binding</a><a href="#checkpoint-annotations">{workspace.annotationLabel}</a></nav>
    <section className="checkpoint-document" id="checkpoint-document" tabIndex={-1} aria-label={workspace.documentLabel}>
      <header className="checkpoint-detail-header"><div><p className="eyebrow">{presentation.label}</p><h2>{item.summary}</h2><p>{presentation.description}</p></div><StatusPill status="pending" /></header>
      <section className="detail-summary"><SummaryFact label="Project" value={item.context.projectName} /><SummaryFact label="Work" value={item.context.workTitle} /><SummaryFact label="Urgency" value={String(item.urgency)} /><SummaryFact label="Recent activity" value={formatDate(item.updatedAt)} /></section>
      <AttentionDocument item={item} />
      <DiagnosticsDetails label="Exact binding and provenance"><dl id="checkpoint-binding">{[["Attention", item.id], ["Run", item.context.runId], ["Resource version", String(item.resourceVersion)], ...subjectFacts(item)].map(([label, value]) => <div key={`${label}:${value}`}><dt>{label}</dt><dd title={value}>{value}</dd></div>)}</dl></DiagnosticsDetails>
    </section>
    <aside className="checkpoint-annotations" id="checkpoint-annotations" aria-label={workspace.annotationLabel}>
      <header><p className="eyebrow">{workspace.annotationLabel}</p><h2>{workspace.label} action</h2><p>{workspace.description}</p></header>
      {item.kind === "workflow_checkpoint" && <section className="checkpoint-annotation-entry"><p>Select text and create anchored comments in the exact candidate workspace. General instructions and all range comments are submitted together.</p><AppLink className="navigation-action" to={`/checkpoints/${encodeURIComponent(item.id)}/review?view=current`}>Open review workspace</AppLink><AppLink className="navigation-action" to={`/artifacts?targetKind=checkpoint&targetId=${encodeURIComponent(item.subject.checkpointId)}&ingest=1`}>Add evidence</AppLink></section>}
      <section className="checkpoint-decision-bar" aria-label={`Available ${workspace.label.toLowerCase()} actions`}><p className="eyebrow">Available now</p><AttentionActions item={item} refresh={refresh} /></section>
    </aside>
  </div>;
}

function AttentionDocument({ item }: { item: AttentionItem }) {
  switch (item.kind) {
    case "workflow_checkpoint": return <section className="checkpoint-document-card"><p className="eyebrow">Candidate document</p><h3>Candidate version {item.subject.candidateVersion}</h3><p>This approval is bound to the immutable candidate digest below. Open the document review to read, compare, and annotate the safe representation.</p><AppLink className="navigation-action" to={`/checkpoints/${encodeURIComponent(item.id)}/review?view=current`}>Read exact candidate</AppLink></section>;
    case "input_required": return isPreparationAttention(item) ? <section className="checkpoint-document-card"><p className="eyebrow">Route preparation</p><h3>Missing assessment evidence</h3><p>The route assessor needs these answers before it can select the smallest safe workflow.</p><ol>{item.subject.questions.map((question) => <li key={question.id}>{question.prompt}</li>)}</ol></section> : <section className="checkpoint-document-card"><p className="eyebrow">Recorded questions</p><h3>Provider input request</h3><pre>{JSON.stringify(item.subject.request, null, 2)}</pre></section>;
    case "provider_permission": return <section className="checkpoint-document-card"><p className="eyebrow">Authority request</p><h3>{humanize(item.subject.interactionKind)} interaction</h3><p>{item.subject.evidence.summary}</p><dl><div><dt>Target</dt><dd>{humanize(item.subject.scope.target)}</dd></div><div><dt>Operation</dt><dd>{humanize(item.subject.scope.operation)}</dd></div><div><dt>Subject</dt><dd>{item.subject.scope.subject}</dd></div></dl><p>Full provider identifiers and binding digests remain in Exact binding and provenance.</p></section>;
    case "workflow_control": return <section className="checkpoint-document-card"><p className="eyebrow">Control proposal</p><h3>Workflow operation</h3><p>The runtime has paused for an explicit control decision. Its exact scope and policy digests are preserved below.</p></section>;
    case "external_delivery": return <section className="checkpoint-document-card"><p className="eyebrow">Delivery proposal</p><h3>External boundary</h3><p>The proposed delivery crosses an external boundary. Review the bound attempt, scope, and policy evidence before deciding.</p></section>;
    default: return assertNever(item);
  }
}

function AttentionActions({ item, refresh }: { item: AttentionItem; refresh(): Promise<void> }) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  async function perform(action: string) {
    setBusy(true); setError("");
    try {
      switch (item.kind) {
        case "workflow_checkpoint": {
          if (!item.allowedActions.includes(action as never)) throw new Error("Action is no longer available.");
          const comment = action === "approve" ? window.prompt("Optional approval comment", "") ?? "" : window.prompt("Explain this decision", "") ?? "";
          if (action !== "approve" && !comment.trim()) throw new Error("This decision requires an explanation.");
          await apiClient.decideApproval(item.id, item.resourceVersion, `dashboard-checkpoint-${crypto.randomUUID()}`, { action: action as "approve" | "request_changes" | "reject", scopeDigest: item.subject.scopeDigest, policyDigest: item.subject.policyDigest, ...(comment.trim() ? { comment: comment.trim() } : {}) } as Schemas["ArtifactCheckpointDecisionRequest"]);
          break;
        }
        case "input_required": {
          if (isPreparationAttention(item)) throw new Error("Use the route preparation answer form.");
          if (!item.allowedActions.includes(action as never)) throw new Error("Action is no longer available.");
          if (action === "retry_delivery") await apiClient.retryInputDelivery(item.id, item.resourceVersion);
          else {
            const raw = window.prompt("Enter the complete JSON answer", "{\"answers\":{}}") ?? "";
            if (!raw) throw new Error("An answer is required.");
            await apiClient.answerInputRequest(item.id, item.resourceVersion, `dashboard-input-${crypto.randomUUID()}`, { scopeDigest: item.subject.scopeDigest, answer: JSON.parse(raw) });
          }
          break;
        }
        case "provider_permission": {
          if (!item.allowedActions.includes(action as never)) throw new Error("Action is no longer available.");
          if (action === "retry_delivery") await apiClient.retryProviderPermissionDelivery(item.id, item.resourceVersion);
          else await apiClient.decideProviderPermission(item.id, item.resourceVersion, `dashboard-provider-permission-${crypto.randomUUID()}`, { decision: action as "allow_once" | "deny" | "cancel", scopeDigest: item.subject.scopeDigest });
          break;
        }
        case "workflow_control":
        case "external_delivery":
          throw new Error("Use the exact authority form.");
        default:
          assertNever(item);
      }
      await refresh();
    } catch (cause) {
      setError(cause instanceof SyntaxError ? "The answer must be valid JSON." : cause instanceof Error ? cause.message : "The action could not be recorded.");
    } finally { setBusy(false); }
  }
  if (isPreparationAttention(item)) return <PreparationQuestions key={`${item.id}:${item.subject.assessmentDigest}`} item={item} refresh={refresh} />;
  if (item.kind === "workflow_control" || item.kind === "external_delivery") return <AuthorityDecisionActions item={item} refresh={refresh} />;
  return <><div>{item.allowedActions.map((action) => <button className="readiness-action" type="button" disabled={busy} key={action} onClick={() => void perform(action)}><strong>{humanize(action)}</strong><span>Valid only for this {humanize(item.kind)} source.</span></button>)}</div>{error && <p className="form-error" role="alert">{error}</p>}</>;
}

function AuthorityDecisionActions({ item, refresh }: { item: Schemas["WorkflowControlAttention"] | Schemas["ExternalDeliveryAttention"]; refresh(): Promise<void> }) {
  const [comment, setComment] = useState("");
  const [busy, setBusy] = useState<string>();
  const [error, setError] = useState("");
  async function decide(action: "approve" | "deny" | "cancel") {
    if (!item.allowedActions.includes(action)) { setError("Action is no longer available."); return; }
    setBusy(action); setError("");
    try {
      await apiClient.decideAttention(item.kind, item.id, item.resourceVersion, `dashboard-attention-${crypto.randomUUID()}`, { action, scopeDigest: item.subject.scopeDigest, policyDigest: item.subject.policyDigest, ...(comment.trim() ? { comment: comment.trim() } : {}) });
      await refresh();
    } catch (cause) {
      setError(cause instanceof ApiRequestError && cause.status === 409 ? "This decision binding changed. Your note is preserved; refresh before deciding." : cause instanceof Error ? cause.message : "The authority decision could not be recorded.");
    } finally { setBusy(undefined); }
  }
  return <form className="inspector-form" onSubmit={(event) => event.preventDefault()} aria-label={`Exact ${humanize(item.kind)} decision`}>
    <label>Decision note<textarea value={comment} maxLength={4096} onChange={(event) => setComment(event.target.value)} disabled={Boolean(busy)} placeholder="Optional context for the durable audit record" /></label>
    <div>{item.allowedActions.map((action) => <button className={action === "approve" ? "button button--primary" : action === "deny" ? "button button--danger" : "button"} type="button" disabled={Boolean(busy)} key={action} onClick={() => void decide(action)}>{busy === action ? "Recording…" : humanize(action)}</button>)}</div>
    <small>Bound to resource {item.resourceVersion}, the recorded scope, and the recorded policy. This note remains if the binding becomes stale.</small>
    {error && <p className="form-error" role="alert">{error}</p>}
  </form>;
}

function isPreparationAttention(item: AttentionItem): item is Schemas["PreparationInputRequiredAttention"] {
  return item.kind === "input_required" && "source" in item.subject && item.subject.source === "route_preparation";
}

function PreparationQuestions({ item, refresh }: { item: Schemas["PreparationInputRequiredAttention"]; refresh(): Promise<void> }) {
  const [answers, setAnswers] = useState<Record<string, string>>({});
  const [inputText, setInputText] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const { navigate } = useRouter();
  async function submit(event: FormEvent) {
    event.preventDefault(); setBusy(true); setError("");
    try {
      const current = await apiClient.getRun(item.context.runId);
      if (current.run.resourceVersion !== item.resourceVersion || current.assessment?.digest !== item.subject.assessmentDigest) throw new Error("These route questions have changed. Refresh Checkpoints before submitting; your answers are kept here.");
      let runInputs: Record<string, unknown> | undefined;
      if (inputText.trim()) {
        const parsed: unknown = JSON.parse(inputText);
        if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) throw new Error("Workflow inputs must be a JSON object of input names and values.");
        runInputs = parsed as Record<string, unknown>;
      }
      const prepared = await apiClient.prepareRun({ workItemId: item.context.workItemId, preparation: { answers, ...(runInputs ? { runInputs } : {}) } }, `dashboard-route-answers-${crypto.randomUUID()}`);
      await refresh();
      navigate(`/work/${encodeURIComponent(item.context.workItemId)}/run/${encodeURIComponent(prepared.id)}`);
    } catch (cause) {
      setError(cause instanceof SyntaxError ? "Workflow inputs must be valid JSON. Your answers have been kept." : cause instanceof Error ? cause.message : "The route could not be assessed. Your answers have been kept.");
    } finally { setBusy(false); }
  }
  return <form className="inspector-form" onSubmit={(event) => void submit(event)} aria-label="Route preparation questions">
    {item.subject.questions.map((question) => <label key={question.id}>{question.prompt}<textarea required value={answers[question.id] ?? ""} onChange={(event) => setAnswers((previous) => ({ ...previous, [question.id]: event.target.value }))} disabled={busy} /></label>)}
    <details><summary>Additional workflow inputs</summary><label>Input names and values (JSON)<textarea value={inputText} onChange={(event) => setInputText(event.target.value)} disabled={busy} placeholder={'{"input_name": "value"}'} /></label></details>
    <button className="button" type="submit" disabled={busy}>{busy ? "Assessing route…" : "Assess with these answers"}</button>
    <AppLink className="navigation-action" to={`/work/${encodeURIComponent(item.context.workItemId)}`}>Open work context</AppLink>
    {error && <p className="form-error" role="alert">{error}</p>}
  </form>;
}

function attentionPresentation(item: AttentionItem): { label: string; description: string } {
  switch (item.kind) {
    case "workflow_checkpoint": return { label: "Workflow checkpoint", description: "Review one immutable workflow candidate." };
    case "input_required": return isPreparationAttention(item) ? { label: "Route input required", description: "Supply the missing details to assess a safe route. Assessment does not start execution." } : { label: "Input required", description: "Answer a provider question without granting authority." };
    case "provider_permission": return { label: "Provider permission", description: "Authorize only the recorded provider interaction scope." };
    case "workflow_control": return { label: "Workflow control", description: "Decide one exact proposed workflow control operation." };
    case "external_delivery": return { label: "External delivery", description: "Authorize one exact external delivery operation." };
    default: return assertNever(item);
  }
}

function subjectFacts(item: AttentionItem): Array<[string, string]> {
  switch (item.kind) {
    case "workflow_checkpoint": return [["Checkpoint", item.subject.checkpointId], ["Node", item.subject.nodeId], ["Attempt", item.subject.attemptId], ["Candidate", `${item.subject.candidateArtifactId} · v${item.subject.candidateVersion}`], ["Scope digest", item.subject.scopeDigest], ["Policy digest", item.subject.policyDigest]];
    case "input_required": return isPreparationAttention(item) ? [["Assessment digest", item.subject.assessmentDigest]] : [["Node", item.subject.nodeId], ["Attempt", item.subject.attemptId], ["Provider request", item.subject.providerRequestId], ["Delivery state", item.subject.status], ["Scope digest", item.subject.scopeDigest]];
    case "provider_permission": return [["Attempt", item.subject.attemptId], ["Node", item.subject.nodeId], ["Interaction", humanize(item.subject.interactionKind)], ["Provider request", item.subject.providerRequestId], ["Delivery state", item.subject.status], ["Scope digest", item.subject.scopeDigest], ["Policy digest", item.subject.policyDigest]];
    case "workflow_control": return [["Node", item.subject.nodeId ?? "Run scope"], ["Scope digest", item.subject.scopeDigest], ["Policy digest", item.subject.policyDigest]];
    case "external_delivery": return [["Attempt", item.subject.attemptId ?? "Run scope"], ["Scope digest", item.subject.scopeDigest], ["Policy digest", item.subject.policyDigest]];
    default: return assertNever(item);
  }
}

function parseKind(value: string | null): AttentionKind | undefined { return attentionKinds.includes(value as AttentionKind) ? value as AttentionKind : undefined; }
function assertNever(value: never): never { throw new Error(`Unknown attention variant: ${JSON.stringify(value)}`); }
function attentionError(cause: unknown) { if (cause instanceof ApiRequestError && cause.status === 400) return "The Checkpoints filter or cursor is no longer valid."; return "The Checkpoints projection could not be loaded. Check daemon health and try again."; }
