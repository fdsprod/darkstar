import { useEffect, useState } from "react";

import { apiClient } from "../api/client";
import type { components } from "../api/schema.generated";
import { AppLink } from "../app/router";
import { AsyncPanel } from "../components/InteractionPatterns";
import "./tickets.css";

type Schemas = components["schemas"];

export function WorkSourcePanel({ workItemId, refreshToken, onView, onChanged }: {
  workItemId: string;
  refreshToken?: string;
  onView?: (view: Schemas["WorkSourceView"]) => void;
  onChanged?: () => Promise<void>;
}) {
  const [view, setView] = useState<Schemas["WorkSourceView"]>();
  const [error, setError] = useState("");
  const [pending, setPending] = useState(false);
  const [reload, setReload] = useState(0);

  useEffect(() => {
    const abort = new AbortController();
    void apiClient.operation("getWorkSourceView", { path: { workId: workItemId }, signal: abort.signal }).then((value) => {
      if (!abort.signal.aborted) {
        setView(value);
        onView?.(value);
      }
    }).catch(() => {
      if (!abort.signal.aborted) {
        setError("Source execution context could not be refreshed. Existing run history remains available.");
      }
    });
    return () => abort.abort();
  }, [workItemId, refreshToken, reload, onView]);

  async function mutate(kind: "refresh" | "approve") {
    if (!view || pending || (kind === "approve" && !view.currentTicket)) {
      return;
    }
    setPending(true);
    setError("");
    try {
      const value = kind === "refresh"
        ? await apiClient.operation("refreshWorkSource", { path: { workId: workItemId } })
        : (await apiClient.operation("approveWorkSource", { path: { workId: workItemId }, body: { schemaVersion: 1, observationId: view.currentTicket!.observationId }, idempotencyKey: `dashboard-source-approve-${crypto.randomUUID()}` })).source;
      setView(value);
      onView?.(value);
      await onChanged?.();
    } catch {
      setError("The source action was not confirmed. Reload this context and review the retained version before retrying.");
    } finally {
      setPending(false);
    }
  }

  return <section className="detail-section work-source-panel" aria-label="Ticket and execution context" aria-busy={pending}>
    <h3>Ticket and execution</h3>
    {error && <AsyncPanel compact state="error" title="Source context needs attention" message={error} />}
    {!view && !error && <p>Loading source lineage…</p>}
    {view && <>
      <WorkSourceFacts view={view} />
      {view.lineage && <>
        <p>Source assessment: <strong>{humanize(view.assessment.state)}</strong></p>
        {view.assessment.reasons.length > 0 && <ul>{view.assessment.reasons.map((reason) => <li key={reason}>{humanize(reason)}</li>)}</ul>}
        <p>Active runs retain their approved input. Source changes are reviewed for future preparation; a missing or inaccessible ticket does not cancel a run.</p>
        <div className="ticket-toolbar">
          <button className="button" type="button" disabled={pending} onClick={() => void mutate("refresh")}>Check original source</button>
          {view.currentTicket && view.currentTicket.observationId !== view.approvedTicket?.observationId && <button className="button button--primary" type="button" disabled={pending || Boolean(error)} onClick={() => void mutate("approve")}>Approve current version for next run</button>}
        </div>
        <SourceVersionComparison approved={view.approvedTicket} current={view.currentTicket} />
        <details><summary>Source lineage history ({view.lineages.length})</summary>
          <ol>{view.lineages.map((lineage) => <li key={lineage.revision}>{humanize(lineage.ref.namespace.provider)} · {lineage.ref.id} · lineage {lineage.revision} · source revision {lineage.bindingRevision}</li>)}</ol>
        </details>
      </>}
    </>}
    <button className="button" type="button" disabled={pending} onClick={() => {
      setError("");
      setReload((value) => value + 1);
    }}>Reload source context</button>
  </section>;
}

