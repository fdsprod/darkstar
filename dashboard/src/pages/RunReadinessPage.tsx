import { useCallback, useEffect, useMemo, useRef, useState, type FormEvent } from "react";

import { ApiRequestError, apiClient } from "../api/client";
import type { components } from "../api/schema.generated";
import { AppLink, useRouter } from "../app/router";
import { useDashboardState } from "../state/DashboardStateProvider";
import { DetailFailure, DetailLoading, formatDate, StatusPill, SummaryFact } from "./WorkDetailPage";
import { humanize, shortIdentifier } from "./runDetailModel";
import { assessmentChanged, buildReadinessDecisionRequest, groupFindings, readinessActionKey, readinessActionPresentation, routeChangePresentation, type AllowedAction, type ReadinessFinding, type ReadinessView } from "./runReadinessModel";

type Schemas = components["schemas"];

export function RunReadinessPage() {
  const { route } = useRouter();
  const { state } = useDashboardState();
  const workId = route.params.workId;
  const runId = route.params.runId;
  const [run, setRun] = useState<Schemas["Run"]>();
  const [work, setWork] = useState<Schemas["WorkItem"]>();
  const [view, setView] = useState<ReadinessView>();
  const [missing, setMissing] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const viewRef = useRef<ReadinessView | undefined>(undefined);
  const dialogRef = useRef<HTMLDialogElement>(null);

  const acceptView = useCallback((next: ReadinessView) => {
    const previous = viewRef.current;
    if (previous && assessmentChanged(previous, next) && dialogRef.current?.open) {
      dialogRef.current.close();
      setNotice("The readiness assessment changed. Review the latest assessment before deciding.");
    }
    viewRef.current = next; setView(next); setMissing(false);
  }, []);

  const load = useCallback(async (signal?: AbortSignal) => {
    try {
      const [runView, workView] = await Promise.all([apiClient.getRun(runId, signal), apiClient.getWorkItem(workId, signal)]);
      if (runView.run.workItemId !== workView.work.id) throw new MismatchedRunError();
      setRun(runView.run); setWork(workView.work);
      try {
        acceptView(await apiClient.getRunReadiness(runId, signal));
      } catch (cause) {
        if (cause instanceof ApiRequestError && cause.status === 404) {
          viewRef.current = undefined; setView(undefined); setMissing(true);
        } else { throw cause; }
      }
      setError("");
    } catch (cause) {
      if (!signal?.aborted) setError(readinessLoadError(cause));
    }
  }, [acceptView, runId, workId]);

  useEffect(() => {
    const abort = new AbortController();
    void load(abort.signal);
    return () => abort.abort();
  }, [load, state.cursor]);

  if (error && !run) return <DetailFailure title="Readiness unavailable" message={error} />;
  if (!run || !work || (!view && !missing)) return <DetailLoading label="Loading run readiness" />;

  return <div className="page detail-page readiness-page">
    <nav className="detail-breadcrumb" aria-label="Breadcrumb"><AppLink to="/board">Board</AppLink><span aria-hidden="true">/</span><AppLink to={`/work/${encodeURIComponent(work.id)}`}>{shortIdentifier(work.id)}</AppLink><span aria-hidden="true">/</span><AppLink to={`/work/${encodeURIComponent(work.id)}/run/${encodeURIComponent(run.id)}`}>{shortIdentifier(run.id)}</AppLink><span aria-hidden="true">/</span><span>Readiness</span></nav>
    <header className="page-header detail-header readiness-header"><div><p className="eyebrow">Run readiness · {shortIdentifier(run.id)}</p><h1>{work.title}</h1><p className="page-header__description">Review the latest durable assessment and record one server-authorized choice. Readiness decisions do not directly resume, extend, or cancel the run.</p></div><StatusPill status={run.status} /></header>
    {notice && <p className="detail-action-message" role="status">{notice}</p>}
    {error && <p className="detail-action-message detail-action-message--error" role="alert">{error}</p>}
    {missing ? <NoAssessment runId={run.id} /> : view && <ReadinessWorkspace view={view} run={run} dialogRef={dialogRef} onAccepted={(next, message) => { acceptView(next); setNotice(message); }} onStale={async () => { setNotice("The readiness assessment changed or was already decided. Review the refreshed assessment before choosing again."); await load(); }} />}
  </div>;
}

