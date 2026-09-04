import { useEffect, useState } from "react";

import { ApiRequestError, apiClient } from "../api/client";
import type { components } from "../api/schema.generated";
import { AppLink, useRouter } from "../app/router";
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

  if (error) return <DetailFailure title="Work item unavailable" message={error} />;
  if (!view) return <DetailLoading label="Loading work item" />;

  const project = state.snapshot.projects.find((candidate) => candidate.id === view.work.projectId);
  const runs = [...view.runs].sort((left, right) => right.createdAt.localeCompare(left.createdAt) || right.id.localeCompare(left.id));
  return (
    <div className="page detail-page">
      <nav className="detail-breadcrumb" aria-label="Breadcrumb"><AppLink to="/board">Board</AppLink><span aria-hidden="true">/</span><span>{shortIdentifier(view.work.id)}</span></nav>
      <header className="page-header detail-header">
        <div>
          <p className="eyebrow">{project?.name ?? "Work item"} · Priority {view.work.priority}</p>
          <h1>{view.work.title}</h1>
          <p className="page-header__description">Durable request and run history. Status changes shown here come from daemon projections.</p>
        </div>
        <StatusPill status={view.work.status} />
      </header>

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
    </div>
  );
}

export function DetailLoading({ label }: { label: string }) {
  return <div className="page detail-page" aria-busy="true" aria-live="polite"><div className="detail-loading"><span /><span /><span /><p>{label}…</p></div></div>;
}

export function DetailFailure({ title, message }: { title: string; message: string }) {
  return <div className="page detail-page"><div className="detail-failure" role="alert"><p className="eyebrow">Unable to load</p><h1>{title}</h1><p>{message}</p><AppLink className="button" to="/board">Return to board</AppLink></div></div>;
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