export function WorkSourceFacts({ view }: { view: Schemas["WorkSourceView"] }) {
  const ticket = view.currentTicket ?? view.approvedTicket;
  const state = ticket?.businessState;
  return <dl className="work-source-facts">
    <dt>Tracker status</dt><dd>{state?.state === "known" ? state.value.name || state.value.id : state ? humanize(state.state) : "No resolved source observation"}</dd>
    <dt>Local activity</dt><dd>{humanize(view.localActivity)}</dd>
    <dt>Run outcome</dt><dd>{humanize(view.runOutcome)}</dd>
    <dt>External acceptance</dt><dd>{view.externalAcceptance.state === "known" ? view.externalAcceptance.value : humanize(view.externalAcceptance.state)}</dd>
    {ticket?.url && /^https?:\/\//i.test(ticket.url) && <><dt>Original ticket</dt><dd><a href={ticket.url} target="_blank" rel="noreferrer">{ticket.key || ticket.ref.id}</a></dd></>}
  </dl>;
}

function SourceVersionComparison({ approved, current }: {
  approved: Schemas["WorkSourceView"]["approvedTicket"];
  current: Schemas["WorkSourceView"]["currentTicket"];
}) {
  return <details className="ticket-source-comparison"><summary>Compare approved and current source content</summary>
    <div className="ticket-layout">
      <section><h4>Approved for preparation</h4>{approved ? <><strong>{approved.title}</strong><p>Revision {approved.revision}</p><div className="ticket-description">{approved.description}</div></> : <p>No version has been approved.</p>}</section>
      <section><h4>Latest retained source</h4>{current ? <><strong>{current.title}</strong><p>Revision {current.revision}</p><div className="ticket-description">{current.description}</div></> : <p>The source observation is unavailable.</p>}</section>
    </div>
  </details>;
}

export function TicketExecutionAdmission({ projectId, ticket, bindingRevision }: {
  projectId: string;
  ticket: Schemas["BacklogTicket"];
  bindingRevision: number;
}) {
  const [items, setItems] = useState<Schemas["WorkSourceView"][]>([]);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState("");
  useEffect(() => {
    const abort = new AbortController();
    void apiClient.operation("listTicketExecutions", { path: { projectId }, query: { observationId: ticket.observationId }, signal: abort.signal }).then((value) => {
      if (!abort.signal.aborted) {
        setItems(value.items);
      }
    }).catch(() => {
      if (!abort.signal.aborted) {
        setError("Existing execution links could not be loaded. Reload the backlog before approving this version.");
      }
    });
    return () => abort.abort();
  }, [projectId, ticket.ticketKey, ticket.observationId]);

  async function admit() {
    if (pending || error) {
      return;
    }
    setPending(true);
    try {
      const result = await apiClient.operation("admitSourceTicket", { path: { projectId }, body: { schemaVersion: 1, expectedBindingRevision: bindingRevision, observationId: ticket.observationId }, idempotencyKey: `dashboard-ticket-admit-${crypto.randomUUID()}` });
      setItems([result.source]);
    } catch {
      setError("Admission was not confirmed. Reload to review the current source version and any retained execution record before retrying.");
    } finally {
      setPending(false);
    }
  }

  const unavailable = !ticket.currentSource || ["missing", "inaccessible", "archived", "out_of_scope"].includes(ticket.status);
  return <section className="ticket-admission" aria-label="Ticket execution admission">
    <h3>DARKSTAR execution</h3>
    {error && <AsyncPanel compact state="error" title="Admission needs attention" message={error} />}
    {items.map((item) => <article key={item.workItemId}>
      <WorkSourceFacts view={item} />
      <AppLink className="navigation-action" to={`/work/${encodeURIComponent(item.workItemId)}`}>Open work and prepare a run</AppLink>
    </article>)}
    {!unavailable && <>
      <p>Approve this retained ticket version to create or reuse its local work record. Route preparation is a separate action.</p>
      <button className="button button--primary" type="button" disabled={pending || Boolean(error)} onClick={() => void admit()}>{pending ? "Recording approval…" : "Approve this version for work"}</button>
    </>}
  </section>;
}

function humanize(value: string) {
  return value.replaceAll("_", " ");
}