function ReadinessWorkspace({ view, run, dialogRef, onAccepted, onStale }: { view: ReadinessView; run: Schemas["Run"]; dialogRef: React.RefObject<HTMLDialogElement | null>; onAccepted(next: ReadinessView, message: string): void; onStale(): Promise<void> }) {
  const [selectedKey, setSelectedKey] = useState("");
  const selected = view.allowedActions.find((action) => readinessActionKey(action) === selectedKey);
  const groups = useMemo(() => groupFindings(view.assessment.findings), [view.assessment.findings]);

  function choose(action: AllowedAction) {
    setSelectedKey(readinessActionKey(action));
    dialogRef.current?.showModal();
  }

  return <>
    <DispositionPanel view={view} />
    <section className="detail-summary readiness-summary" aria-label="Readiness assessment summary"><SummaryFact label="Assessment" value={shortIdentifier(view.assessment.assessmentId)} mono /><SummaryFact label="Assessed node" value={view.assessment.nodeId} mono /><SummaryFact label="Resource version" value={String(view.resourceVersion)} /><SummaryFact label="Updated" value={formatDate(view.updatedAt)} /></section>
    <div className="readiness-layout"><section className="readiness-primary"><ScorePanel scores={view.assessment.scores} /><FindingPanel groups={groups} />{view.assessment.routeChange && <RouteChangePanel current={run.routeSnapshot} change={view.assessment.routeChange} />}</section><aside className="readiness-sidebar"><DecisionPanel view={view} onChoose={choose} />{view.decision && <DecisionReceipt decision={view.decision} />}</aside></div>
    <DecisionDialog dialogRef={dialogRef} view={view} action={selected} onAccepted={(next) => { setSelectedKey(""); onAccepted(next, "Readiness decision recorded. Any workflow effect remains pending and separate."); }} onStale={async () => { setSelectedKey(""); await onStale(); }} />
  </>;
}

function DispositionPanel({ view }: { view: ReadinessView }) {
  const copy = dispositionCopy(view.assessment.disposition);
  return <section className={`readiness-disposition readiness-disposition--${view.assessment.disposition}`} aria-labelledby="readiness-disposition-title"><div><p className="eyebrow">Server disposition</p><h2 id="readiness-disposition-title">{copy.title}</h2><p>{copy.description}</p></div><span>{humanize(view.status)}</span></section>;
}

function ScorePanel({ scores }: { scores: Schemas["ReadinessScore"][] }) {
  return <section className="detail-section readiness-scores"><div className="section-heading"><div><p className="eyebrow">Assessment evidence</p><h2>Scores</h2></div><span className="section-count">{scores.length}</span></div>{scores.length === 0 ? <p className="readiness-empty-copy">No scores were recorded.</p> : <div className="score-list">{scores.map((score) => <article key={score.name}><header><strong>{humanize(score.name)}</strong><span>{Math.round(score.value * 100)}%</span></header><div className="score-meter" role="meter" aria-label={score.name} aria-valuemin={0} aria-valuemax={100} aria-valuenow={Math.round(score.value * 100)}><span style={{ width: `${Math.max(0, Math.min(100, score.value * 100))}%` }} /></div><EvidenceList values={score.evidence} /></article>)}</div>}</section>;
}

function FindingPanel({ groups }: { groups: ReturnType<typeof groupFindings> }) {
  const definitions = [
    ["information", "Information", "Recorded context; it does not authorize a route change."],
    ["recommendation", "Recommendations", "Advisory choices that require an attributable decision."],
    ["policy_gate", "Policy gates", "Versioned policy evidence and its recorded status."],
    ["invariant", "Invariants", "Constraints the assessment must preserve."],
  ] as const;
  return <section className="detail-section"><div className="section-heading"><div><p className="eyebrow">Structured findings</p><h2>Readiness findings</h2></div></div><div className="finding-groups">{definitions.map(([level, title, description]) => <section key={level} className={`finding-group finding-group--${level}`}><header><div><h3>{title}</h3><p>{description}</p></div><span>{groups[level].length}</span></header>{groups[level].length ? <div className="finding-list">{groups[level].map((finding) => <FindingCard key={finding.code} finding={finding} />)}</div> : <p className="readiness-empty-copy">No {title.toLowerCase()} recorded.</p>}</section>)}</div></section>;
}

