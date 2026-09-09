import { RunLive } from "./RunLive";
import { useCallback, useEffect, useState } from "react";

import { ApiRequestError, apiClient } from "../api/client";
import type { components } from "../api/schema.generated";
import { AppLink, useRouter } from "../app/router";
import { PageHeader } from "../components/PageStructure";
import { DetailFailure, DetailLoading, formatDate, StatusPill } from "./WorkDetailPage";
import { availableCardActions, buildWorkTransitionRequest, transitionDecision, transitionTargetForAction, type BoardCardAction } from "./boardModel";
import { humanize, shortIdentifier } from "./runDetailModel";

type Schemas = components["schemas"];
type RunView = Schemas["RunView"];

export function RunDetailPage() {
  const { route } = useRouter();
  const workId = route.params.workId;
  const runId = route.params.runId;
  const [view, setView] = useState<RunView>();
  const [work, setWork] = useState<Schemas["WorkItem"]>();
  const [transitionPlan, setTransitionPlan] = useState<Schemas["WorkTransitionPlan"]>();
  const [error, setError] = useState("");
  const [action, setAction] = useState("");
  const [actionMessage, setActionMessage] = useState("");

  const load = useCallback(async (signal?: AbortSignal) => {
    try {
      const [runView, workView] = await Promise.all([apiClient.getRun(runId, signal), apiClient.getWorkItem(workId, signal)]);
      if (runView.run.workItemId !== workView.work.id) throw new MismatchedRunError();
      const plan = await apiClient.planWorkItemTransition(workView.work.id, lifecycleHint(runView.run.status), undefined, signal);
      setView(runView); setWork(workView.work); setTransitionPlan(plan); setError("");
    } catch (cause) {
      if (signal?.aborted) return;
      setError(runDetailError(cause));
    }
  }, [runId, workId]);

  useEffect(() => {
    const abort = new AbortController();
    void load(abort.signal);
    const timer = setInterval(() => void load(abort.signal), 2000);
    return () => { abort.abort(); clearInterval(timer); };
  }, [load]);

  const invoke = async (name: BoardCardAction) => {
    if (!view || !work || !transitionPlan) return;
    const target = transitionTargetForAction(name);
    const decision = transitionDecision(transitionPlan, target);
    if (decision?.availability !== "enabled") return;
    if (decision.confirmation === "required" && !window.confirm(`Apply ${humanize(name)} to “${work.title}”? Durable run history and evidence will be preserved.`)) return;
    setAction(name); setActionMessage("");
    try {
      const key = `dashboard-work-transition-${crypto.randomUUID()}`;
      const result = await apiClient.applyWorkItemTransition(work.id, transitionPlan.resourceVersion, key, buildWorkTransitionRequest("menu", target));
      setTransitionPlan(result.after);
      await load();
      setActionMessage(`${humanize(name)} requested. The detail now reflects daemon state.`);
    } catch (cause) {
      if (cause instanceof ApiRequestError && cause.workTransitionPlan) setTransitionPlan(cause.workTransitionPlan);
      await load();
      setActionMessage(safeActionError(cause));
    } finally { setAction(""); }
  };

  if (error && !view) return <DetailFailure title="Run unavailable" message={error} pageTitle="Run" breadcrumbs={[{ label: "Board", to: "/board" }, { label: shortIdentifier(workId), to: `/work/${encodeURIComponent(workId)}` }, { label: shortIdentifier(runId) }]} />;
  if (!view || !work) return <DetailLoading label="Loading run timeline" pageTitle="Run" breadcrumbs={[{ label: "Board", to: "/board" }, { label: shortIdentifier(workId), to: `/work/${encodeURIComponent(workId)}` }, { label: shortIdentifier(runId) }]} />;

  const controls = availableCardActions({ work, run: view.run, lifecycle: transitionPlan?.state ?? lifecycleHint(view.run.status) }, transitionPlan)
    .filter((control) => control !== "prepare" && !(control === "resume" && view.nodes.some(node => node.status === "waiting_checkpoint")));
  return <div className="page detail-page run-detail-page">
    <PageHeader className="detail-header run-detail-header" eyebrow="Run" title={work.title} description={<>{view.run.workflowId} · v{view.run.workflowVersion}</>} breadcrumbs={[{ label: "Board", to: "/board" }, { label: work.title, to: `/work/${encodeURIComponent(work.id)}` }, { label: "Run" }]} status={<StatusPill status={view.run.status} />} actions={<>{controls.map(control => <button className={`button ${control === "cancel" ? "button--danger" : ""}`} key={control} disabled={Boolean(action)} onClick={() => void invoke(control)}>{action === control ? "Requesting…" : control === "cancel" ? "Stop" : humanize(control)}</button>)}</>} />
    {actionMessage && <p role="status">{actionMessage}</p>}
    {error && <p role="alert">{error}</p>}
    {view.issue && <p className="run-inline-error">{view.issue.message}</p>}
    <RunLive key={view.run.id} view={view} refresh={() => load()} />
    <details className="run-details"><summary>Run details</summary><dl><div><dt>Run</dt><dd>{view.run.id}</dd></div><div><dt>Started</dt><dd>{formatDate(view.run.createdAt)}</dd></div><div><dt>Workflow steps</dt><dd>{view.nodes.map(node => `${node.nodeId}: ${humanize(node.status)}`).join(" · ")}</dd></div></dl><AppLink to={`/artifacts?targetKind=run&targetId=${encodeURIComponent(view.run.id)}&ingest=1`}>Attach an artifact to this run</AppLink>{view.nodes.map(node => <p key={node.id}><AppLink to={`/artifacts?targetKind=node&targetId=${encodeURIComponent(`${view.run.id}/${node.nodeId}`)}&ingest=1`}>Attach an artifact to {node.nodeId}</AppLink></p>)}</details>
  </div>;
}

function lifecycleHint(status: Schemas["Run"]["status"]): Schemas["WorkLifecycleState"] {
  if (status === "ready" || status === "draft" || status === "pending") return "ready";
  if (status === "queued" || status === "running") return "running";
  if (status === "waiting") return "waiting";
  if (status === "blocked" || status === "reconcile_required") return "blocked";
  if (status === "failed") return "failed";
  if (status === "completed" || status === "cancelled") return "done";
  return "backlog";
}

function safeActionError(cause: unknown) {
  if (cause instanceof ApiRequestError && (cause.status === 409 || cause.status === 412)) return "The run changed before the command completed. Authoritative state was refreshed; review it and try again.";
  return "The run command could not be completed. Check daemon health and try again.";
}

function runDetailError(cause: unknown) {
  if (cause instanceof MismatchedRunError || (cause instanceof ApiRequestError && cause.status === 404)) return "The requested run does not exist for this work item.";
  return "Authoritative run data is temporarily unavailable. Check daemon health and try again.";
}

class MismatchedRunError extends Error {}
