import { useEffect, useState } from "react";

import { ApiRequestError, apiClient } from "../api/client";
import type { components } from "../api/schema.generated";
import { AppLink, useRouter } from "../app/router";
import { ContextPanel, ContextTabs } from "../components/ContextTabs";
import { AsyncPanel, DiagnosticsDetails, EmptyState, SectionHeader } from "../components/InteractionPatterns";
import { PageHeader, type BreadcrumbItem } from "../components/PageStructure";
import { useDashboardState } from "../state/DashboardStateProvider";
import { humanize, shortIdentifier, statusTone } from "./runDetailModel";
import { ProducedArtifacts } from "./ProducedArtifacts";
import { WorkRoutePreparation } from "./WorkRoutePreparation";
import { contextLocation, latestRun, parseWorkContextTab, type WorkContextTab, workNextAction } from "./workContextModel";

type Schemas = components["schemas"];

export function WorkDetailPage() {
  const { route, search, navigate } = useRouter();
  const { state, refresh } = useDashboardState();
  const workId = route.params.workId;
  const [view, setView] = useState<Schemas["WorkItemView"]>();
  const [error, setError] = useState("");

  useEffect(() => {
    const abort = new AbortController();
    setError("");
    void apiClient.getWorkItem(workId, abort.signal)
      .then((value) => setView(value))
      .catch((cause) => { if (!abort.signal.aborted) setError(detailError(cause, "work item")); });
    return () => abort.abort();
  }, [state.cursor, state.lastSynchronizedAt, workId]);

  if (error) return <DetailFailure title="Work item unavailable" message={error} pageTitle="Work item" breadcrumbs={[{ label: "Board", to: "/board" }, { label: shortIdentifier(workId) }]} />;
  if (!view) return <DetailLoading label="Loading work item" pageTitle="Work item" breadcrumbs={[{ label: "Board", to: "/board" }, { label: shortIdentifier(workId) }]} />;

  const project = state.snapshot.projects.find((candidate) => candidate.id === view.work.projectId);
  const runs = [...view.runs].sort((left, right) => right.createdAt.localeCompare(left.createdAt) || right.id.localeCompare(left.id));
  const currentRun = latestRun(view.runs);
  const params = new URLSearchParams(search);
  const tab = parseWorkContextTab(params.get("tab"));
  const path = `/work/${encodeURIComponent(view.work.id)}`;
  const next = workNextAction(currentRun);
  const tabs = [
    { id: "overview", label: "Overview" },
    { id: "runs", label: "Runs", count: runs.length },
    { id: "evidence", label: "Evidence" },
    { id: "diagnostics", label: "Diagnostics" },
  ] satisfies Array<{ id: WorkContextTab; label: string; count?: number }>;
  return (
    <div className="page detail-page">
      <PageHeader className="detail-header" eyebrow={`${project?.name ?? "Work item"} · Priority ${view.work.priority}`} title={view.work.title} description="One work context for outcome, current execution, evidence, and durable history." breadcrumbs={[{ label: "Board", to: "/board" }, { label: "Work item" }]} status={<StatusPill status={currentRun?.status ?? view.work.status} />} actions={currentRun ? <AppLink className="navigation-action" to={`/work/${encodeURIComponent(view.work.id)}/run/${encodeURIComponent(currentRun.id)}`}>{next.label}</AppLink> : <button className="button button--primary" type="button" onClick={() => navigate(contextLocation(path, params, next.tab))}>{next.label}</button>} />

      <ContextTabs tabs={tabs} active={tab} onSelect={(value) => navigate(contextLocation(path, params, value))} />

      <ContextPanel id="overview" active={tab === "overview"}><section className="detail-summary" aria-label="Work item summary">
        <SummaryFact label="Project" value={project?.name ?? view.work.projectId} />
        <SummaryFact label="Priority" value={String(view.work.priority)} />
        <SummaryFact label="Current run" value={currentRun ? `${currentRun.workflowId} · ${humanize(currentRun.status)}` : "Not prepared"} />
        <SummaryFact label="Next action" value={next.label} />
      </section>{currentRun ? <section className="detail-section current-run-card"><SectionHeader eyebrow="Current execution" title={currentRun.workflowId} meta={<StatusPill status={currentRun.status} />} /><p>Updated {formatDate(currentRun.updatedAt)} · workflow version {currentRun.workflowVersion}</p><AppLink className="navigation-action" to={`/work/${encodeURIComponent(view.work.id)}/run/${encodeURIComponent(currentRun.id)}`}>Open current run context →</AppLink></section> : <EmptyDetail title="No current run" message="Assess a route below before starting provider work." />}

      <WorkRoutePreparation work={view.work} run={currentRun} onChanged={refresh} />

      <section className="detail-section work-plan-evidence">
        <SectionHeader eyebrow="Accepted-plan targets" title={<>Stories &amp; implementation points</>} meta={<span className="section-count">{view.stories.length} / {view.points.length}</span>} />
        {view.stories.length === 0 ? <EmptyDetail title="No accepted-plan targets" message="Stories and points appear here when the work plan is durably recorded." /> : <div className="story-targets">{view.stories.map((story) => { const points = view.points.filter((point) => point.storyId === story.id).sort((left, right) => left.position - right.position || left.id.localeCompare(right.id)); return <article key={story.id}><header><div><strong>{story.title}</strong></div><div><AppLink to={contextLocation(path, params, "evidence")}>Story evidence</AppLink><StatusPill status={story.status} /></div></header>{points.length ? <ol>{points.map((point) => <li key={point.id}><span><strong>{point.title}</strong></span><AppLink to={`/artifacts?targetKind=implementation_point&targetId=${encodeURIComponent(point.id)}&ingest=1`}>Add evidence</AppLink><StatusPill status={point.status} /></li>)}</ol> : <p>No implementation points are recorded for this story.</p>}</article>; })}</div>}
      </section></ContextPanel>

      <ContextPanel id="runs" active={tab === "runs"}><section className="detail-section">
        <SectionHeader eyebrow="Execution history" title="Runs" meta={<span className="section-count">{runs.length}</span>} />
        {runs.length === 0 ? <EmptyDetail title="No runs yet" message="Start this work from the lifecycle board after selecting an installed workflow." /> : (
          <div className="run-list">
            {runs.map((run) => (
              <AppLink className="run-list-item" key={run.id} to={`/work/${encodeURIComponent(view.work.id)}/run/${encodeURIComponent(run.id)}`}>
                <span className={`timeline-marker timeline-marker--${statusTone(run.status)}`} aria-hidden="true" />
                <span className="run-list-item__copy"><strong>{run.workflowId} <small>v{run.workflowVersion}</small></strong><span>Updated {formatDate(run.updatedAt)}</span></span>
                <StatusPill status={run.status} />
                <span aria-hidden="true">→</span>
              </AppLink>
            ))}
          </div>
        )}
      </section></ContextPanel>

      <ContextPanel id="evidence" active={tab === "evidence"}>{tab === "evidence" && <><ProducedArtifacts scopes={[{ kind: "work", id: view.work.id, label: "Work item" }, ...view.stories.map((story) => ({ kind: "story" as const, id: story.id, label: `Story · ${story.title}` })), ...view.points.map((point) => ({ kind: "implementation_point" as const, id: point.id, label: `Implementation point · ${point.title}` }))]} /><AppLink className="navigation-action" to={`/artifacts?targetKind=work&targetId=${encodeURIComponent(view.work.id)}&ingest=1`}>Add work evidence</AppLink></>}</ContextPanel>

      <ContextPanel id="diagnostics" active={tab === "diagnostics"}><DiagnosticsDetails label="Work diagnostics"><dl><div><dt>Work identifier</dt><dd>{view.work.id}</dd></div><div><dt>Project identifier</dt><dd>{view.work.projectId}</dd></div><div><dt>Resource version</dt><dd>{view.work.resourceVersion}</dd></div><div><dt>Created</dt><dd>{formatDate(view.work.createdAt)}</dd></div><div><dt>Last updated</dt><dd>{formatDate(view.work.updatedAt)}</dd></div></dl></DiagnosticsDetails><p className="diagnostic-route-links"><AppLink to={`/artifacts?targetKind=work&targetId=${encodeURIComponent(view.work.id)}`}>Artifact registry deep link</AppLink>{currentRun && <AppLink to="/agents">Agent diagnostics</AppLink>}</p></ContextPanel>
    </div>
  );
}

