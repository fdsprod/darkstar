import { useCallback, useEffect, useMemo, useState } from "react";

import { ApiRequestError, apiClient } from "../api/client";
import type { components } from "../api/schema.generated";
import { AppLink, useRouter } from "../app/router";
import { useDashboardState } from "../state/DashboardStateProvider";
import { DetailFailure, DetailLoading, EmptyDetail, formatDate, StatusPill, SummaryFact } from "./WorkDetailPage";
import { attemptsForVisit, eventCategory, humanize, shortIdentifier, sortNodeVisits, statusTone, terminalBoundary } from "./runDetailModel";

type Schemas = components["schemas"];
type RunView = Schemas["RunView"];

export function RunDetailPage() {
  const { route } = useRouter();
  const { state } = useDashboardState();
  const workId = route.params.workId;
  const runId = route.params.runId;
  const [view, setView] = useState<RunView>();
  const [work, setWork] = useState<Schemas["WorkItem"]>();
  const [error, setError] = useState("");
  const [action, setAction] = useState("");
  const [actionMessage, setActionMessage] = useState("");

  const load = useCallback(async (signal?: AbortSignal) => {
    try {
      const [runView, workView] = await Promise.all([apiClient.getRun(runId, signal), apiClient.getWorkItem(workId, signal)]);
      if (runView.run.workItemId !== workView.work.id) throw new MismatchedRunError();
      setView(runView); setWork(workView.work); setError("");
    } catch (cause) {
      if (signal?.aborted) return;
      setError(runDetailError(cause));
    }
  }, [runId, workId]);

  useEffect(() => {
    const abort = new AbortController();
    void load(abort.signal);
    return () => abort.abort();
  }, [load, state.cursor]);

  const invoke = async (name: "pause" | "resume" | "retry") => {
    if (!view) return;
    setAction(name); setActionMessage("");
    try {
      const key = `dashboard-${name}-${crypto.randomUUID()}`;
      if (name === "pause") await apiClient.pauseRun(view.run.id, view.run.resourceVersion, key);
      if (name === "resume") await apiClient.resumeRun(view.run.id, view.run.resourceVersion, key);
      if (name === "retry") await apiClient.retryRun(view.run.id, view.run.resourceVersion, key);
      await load();
      setActionMessage(`${humanize(name)} requested. The detail now reflects daemon state.`);
    } catch (cause) {
      await load();
      setActionMessage(safeActionError(cause));
    } finally { setAction(""); }
  };

  if (error && !view) return <DetailFailure title="Run unavailable" message={error} />;
  if (!view || !work) return <DetailLoading label="Loading run timeline" />;

  const routeSnapshot = view.run.routeSnapshot;
  const visits = sortNodeVisits(view.nodes);
  const linkedAttempts = new Set(visits.flatMap((visit) => attemptsForVisit(view.attempts, visit.id).map((attempt) => attempt.id)));
  const unlinkedAttempts = view.attempts.filter((attempt) => !linkedAttempts.has(attempt.id));
  const controls = runControls(view.run.status);

  return (
    <div className="page detail-page run-detail-page">
      <nav className="detail-breadcrumb" aria-label="Breadcrumb"><AppLink to="/board">Board</AppLink><span aria-hidden="true">/</span><AppLink to={`/work/${encodeURIComponent(work.id)}`}>{shortIdentifier(work.id)}</AppLink><span aria-hidden="true">/</span><span>{shortIdentifier(view.run.id)}</span></nav>
      <header className="page-header detail-header run-detail-header">
        <div><p className="eyebrow">Run · {shortIdentifier(view.run.id)}</p><h1>{work.title}</h1><p className="page-header__description">{view.run.workflowId} · v{view.run.workflowVersion}. Node visits, attempts, and evidence are read from durable projections.</p></div>
        <div className="run-header-actions"><StatusPill status={view.run.status} /><AppLink className="button" to={`/work/${encodeURIComponent(work.id)}/run/${encodeURIComponent(view.run.id)}/readiness`}>Readiness</AppLink>{controls.map((control) => <button className="button" type="button" key={control} disabled={Boolean(action)} onClick={() => void invoke(control)}>{action === control ? "Requesting…" : humanize(control)}</button>)}</div>
      </header>
      {actionMessage && <p className="detail-action-message" role="status">{actionMessage}</p>}
      {error && <p className="detail-action-message detail-action-message--error" role="alert">{error}</p>}

      <section className="detail-summary" aria-label="Run summary">
        <SummaryFact label="Run identifier" value={view.run.id} mono />
        <SummaryFact label="Resource version" value={String(view.run.resourceVersion)} />
        <SummaryFact label="Created" value={formatDate(view.run.createdAt)} />
        <SummaryFact label="Last updated" value={formatDate(view.run.updatedAt)} />
      </section>

      <div className="run-detail-grid">
        <main className="run-detail-primary">
          <RoutePanel run={view.run} visits={visits} />
          <NodeTimeline visits={visits} attempts={view.attempts} unlinkedAttempts={unlinkedAttempts} />
          <EventTimeline view={view} />
        </main>
        <aside className="run-detail-aside" aria-label="Run evidence">
          <BoundaryPanel route={routeSnapshot} />
          <RecordedCommands view={view} />
          <EvidenceCoverage view={view} />
        </aside>
      </div>
    </div>
  );
}