function FindingCard({ finding }: { finding: ReadinessFinding }) {
  return <article className="finding-card"><header><div><strong>{finding.summary}</strong><code>{finding.code}</code></div><span>{finding.level === "policy_gate" ? finding.status : finding.level === "invariant" ? finding.status : humanize(finding.level)}</span></header>{finding.level === "policy_gate" && <p className="finding-context">Policy: <code>{finding.policy}</code></p>}{finding.level === "invariant" && <p className="finding-context">Invariant: {finding.invariant}</p>}{"remedyCode" in finding && finding.remedyCode && <p className="finding-remedy">Remedy: <code>{finding.remedyCode}</code></p>}<EvidenceList values={finding.evidence} /></article>;
}

function EvidenceList({ values }: { values: Schemas["ReadinessEvidence"][] }) { return <details className="finding-evidence"><summary>Evidence <span>{values.length}</span></summary><ul>{values.map((item, index) => <li key={`${item.source}:${index}`}><strong>{item.source}</strong><span>{item.observation}</span></li>)}</ul></details>; }

function RouteChangePanel({ current, change }: { current?: Schemas["FrozenRoute"]; change: Schemas["ReadinessRouteChange"] }) {
  const impact = routeChangePresentation(change);
  return <section className="detail-section readiness-route-change"><div className="section-heading"><div><p className="eyebrow">Validated server proposal</p><h2>Optional route change</h2></div><span className="scope-badge">{humanize(impact.authorizationMode)}</span></div><p className="route-change-reason">{change.reason}</p><div className="route-comparison"><RouteBoundary title="Current frozen route" route={current} /><span aria-hidden="true">→</span><RouteBoundary title="Candidate route · revision " suffix={String(change.candidate.revision)} route={impact.candidate} /></div><div className="impact-grid"><ImpactList title="Added nodes" values={impact.addedNodes} tone="added" /><ImpactList title="Removed nodes" values={impact.removedNodes} tone="removed" /><ImpactList title="Added transitions" values={impact.addedTransitions} tone="added" /><ImpactList title="Removed transitions" values={impact.removedTransitions} tone="removed" /></div><p className="route-change-safety">The dashboard displays this validated proposal but never sends client-authored patch operations.</p></section>;
}

function RouteBoundary({ title, suffix = "", route }: { title: string; suffix?: string; route?: Schemas["FrozenRoute"] }) { return <article><span>{title}{suffix}</span>{route ? <><strong>{route.entry}</strong><small>to {route.terminals.join(", ")}</small><p>{route.nodes.length} included nodes</p></> : <p>No current route snapshot.</p>}</article>; }
function ImpactList({ title, values, tone }: { title: string; values: string[]; tone: "added" | "removed" }) { return <section><h3>{title} <span>{values.length}</span></h3>{values.length ? <ul>{values.map((value) => <li data-tone={tone} key={value}><code>{value}</code></li>)}</ul> : <p>None</p>}</section>; }

function DecisionPanel({ view, onChoose }: { view: ReadinessView; onChoose(action: AllowedAction): void }) {
  return <section className="side-panel readiness-actions"><p className="eyebrow">Attributable control</p><h2>Readiness decision</h2>{view.status === "decided" ? <p>This assessment already has a durable decision. No further actions are allowed.</p> : <><p>Only actions authorized by the daemon are shown.</p>{view.allowedActions.length ? <div>{view.allowedActions.map((action) => { const presentation = readinessActionPresentation(action); return <button type="button" className={`readiness-action readiness-action--${presentation.tone}`} key={readinessActionKey(action)} onClick={() => onChoose(action)}><strong>{presentation.label}</strong><span>{presentation.description}</span></button>; })}</div> : <p className="readiness-empty-copy">The daemon has not authorized a decision for this assessment.</p>}</>}</section>;
}

function DecisionReceipt({ decision }: { decision: Schemas["ReadinessDecision"] }) {
  return <section className="side-panel decision-receipt"><p className="eyebrow">Durable receipt</p><h2>{humanize(decision.choice)}</h2><dl><div><dt>Decision</dt><dd><code>{shortIdentifier(decision.decisionId)}</code></dd></div><div><dt>Actor</dt><dd>{decision.actor.type} · {decision.actor.id}</dd></div><div><dt>Recorded</dt><dd><time dateTime={decision.decidedAt}>{formatDate(decision.decidedAt)}</time></dd></div><div><dt>Workflow effect</dt><dd>{humanize(decision.effectStatus)}</dd></div>{decision.remedyCode && <div><dt>Remedy</dt><dd><code>{decision.remedyCode}</code></dd></div>}</dl><blockquote>{decision.reason}</blockquote><small>A pending effect means the choice is recorded; it does not claim the route or run has changed.</small></section>;
}

