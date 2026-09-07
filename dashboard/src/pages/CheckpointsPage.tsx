import { useCallback, useEffect, useMemo, useRef, useState, type FormEvent } from "react";

import { ApiRequestError, apiClient } from "../api/client";
import type { components } from "../api/schema.generated";
import { AppLink, useRouter } from "../app/router";
import { AsyncPanel, EmptyState } from "../components/InteractionPatterns";
import { PageHeader } from "../components/PageStructure";
import { useDashboardState } from "../state/DashboardStateProvider";
import { DetailFailure, DetailLoading, formatDate, StatusPill, SummaryFact } from "./WorkDetailPage";
import { humanize, shortIdentifier } from "./runDetailModel";

type Schemas = components["schemas"];
type AttentionItem = Schemas["AttentionCheckpoint"];
type AttentionKind = Schemas["AttentionKind"];

const attentionKinds: readonly AttentionKind[] = ["workflow_checkpoint", "input_required", "provider_permission", "workflow_control", "external_delivery"];

export function CheckpointsPage() {
  const { state } = useDashboardState();
  const { search, navigate } = useRouter();
  const params = useMemo(() => new URLSearchParams(search), [search]);
  const kind = parseKind(params.get("kind"));
  const runId = params.get("runId")?.trim() ?? "";
  const cursor = params.get("cursor")?.trim() ?? "";
  const deepItem = params.get("itemId")?.trim() ?? "";
  const [draftRun, setDraftRun] = useState(runId);
  const [page, setPage] = useState<Schemas["AttentionPage"]>();
  const [selectedId, setSelectedId] = useState(deepItem);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const signature = useRef("");

  const load = useCallback(async (signal?: AbortSignal) => {
    try {
      const next = await apiClient.listAttention({ ...(kind ? { kind } : {}), ...(runId ? { runId } : {}), ...(cursor ? { cursor } : {}), limit: 50 }, signal);
      const nextSignature = next.items.map((item) => `${item.kind}:${item.id}:${item.resourceVersion}`).join("|");
      if (signature.current && signature.current !== nextSignature) setNotice("Checkpoints refreshed from authoritative events.");
      signature.current = nextSignature;
      setPage(next);
      setSelectedId((current) => next.items.some((item) => item.id === current) ? current : next.items[0]?.id ?? "");
      setError("");
    } catch (cause) {
      if (!signal?.aborted) setError(attentionError(cause));
    }
  }, [cursor, kind, runId]);

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

  const selected = page?.items.find((item) => item.id === selectedId) ?? page?.items[0];
  return <div className="page checkpoints-page">
    <PageHeader className="checkpoints-header" eyebrow="Derived operator projection" title="Checkpoints" description="One server-authored queue for workflow decisions, required input, provider authority, controls, and delivery." breadcrumbs={[{ label: "Board", to: "/board" }, { label: "Checkpoints" }]} readOnly="Authoritative sources" />
    <form className="checkpoint-filters" onSubmit={setFilters}>
      <label><span>Kind</span><select value={kind ?? ""} onChange={(event) => setKind(event.target.value)}><option value="">All unresolved</option>{attentionKinds.map((value) => <option key={value} value={value}>{humanize(value)}</option>)}</select></label>
      <label><span>Run ID</span><input value={draftRun} placeholder="All runs" onChange={(event) => setDraftRun(event.target.value)} /></label>
      <button className="button" type="submit">Apply run filter</button>
      <strong>{page?.items.length ?? 0} items</strong>
    </form>
    {notice && <AsyncPanel compact state="success" title="Queue updated" message={notice} />}
    {error && page && <AsyncPanel compact state="error" title="Refresh failed" message={error} />}
    {!page && error ? <DetailFailure title="Checkpoints unavailable" message={error} /> : !page ? <DetailLoading label="Loading Checkpoints" /> : <section className="checkpoint-workspace" aria-label="Unified Checkpoints queue">
      <div className="checkpoint-layout">
        <aside className="checkpoint-list" aria-label="Unresolved operator attention">
          {page.items.length ? <ol>{page.items.map((item) => { const presentation = attentionPresentation(item); return <li key={`${item.kind}:${item.id}`}><button type="button" aria-current={selected?.id === item.id ? "true" : undefined} onClick={() => choose(item)}><span className="timeline-marker timeline-marker--waiting" aria-hidden="true" /><span><strong>{presentation.label}</strong><code>{shortIdentifier(item.id)}</code><small>{item.context.workTitle} · priority {item.urgency}</small></span><StatusPill status="pending" /></button></li>; })}</ol> : <EmptyState kind="filtered" title="No unresolved attention" message="No authoritative source matches these filters." compact />}
          {page.nextCursor && <button className="button checkpoint-next" type="button" onClick={nextPage}>Next page</button>}
        </aside>
        <section className="checkpoint-detail">{selected ? <AttentionDetail item={selected} refresh={() => load()} /> : <EmptyState kind="empty" title="Choose a checkpoint" message="Select an unresolved item to inspect its exact subject and legal actions." compact />}</section>
      </div>
    </section>}
  </div>;
}