export function DetailLoading({ label, pageTitle, breadcrumbs }: { label: string; pageTitle?: string; breadcrumbs?: BreadcrumbItem[] }) {
  return <div className={pageTitle ? "page detail-page" : undefined}>{pageTitle && <PageHeader eyebrow="Loading authoritative state" title={pageTitle} description={label} breadcrumbs={breadcrumbs} readOnly="Loading" />}<AsyncPanel state="loading" title={label} message="Waiting for the local API to return authoritative state." /></div>;
}

export function DetailFailure({ title, message, pageTitle, breadcrumbs }: { title: string; message: string; pageTitle?: string; breadcrumbs?: BreadcrumbItem[] }) {
  return <div className={pageTitle ? "page detail-page" : undefined}>{pageTitle && <PageHeader eyebrow="Unavailable" title={pageTitle} description={message} breadcrumbs={breadcrumbs} readOnly="Read-only" />}<AsyncPanel state="error" title={title} message={message} action={<AppLink className="navigation-action" to="/board">Return to board →</AppLink>} /></div>;
}

export function StatusPill({ status }: { status: string }) {
  return <span className={`detail-status detail-status--${statusTone(status)}`}><span aria-hidden="true" />{humanize(status)}</span>;
}

export function SummaryFact({ label, value, mono = false }: { label: string; value: string; mono?: boolean }) {
  return <div className="summary-fact"><span>{label}</span><strong className={mono ? "mono" : undefined}>{value}</strong></div>;
}

export function EmptyDetail({ title, message }: { title: string; message: string }) {
  return <EmptyState kind="awaiting" title={title} message={message} compact />;
}

export function formatDate(value: string) {
  const date = new Date(value);
  return Number.isNaN(date.valueOf()) ? value : new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "short" }).format(date);
}

function detailError(cause: unknown, resource: string) {
  if (cause instanceof ApiRequestError && cause.status === 404) return `The requested ${resource} does not exist.`;
  return `Authoritative ${resource} data is temporarily unavailable. Check daemon health and try again.`;
}