function DecisionDialog({ dialogRef, view, action, onAccepted, onStale }: { dialogRef: React.RefObject<HTMLDialogElement | null>; view: ReadinessView; action?: AllowedAction; onAccepted(next: ReadinessView): void; onStale(): Promise<void> }) {
  const [reason, setReason] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState("");
  const presentation = action ? readinessActionPresentation(action) : undefined;
  function close() { if (!submitting) { dialogRef.current?.close(); setReason(""); setError(""); } }
  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault(); if (!action) return;
    let body: Schemas["ReadinessDecisionRequest"];
    try { body = buildReadinessDecisionRequest(view, readinessActionKey(action), reason); }
    catch (cause) { setError(cause instanceof Error ? cause.message : "The decision is invalid."); return; }
    setSubmitting(true); setError("");
    try {
      const result = await apiClient.decideRunReadiness(view.assessment.runId, view.resourceVersion, `dashboard-readiness-${crypto.randomUUID()}`, body);
      dialogRef.current?.close(); setReason(""); onAccepted(result);
    } catch (cause) {
      if (cause instanceof ApiRequestError && (cause.status === 409 || cause.status === 412)) {
        dialogRef.current?.close(); await onStale();
      } else { setError("The readiness decision could not be recorded. Check daemon health and try again."); }
    } finally { setSubmitting(false); }
  }
  return <dialog ref={dialogRef} className="work-dialog readiness-dialog" aria-labelledby="readiness-dialog-title" onCancel={(event) => { if (submitting) event.preventDefault(); }} onClose={() => { if (!submitting) { setReason(""); setError(""); } }}><form onSubmit={(event) => void submit(event)}><header className="work-dialog__header"><div><p className="eyebrow">Record readiness choice</p><h2 id="readiness-dialog-title">{presentation?.label ?? "Choose an action"}</h2></div><button type="button" className="icon-button" aria-label="Close readiness decision" onClick={close}>×</button></header><p className="work-dialog__intro">{presentation?.description}</p>{action?.choice === "accept_route_change" && <p className="decision-warning">This accepts only proposal <code>{view.assessment.routeChange?.patchId}</code>. Application or approval remains a separate workflow effect.</p>}{action?.choice === "supply_input" && action.remedy && <div className="selected-remedy"><span>Server-provided remedy</span><strong>{action.remedy.code}</strong><p>{action.remedy.description} Target: <code>{action.remedy.target}</code>.</p></div>}{action?.choice === "cancel" && <p className="decision-warning">This cancels the readiness decision only. It does not invoke run cancellation.</p>}<label className="field"><span>Reason</span><textarea required autoFocus rows={4} maxLength={4096} value={reason} onChange={(event) => setReason(event.target.value)} placeholder="Explain why this choice is appropriate" /></label>{error && <p className="form-error" role="alert">{error}</p>}<footer className="work-dialog__footer"><button className="button" type="button" disabled={submitting} onClick={close}>Back</button><button className="button button--primary" type="submit" disabled={submitting || !action}>{submitting ? "Recording…" : "Record decision"}</button></footer></form></dialog>;
}

function NoAssessment({ runId }: { runId: string }) { return <section className="readiness-no-assessment"><span aria-hidden="true">◇</span><p className="eyebrow">No durable assessment</p><h2>Readiness has not been recorded for this run</h2><p>The dashboard does not infer a ready or blocked state. A provider or system assessment must be validated and recorded before choices appear.</p><code>{shortIdentifier(runId)}</code></section>; }

function dispositionCopy(value: Schemas["ReadinessAssessment"]["disposition"]) {
  switch (value) {
    case "ready": return { title: "Ready", description: "The validated assessment reports no unresolved recommendation or blocking condition." };
    case "choice_required": return { title: "Choice required", description: "A recommendation or advisory gate requires an attributable operator choice." };
    case "policy_blocked": return { title: "Policy blocked", description: "A blocking policy gate is unsatisfied. Only daemon-authorized remedies and cancellation are available." };
    case "invariant_blocked": return { title: "Invariant blocked", description: "A required workflow invariant is violated. It cannot be bypassed locally." };
  }
}
function readinessLoadError(cause: unknown) { if (cause instanceof MismatchedRunError || (cause instanceof ApiRequestError && cause.status === 404)) return "The requested run or work item does not exist."; return "Authoritative readiness data is temporarily unavailable. Check daemon health and try again."; }
class MismatchedRunError extends Error {}