function RoutePanel({ run, visits }: { run: Schemas["Run"]; visits: Schemas["NodeVisit"][] }) {
  const route = run.routeSnapshot;
  const latestByNode = useMemo(() => {
    const values = new Map<string, Schemas["NodeVisit"]>();
    for (const visit of visits) {
      const current = values.get(visit.nodeId);
      if (!current || current.updatedAt < visit.updatedAt) values.set(visit.nodeId, visit);
    }
    return values;
  }, [visits]);
  return <section className="detail-section route-panel">
    <div className="section-heading"><div><p className="eyebrow">Frozen execution plan</p><h2>Selected route</h2></div>{run.routeDigest && <span className="digest" title={run.routeDigest}>{shortIdentifier(run.routeDigest)}</span>}</div>
    <div className="requested-route"><span>Requested route</span><strong>Workflow default requested · {run.workflowId} v{run.workflowVersion}</strong><p>The current API accepts the installed workflow default; no custom route request is persisted. The selected route below is the authoritative frozen snapshot.</p></div>
    {!route ? <EmptyDetail title="Route not frozen" message="This run has not selected a durable route snapshot yet." /> : <>
      <div className="route-facts"><SummaryFact label="Entry" value={route.entry} mono /><SummaryFact label="Terminal boundary" value={route.terminals.join(", ") || "None recorded"} mono /><SummaryFact label="Included nodes" value={String(route.nodes.length)} /><SummaryFact label="Excluded nodes" value={String(route.excludedNodes.length)} /></div>
      <ol className="route-nodes" aria-label="Frozen route nodes">{route.nodes.map((node, index) => {
        const visit = latestByNode.get(node.id);
        const isTerminal = route.terminals.includes(node.id);
        return <li key={node.id}><span className={`timeline-marker timeline-marker--${statusTone(visit?.status ?? "pending")}`} aria-hidden="true" /><div><strong>{node.id}</strong><span>{index === 0 && node.id === route.entry ? "Entry" : isTerminal ? "Terminal" : "Included"}{visit ? ` · ${humanize(visit.status)}` : " · Not visited"}</span></div></li>;
      })}</ol>
      {route.inputRequirements.length > 0 && <div className="route-requirements"><h3>Input requirements</h3>{route.inputRequirements.map((requirement) => <p key={`${requirement.node}:${requirement.input}`}><strong>{requirement.node}.{requirement.input}</strong><span>{requirement.code} · {requirement.source}</span></p>)}</div>}
    </>}
  </section>;
}

function NodeTimeline({ visits, attempts, unlinkedAttempts }: { visits: Schemas["NodeVisit"][]; attempts: Schemas["Attempt"][]; unlinkedAttempts: Schemas["Attempt"][] }) {
  return <section className="detail-section">
    <div className="section-heading"><div><p className="eyebrow">Durable execution</p><h2>Node visits and attempts</h2></div><span className="section-count">{visits.length}</span></div>
    {visits.length === 0 ? <EmptyDetail title="No node visits recorded" message="The run has not activated a node visit." /> : <ol className="node-timeline">{visits.map((visit) => {
      const visitAttempts = attemptsForVisit(attempts, visit.id);
      return <li key={visit.id} className="node-visit"><span className={`timeline-marker timeline-marker--${statusTone(visit.status)}`} aria-hidden="true" /><div className="node-visit__body"><header><div><strong>{visit.nodeId}</strong><span>{shortIdentifier(visit.id)} · revision {visit.resourceVersion}</span></div><StatusPill status={visit.status} /></header><p className="node-visit__time">Activated {formatDate(visit.createdAt)} · updated {formatDate(visit.updatedAt)}</p>{visitAttempts.length === 0 ? <p className="attempt-empty">No attempt recorded for this visit.</p> : <div className="attempt-list">{visitAttempts.map((attempt, index) => <AttemptRow key={attempt.id} attempt={attempt} ordinal={index + 1} />)}</div>}</div></li>;
    })}</ol>}
    {unlinkedAttempts.length > 0 && <div className="unlinked-attempts"><h3>Point or legacy attempts</h3><p>These attempts do not name a durable node visit, so they are kept separate rather than guessed into the timeline.</p>{unlinkedAttempts.map((attempt, index) => <AttemptRow key={attempt.id} attempt={attempt} ordinal={index + 1} />)}</div>}
  </section>;
}

