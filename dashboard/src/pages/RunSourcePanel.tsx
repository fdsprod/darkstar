import type { components } from "../api/schema.generated";
import "./tickets.css";

type Schemas = components["schemas"];

export function RunSourcePanel({ snapshot }: { snapshot: Schemas["RunSourceSnapshot"] }) {
  const ticket = snapshot.ticket;
  return <details className="detail-section run-source-panel">
    <summary>Ticket version used by this run</summary>
    <h3>{ticket.title}</h3>
    <p>This retained input remains unchanged by later ticket edits or project source changes.</p>
    <dl className="work-source-facts">
      <dt>Source</dt><dd>{snapshot.ref.Namespace.Provider.replaceAll("_", " ")} · {ticket.key || snapshot.ref.ID}</dd>
      <dt>Approved ticket status</dt><dd>{ticket.businessState.state === "known" ? ticket.businessState.value.Name || ticket.businessState.value.ID : ticket.businessState.state}</dd>
      <dt>Ticket revision</dt><dd>{ticket.revision}</dd>
      <dt>Approved</dt><dd>{new Date(snapshot.approvedAt).toLocaleString()}</dd>
      <dt>Captured for this run</dt><dd>{new Date(snapshot.capturedAt).toLocaleString()}</dd>
      <dt>Source binding revision</dt><dd>{snapshot.bindingRevision}</dd>
    </dl>
    {ticket.url && /^https?:\/\//i.test(ticket.url) && <a href={ticket.url} target="_blank" rel="noreferrer">Open original source ticket</a>}
    <div className="ticket-description">{ticket.description}</div>
    <details><summary>Input provenance</summary><dl>
      <dt>Approved observation</dt><dd>{snapshot.observationId}</dd>
      <dt>Admission</dt><dd>{snapshot.admissionId}</dd>
      <dt>Original evidence</dt><dd>{ticket.evidenceRef}</dd>
      <dt>Adapter</dt><dd>{snapshot.pin.AdapterID} · {snapshot.pin.AdapterVersion}</dd>
      <dt>Connection revision</dt><dd>{snapshot.pin.ConfigRevision}</dd>
    </dl></details>
  </details>;
}
