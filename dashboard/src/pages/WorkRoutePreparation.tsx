import { useCallback, useEffect, useMemo, useState } from "react";

import { ApiRequestError, apiClient } from "../api/client";
import type { components } from "../api/schema.generated";
import { AppLink } from "../app/router";
import { AsyncPanel, DiagnosticsDetails, SectionHeader } from "../components/InteractionPatterns";
import { humanize, shortIdentifier } from "./runDetailModel";
import { assessmentPresentation, buildPreparationRequest, emptyPreparationDraft, routeDifference, type PreparationDraft, type PreparationState } from "./routePreparationModel";

type Schemas = components["schemas"];

export function WorkRoutePreparation({ work, run, onChanged }: { work: Schemas["WorkItem"]; run?: Schemas["Run"]; onChanged(): Promise<void> }) {
  const [view, setView] = useState<Schemas["RunView"]>();
  const [workflows, setWorkflows] = useState<Schemas["WorkflowVersionSummary"][]>([]);
  const [state, setState] = useState<PreparationState>({ kind: "idle", draft: emptyPreparationDraft() });
  const [priorRoute, setPriorRoute] = useState<Schemas["FrozenRoute"]>();
  const [message, setMessage] = useState("");

  const load = useCallback(async (signal?: AbortSignal) => {
    if (!run) { setView(undefined); return; }
    try { setView(await apiClient.getRun(run.id, signal)); }
    catch (cause) { if (!signal?.aborted) setMessage(actionError(cause, "The prepared route could not be refreshed.")); }
  }, [run]);

  useEffect(() => { const abort = new AbortController(); void load(abort.signal); return () => abort.abort(); }, [load]);
  useEffect(() => { const abort = new AbortController(); void apiClient.listWorkflows(undefined, abort.signal).then(setWorkflows).catch(() => undefined); return () => abort.abort(); }, []);

  const assessment = view?.assessment ?? view?.run.routeSnapshot?.assessment;
  const presentation = assessment ? assessmentPresentation(assessment) : undefined;
  const difference = assessment ? routeDifference(priorRoute, assessment.route) : undefined;
  const draft = state.draft;
  const busy = state.kind === "preparing";
  const prepared = Boolean(view && assessment && (view.run.status === "ready" || view.run.status === "waiting"));

  function updateDraft(change: Partial<PreparationDraft>) { setState({ kind: "idle", draft: { ...draft, ...change } }); setMessage(""); }
  async function prepare() {
    let request: Schemas["CreateRunRequest"];
    try { request = buildPreparationRequest(work.id, draft); }
    catch (cause) { setState({ kind: "error", draft, message: cause instanceof Error ? cause.message : "Preparation input is invalid." }); return; }
    const before = view?.run.routeSnapshot;
    setState({ kind: "preparing", draft, priorRoute: before }); setMessage("");
    try {
      const preparedRun = await apiClient.prepareRun(request, `dashboard-route-prepare-${crypto.randomUUID()}`);
      setPriorRoute(before); await onChanged(); setView(await apiClient.getRun(preparedRun.id)); setState({ kind: "idle", draft });
    } catch (cause) {
      if (cause instanceof ApiRequestError && (cause.status === 409 || cause.status === 412)) {
        await onChanged().catch(() => undefined); await load(); setState({ kind: "stale", draft, message: "Authoritative work or route state changed. Your answers and override draft are preserved; review the refreshed state and assess again." });
      } else setState({ kind: "error", draft, message: actionError(cause, "Route assessment failed.") });
    }
  }
  async function decide(action: "confirm" | "cancel") {
    if (!view || !assessment) return;
    const expectedRun = view.run.id; const expectedVersion = view.run.resourceVersion; const expectedDigest = assessment.digest;
    setMessage(""); setState({ kind: "preparing", draft, priorRoute });
    try {
      if (action === "confirm") await apiClient.startRun(expectedRun, expectedVersion, `dashboard-route-confirm-${crypto.randomUUID()}`, expectedDigest);
      else await apiClient.cancelRun(expectedRun, expectedVersion, `dashboard-route-cancel-${crypto.randomUUID()}`);
      await onChanged(); setState({ kind: "idle", draft }); setMessage(action === "confirm" ? "The exact assessed route was confirmed and queued." : "The prepared route was cancelled. Work and evidence remain available.");
    } catch (cause) {
      if (cause instanceof ApiRequestError && (cause.status === 409 || cause.status === 412)) {
        await onChanged().catch(() => undefined); await load(); setState({ kind: "stale", draft, message: "The prepared run changed before this decision. Your draft is preserved; review the current assessment before deciding again." });
      } else setState({ kind: "error", draft, message: actionError(cause, "The route decision failed.") });
    }
  }

  if (run && !["ready", "waiting", "completed", "cancelled", "failed"].includes(run.status)) return null;
  if (!prepared || !view || !assessment || !presentation) return <section className="detail-section route-preparation">
    <SectionHeader eyebrow="Automatic route assessment" title="Choose the smallest safe workflow route" />
    <p>DARKSTAR assesses the requested outcome, evidence, and project policy. Ready work starts automatically when capacity is available; questions and approvals pause it for review.</p>
    <PreparationEditor draft={draft} workflows={workflows} busy={busy} onChange={updateDraft} />
    {state.kind === "error" || state.kind === "stale" ? <AsyncPanel compact state="error" title={state.kind === "stale" ? "Assessment became stale" : "Assessment unavailable"} message={state.message} /> : undefined}
    <button className="button button--primary" type="button" disabled={busy} onClick={() => void prepare()}>{busy ? "Assessing…" : "Assess route"}</button>
  </section>;

  return <section className="detail-section route-preparation" aria-busy={busy}>
    <SectionHeader eyebrow="Prepared route" title="Route preview and readiness" meta={<span className={`route-readiness route-readiness--${presentation.readiness}`}>{humanize(presentation.readiness)}</span>} />
    <p>{assessment.rationale}</p>
    <div className="route-preparation__facts"><Fact label="Workflow" value={`${view.run.workflowId} v${view.run.workflowVersion}`} /><Fact label="Entry" value={assessment.route.entry} /><Fact label="Terminal boundary" value={assessment.route.terminals.join(", ")} /><Fact label="Routing" value={presentation.routeSource === "automatic" ? "Automatic assessment" : "Advanced override"} /></div>
    <ol className="route-preparation__nodes" aria-label="Assessed workflow route">{assessment.route.nodes.map((node, index) => <li key={node.id}><span>{index + 1}</span><strong>{node.id}</strong>{node.id === assessment.route.entry && <small>Entry</small>}{assessment.route.terminals.includes(node.id) && <small>Terminal</small>}</li>)}</ol>
    {assessment.route.excludedNodes.length > 0 && <details className="route-preparation__skipped"><summary>Skipped stages <span>{assessment.route.excludedNodes.length}</span></summary><ul>{assessment.route.excludedNodes.map((node) => <li key={node.id}><code>{node.id}</code><span>{node.reason}</span></li>)}</ul></details>}
    {difference && <section className="route-preparation__change"><h3>Route changed after reassessment</h3><div><RouteBoundary label="Before" route={difference.before} /><span aria-hidden="true">→</span><RouteBoundary label="After" route={difference.after} /></div>{difference.added.length > 0 && <p>Added: {difference.added.join(", ")}</p>}{difference.removed.length > 0 && <p>Skipped now: {difference.removed.join(", ")}</p>}</section>}
    {(assessment.questions ?? []).length > 0 && <section className="route-preparation__needs"><h3>Missing inputs</h3>{assessment.questions?.map((question) => <label className="field" key={question.id}><span>{question.prompt}</span><input value={draft.answers[question.id] ?? ""} onChange={(event) => updateDraft({ answers: { ...draft.answers, [question.id]: event.target.value } })} /></label>)}</section>}
    {(assessment.confirmationReasons ?? []).length > 0 && <section className="route-preparation__needs"><h3>Confirmation reasons</h3><ul>{assessment.confirmationReasons?.map((reason) => <li key={reason}>{reason}</li>)}</ul></section>}
    {presentation.selectedAssumptions.length > 0 && <section className="route-preparation__needs"><h3>Assumptions</h3><ul>{presentation.selectedAssumptions.map((assumption) => <li key={assumption}>{assumption}</li>)}</ul></section>}
    {presentation.evidence.length > 0 && <section className="route-preparation__evidence"><h3>Evidence</h3><ul>{presentation.evidence.map((item) => <li key={item.reference}><span>{item.reference}</span><strong>{item.used ? "Used" : item.available ? "Available" : "Unavailable"}</strong></li>)}</ul></section>}
    <PreparationEditor draft={draft} workflows={workflows} busy={busy} onChange={updateDraft} compact />
    {(state.kind === "error" || state.kind === "stale") && <AsyncPanel compact state="error" title={state.kind === "stale" ? "Assessment became stale" : "Action unavailable"} message={state.message} />}
    {message && <AsyncPanel compact state="success" title="Route decision recorded" message={message} />}
    <div className="route-preparation__actions">
      {presentation.readiness === "input_required" ? <button className="button button--primary" type="button" disabled={busy} onClick={() => void prepare()}>Supply inputs and reassess</button> : presentation.readiness === "confirmation_required" ? <button className="button button--primary" type="button" disabled={busy} onClick={() => void decide("confirm")}>Approve and queue route</button> : <p>Queued. Starts automatically when a run slot is available.</p>}
      <AppLink className="navigation-action" to={`/artifacts?targetKind=work&targetId=${encodeURIComponent(work.id)}&ingest=1`}>Add work artifact →</AppLink>
      <button className="button button--danger" type="button" disabled={busy} onClick={() => void decide("cancel")}>Cancel preview</button>
    </div>
    <DiagnosticsDetails label="Route assessment diagnostics"><dl><div><dt>Assessment digest</dt><dd>{assessment.digest}</dd></div><div><dt>Input digest</dt><dd>{assessment.inputDigest}</dd></div><div><dt>Workflow digest</dt><dd>{assessment.input.workflowDigest}</dd></div><div><dt>Run</dt><dd>{view.run.id} · revision {view.run.resourceVersion}</dd></div><div><dt>Confidence</dt><dd>{assessment.advice.confidence}</dd></div></dl></DiagnosticsDetails>
  </section>;
}

