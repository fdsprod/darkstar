import { useEffect, useState } from "react";

import { ApiRequestError, apiClient } from "../api/client";
import type { components } from "../api/schema.generated";
import { AppLink, useRouter } from "../app/router";
import { PageHeader, type BreadcrumbItem } from "../components/PageStructure";
import { useDashboardState } from "../state/DashboardStateProvider";
import { humanize, shortIdentifier, statusTone } from "./runDetailModel";

type Schemas = components["schemas"];

export function WorkDetailPage() {
  const { route } = useRouter();
  const { state } = useDashboardState();
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
  }, [state.cursor, workId]);

  if (error) return <DetailFailure title="Work item unavailable" message={error} pageTitle="Work item" breadcrumbs={[{ label: "Board", to: "/board" }, { label: shortIdentifier(workId) }]} />;
  if (!view) return <DetailLoading label="Loading work item" pageTitle="Work item" breadcrumbs={[{ label: "Board", to: "/board" }, { label: shortIdentifier(workId) }]} />;

  const project = state.snapshot.projects.find((candidate) => candidate.id === view.work.projectId);
  const runs = [...view.runs].sort((left, right) => right.createdAt.localeCompare(left.createdAt) || right.id.localeCompare(left.id));
  return (
    <div className="page detail-page">
      <PageHeader className="detail-header" eyebrow={`${project?.name ?? "Work item"} · Priority ${view.work.priority}`} title={view.work.title} description="Durable request and run history. Status changes shown here come from daemon projections." breadcrumbs={[{ label: "Board", to: "/board" }, { label: shortIdentifier(view.work.id) }]} status={<StatusPill status={view.work.status} />} actions={<AppLink className="button button--primary" to={`/artifacts?targetKind=work&targetId=${encodeURIComponent(view.work.id)}&ingest=1`}>Add evidence</AppLink>} />

      <section className="detail-summary" aria-label="Work item summary">
        <SummaryFact label="Identifier" value={view.work.id} mono />
        <SummaryFact label="Project" value={project?.name ?? view.work.projectId} />
        <SummaryFact label="Created" value={formatDate(view.work.createdAt)} />
        <SummaryFact label="Last updated" value={formatDate(view.work.updatedAt)} />
      </section>

      <section className="detail-section">
        <div className="section-heading"><div><p className="eyebrow">Execution history</p><h2>Runs</h2></div><span className="section-count">{runs.length}</span></div>
        {runs.length === 0 ? <EmptyDetail title="No runs yet" message="Start this work from the lifecycle board after selecting an installed workflow." /> : (
          <div className="run-list">
            {runs.map((run) => (
              <AppLink className="run-list-item" key={run.id} to={`/work/${encodeURIComponent(view.work.id)}/run/${encodeURIComponent(run.id)}`}>
                <span className={`timeline-marker timeline-marker--${statusTone(run.status)}`} aria-hidden="true" />
                <span className="run-list-item__copy"><strong>{run.workflowId} <small>v{run.workflowVersion}</small></strong><span>{shortIdentifier(run.id)} · updated {formatDate(run.updatedAt)}</span></span>
                <StatusPill status={run.status} />
                <span aria-hidden="true">→</span>
              </AppLink>
            ))}
          </div>
        )}
      </section>

      <section className="detail-section work-plan-evidence">
        <div className="section-heading"><div><p className="eyebrow">Accepted-plan targets</p><h2>Stories &amp; implementation points</h2></div><span className="section-count">{view.stories.length} / {view.points.length}</span></div>
        {view.stories.length === 0 ? <EmptyDetail title="No accepted-plan targets" message="Stories and points appear here when the work plan is durably recorded." /> : <div className="story-targets">{view.stories.map((story) => { const points = view.points.filter((point) => point.storyId === story.id).sort((left, right) => left.position - right.position || left.id.localeCompare(right.id)); return <article key={story.id}><header><div><strong>{story.title}</strong><code>{shortIdentifier(story.id)}</code></div><div><AppLink to={`/artifacts?targetKind=story&targetId=${encodeURIComponent(story.id)}&ingest=1`}>Add story evidence</AppLink><StatusPill status={story.status} /></div></header>{points.length ? <ol>{points.map((point) => <li key={point.id}><span><strong>{point.title}</strong><code>{shortIdentifier(point.id)} · revision {point.revision}</code></span><AppLink to={`/artifacts?targetKind=implementation_point&targetId=${encodeURIComponent(point.id)}&ingest=1`}>Add evidence</AppLink><StatusPill status={point.status} /></li>)}</ol> : <p>No implementation points are recorded for this story.</p>}</article>; })}</div>}
      </section>
    </div>
  );
}

export function DetailLoading({ label, pageTitle, breadcrumbs }: { label: string; pageTitle?: string; breadcrumbs?: BreadcrumbItem[] }) {
  return <div className={pageTitle ? "page detail-page" : undefined} aria-busy="true" aria-live="polite">{pageTitle && <PageHeader eyebrow="Loading authoritative state" title={pageTitle} description={label} breadcrumbs={breadcrumbs} readOnly="Loading" />}<div className="detail-loading"><span /><span /><span /><p>{label}…</p></div></div>;
}

export function DetailFailure({ title, message, pageTitle, breadcrumbs }: { title: string; message: string; pageTitle?: string; breadcrumbs?: BreadcrumbItem[] }) {
  return <div className={pageTitle ? "page detail-page" : undefined}>{pageTitle && <PageHeader eyebrow="Unavailable" title={pageTitle} description={message} breadcrumbs={breadcrumbs} readOnly="Read-only" />}<div className="detail-failure" role="alert"><p className="eyebrow">Unable to load</p><h2>{title}</h2><p>{message}</p><AppLink className="button" to="/board">Return to board</AppLink></div></div>;
}

export function StatusPill({ status }: { status: string }) {
  return <span className={`detail-status detail-status--${statusTone(status)}`}><span aria-hidden="true" />{humanize(status)}</span>;
}

export function SummaryFact({ label, value, mono = false }: { label: string; value: string; mono?: boolean }) {
  return <div className="summary-fact"><span>{label}</span><strong className={mono ? "mono" : undefined}>{value}</strong></div>;
}

export function EmptyDetail({ title, message }: { title: string; message: string }) {
  return <div className="detail-empty"><strong>{title}</strong><p>{message}</p></div>;
}

export function formatDate(value: string) {
  const date = new Date(value);
  return Number.isNaN(date.valueOf()) ? value : new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "short" }).format(date);
}

function detailError(cause: unknown, resource: string) {
  if (cause instanceof ApiRequestError && cause.status === 404) return `The requested ${resource} does not exist.`;
  return `Authoritative ${resource} data is temporarily unavailable. Check daemon health and try again.`;
}