function AttemptRow({ attempt, ordinal }: { attempt: Schemas["Attempt"]; ordinal: number }) {
  const target = attempt.pointId ? `${attempt.pointId} · point revision ${attempt.pointRevision}` : attempt.nodeId ?? "Unscoped attempt";
  return <article className="attempt-row"><div className="attempt-row__ordinal">{ordinal}</div><div className="attempt-row__copy"><strong>{attempt.provider} · {target}</strong><span>{shortIdentifier(attempt.id)} · sequence {attempt.lastSequence}</span>{attempt.providerThreadId && <span>Provider thread {shortIdentifier(attempt.providerThreadId)}</span>}{attempt.logReference && <code>Log {attempt.logReference}</code>}</div><StatusPill status={attempt.status} /></article>;
}

function EventTimeline({ view }: { view: RunView }) {
  return <section className="detail-section">
    <div className="section-heading"><div><p className="eyebrow">Bounded audit window</p><h2>Latest durable events</h2></div><span className="section-count">{view.timeline.length}</span></div>
    {view.timelinePageInfo.hasEarlier && <p className="bounded-note">Earlier events are not included in this 200-event dashboard window. Use a run export for complete evidence.</p>}
    {view.timeline.length === 0 ? <EmptyDetail title="No correlated events" message="No durable run events are currently available in the dashboard window." /> : <ol className="event-timeline">{view.timeline.map((event) => <li key={event.id}><time dateTime={event.occurredAt}>{formatDate(event.occurredAt)}</time><span className={`event-category event-category--${eventCategory(event.kind)}`}>{eventCategory(event.kind)}</span><div><strong>{humanize(event.kind)}</strong><span>{event.aggregateType} · {shortIdentifier(event.aggregateId)} · revision {event.aggregateRevision} · {event.actorType}</span></div><code>#{event.globalPosition}</code></li>)}</ol>}
  </section>;
}

function BoundaryPanel({ route }: { route: Schemas["FrozenRoute"] | undefined }) {
  const terminals = terminalBoundary(route);
  return <section className="side-panel"><p className="eyebrow">Execution boundary</p><h2>Terminal boundary</h2>{terminals.length === 0 ? <p>No frozen terminal has been recorded.</p> : <ul className="boundary-list">{terminals.map((terminal) => <li key={terminal}><span aria-hidden="true">◎</span><code>{terminal}</code></li>)}</ul>}<small>Execution may stop at any selected terminal; the dashboard does not predict completion.</small></section>;
}

function RecordedCommands({ view }: { view: RunView }) {
  return <section className="side-panel"><div className="side-panel__heading"><div><p className="eyebrow">Audit evidence</p><h2>Recorded commands</h2></div><span>{view.commands.length}</span></div><p className="panel-context">Commands shown here have durable run events; rejected commands without an event are not included.</p>{view.commandsPageInfo.hasEarlier && <p className="bounded-note">Older command summaries are outside this 100-command window.</p>}{view.commands.length === 0 ? <p>No commands with durable run events are recorded in this window.</p> : <ol className="command-list">{view.commands.map((command, index) => <li key={`${command.scope}:${command.createdAt}:${index}`}><div><strong>{humanize(command.status)}</strong><span>{command.scope}</span></div><time dateTime={command.createdAt}>{formatDate(command.createdAt)}</time>{command.responseStatus && <code>HTTP {command.responseStatus}</code>}</li>)}</ol>}</section>;
}

function EvidenceCoverage({ view }: { view: RunView }) {
  const categories = new Set(view.timeline.map((event) => eventCategory(event.kind)));
  return <section className="side-panel"><p className="eyebrow">Recorded facts</p><h2>Execution evidence</h2><dl className="coverage-list"><Coverage label="Command lifecycle" present={view.commands.length > 0 || categories.has("command")} /><Coverage label="Validation/checkpoints" present={categories.has("validation")} /><Coverage label="Commits/delivery" present={categories.has("commit")} /><Coverage label="Attempt logs" present={view.attempts.some((attempt) => Boolean(attempt.logReference))} /></dl><small>“Not recorded” means no matching fact exists in this bounded query response; the dashboard does not infer one.</small></section>;
}

function Coverage({ label, present }: { label: string; present: boolean }) { return <div><dt>{label}</dt><dd data-present={present}>{present ? "Recorded" : "Not recorded"}</dd></div>; }

function runControls(status: Schemas["Run"]["status"]): Array<"pause" | "resume" | "retry"> {
  if (status === "queued" || status === "running") return ["pause"];
  if (status === "waiting" || status === "blocked") return ["resume"];
  if (status === "failed") return ["retry"];
  return [];
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