function PreparationEditor({ draft, workflows, busy, onChange, compact = false }: { draft: PreparationDraft; workflows: Schemas["WorkflowVersionSummary"][]; busy: boolean; onChange(change: Partial<PreparationDraft>): void; compact?: boolean }) {
  return <details className="diagnostics-details route-preparation__editor" open={!compact}><summary>{compact ? "Supply evidence or use an advanced override" : "Advanced route and evidence"}</summary><div className="diagnostics-details__body">
    <label className="field"><span>Evidence references <small>(one per line)</small></span><textarea rows={2} value={draft.evidence} disabled={busy} onChange={(event) => onChange({ evidence: event.target.value })} placeholder="Repository file, URL, artifact, or decision reference" /></label>
    <label className="field"><span>Workflow override <small>(optional)</small></span><select value={draft.workflowId && draft.workflowVersion ? `${draft.workflowId}\u0000${draft.workflowVersion}` : ""} disabled={busy} onChange={(event) => { const [workflowId, workflowVersion] = event.target.value.split("\u0000"); onChange({ workflowId, workflowVersion }); }}><option value="">Automatic (recommended)</option>{workflows.map((workflow) => <option key={`${workflow.name}:${workflow.version}:${workflow.digest}`} value={`${workflow.name}\u0000${workflow.version}`}>{workflow.name} · {workflow.version}</option>)}</select></label>
    <label className="field"><span>Route profile <small>(optional)</small></span><input value={draft.profile} disabled={busy} onChange={(event) => onChange({ profile: event.target.value })} placeholder="Authored profile identifier" /></label>
    <label className="field"><span>Entry node <small>(optional)</small></span><input value={draft.entryNodeId} disabled={busy} onChange={(event) => onChange({ entryNodeId: event.target.value })} placeholder="Explicit route entry" /></label>
    <label className="field"><span>Terminal nodes <small>(optional, comma-separated)</small></span><input value={draft.terminalNodeIds} disabled={busy} onChange={(event) => onChange({ terminalNodeIds: event.target.value })} placeholder="review, delivery" /></label>
    <label className="field"><span>Run inputs JSON <small>(optional)</small></span><textarea rows={3} value={draft.runInputs} disabled={busy} onChange={(event) => onChange({ runInputs: event.target.value })} placeholder={'{"input_name":"value"}'} /></label>
  </div></details>;
}

function Fact({ label, value }: { label: string; value: string }) { return <div><span>{label}</span><strong>{value}</strong></div>; }
function RouteBoundary({ label, route }: { label: string; route: Schemas["FrozenRoute"] }) { return <div><span>{label}</span><strong>{route.entry} → {route.terminals.join(", ")}</strong><small>{route.nodes.length} stages</small></div>; }
function actionError(cause: unknown, fallback: string) { if (cause instanceof ApiRequestError) { if (cause.status === 400) return cause.message; if (cause.status === 409 || cause.status === 412) return "Authoritative route state changed."; } return fallback; }