function AttentionDetail({ item, refresh }: { item: AttentionItem; refresh(): Promise<void> }) {
  const presentation = attentionPresentation(item);
  return <>
    <header className="checkpoint-detail-header"><div><p className="eyebrow">{presentation.label} · {shortIdentifier(item.id)}</p><h2>{item.summary}</h2><p>{presentation.description}</p></div><StatusPill status="pending" /></header>
    <section className="detail-summary"><SummaryFact label="Project" value={item.context.projectName} /><SummaryFact label="Work" value={item.context.workTitle} /><SummaryFact label="Run" value={shortIdentifier(item.context.runId)} mono /><SummaryFact label="Created" value={formatDate(item.createdAt)} /></section>
    <section className="detail-section"><div className="section-heading"><div><p className="eyebrow">Class-specific subject</p><h2>Authority boundary</h2></div></div><dl>{subjectFacts(item).map(([label, value]) => <div key={label}><dt>{label}</dt><dd title={value}>{value}</dd></div>)}</dl></section>
    <section className="detail-section checkpoint-actions"><div className="section-heading"><div><p className="eyebrow">Server-derived legal actions</p><h2>Available now</h2></div></div><AttentionActions item={item} refresh={refresh} />{item.kind === "workflow_checkpoint" && <div className="candidate-actions"><AppLink className="navigation-action" to={`/checkpoints/${encodeURIComponent(item.id)}/review?view=current`}>Open review workspace</AppLink><AppLink className="navigation-action" to={`/artifacts?targetKind=checkpoint&targetId=${encodeURIComponent(item.subject.checkpointId)}&ingest=1`}>Add evidence</AppLink></div>}</section>
  </>;
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
          throw new Error("This authority action is not yet exposed by the public command API.");
        default:
          assertNever(item);
      }
      await refresh();
    } catch (cause) {
      setError(cause instanceof SyntaxError ? "The answer must be valid JSON." : cause instanceof Error ? cause.message : "The action could not be recorded.");
    } finally { setBusy(false); }
  }
  const commandUnavailable = item.kind === "workflow_control" || item.kind === "external_delivery";
  return <><div>{item.allowedActions.map((action) => <button className="readiness-action" type="button" disabled={busy || commandUnavailable} key={action} onClick={() => void perform(action)}><strong>{humanize(action)}</strong><span>Valid only for this {humanize(item.kind)} source at resource version {item.resourceVersion}.</span></button>)}</div>{error && <p className="form-error" role="alert">{error}</p>}</>;
}

function attentionPresentation(item: AttentionItem): { label: string; description: string } {
  switch (item.kind) {
    case "workflow_checkpoint": return { label: "Workflow checkpoint", description: "Review one immutable workflow candidate." };
    case "input_required": return { label: "Input required", description: "Answer a provider question without granting authority." };
    case "provider_permission": return { label: "Provider permission", description: "Authorize only the recorded provider interaction scope." };
    case "workflow_control": return { label: "Workflow control", description: "Decide one exact proposed workflow control operation." };
    case "external_delivery": return { label: "External delivery", description: "Authorize one exact external delivery operation." };
    default: return assertNever(item);
  }
}

function subjectFacts(item: AttentionItem): Array<[string, string]> {
  switch (item.kind) {
    case "workflow_checkpoint": return [["Checkpoint", item.subject.checkpointId], ["Node", item.subject.nodeId], ["Attempt", item.subject.attemptId], ["Candidate", `${item.subject.candidateArtifactId} · v${item.subject.candidateVersion}`], ["Scope digest", item.subject.scopeDigest], ["Policy digest", item.subject.policyDigest]];
    case "input_required": return [["Node", item.subject.nodeId], ["Attempt", item.subject.attemptId], ["Provider request", item.subject.providerRequestId], ["Delivery state", item.subject.status], ["Scope digest", item.subject.scopeDigest]];
    case "provider_permission": return [["Attempt", item.subject.attemptId], ["Node", item.subject.nodeId], ["Interaction", humanize(item.subject.interactionKind)], ["Provider request", item.subject.providerRequestId], ["Delivery state", item.subject.status], ["Scope digest", item.subject.scopeDigest], ["Policy digest", item.subject.policyDigest]];
    case "workflow_control": return [["Node", item.subject.nodeId ?? "Run scope"], ["Scope digest", item.subject.scopeDigest], ["Policy digest", item.subject.policyDigest]];
    case "external_delivery": return [["Attempt", item.subject.attemptId ?? "Run scope"], ["Scope digest", item.subject.scopeDigest], ["Policy digest", item.subject.policyDigest]];
    default: return assertNever(item);
  }
}

function parseKind(value: string | null): AttentionKind | undefined { return attentionKinds.includes(value as AttentionKind) ? value as AttentionKind : undefined; }
function assertNever(value: never): never { throw new Error(`Unknown attention variant: ${JSON.stringify(value)}`); }
function attentionError(cause: unknown) { if (cause instanceof ApiRequestError && cause.status === 400) return "The Checkpoints filter or cursor is no longer valid."; return "The Checkpoints projection could not be loaded. Check daemon health and try again."; }
