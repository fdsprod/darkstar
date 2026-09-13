import { useEffect, useState, type FormEvent } from "react";

import { apiClient } from "../api/client";
import type { components } from "../api/schema.generated";
import { AppLink } from "../app/router";
import { AsyncPanel, EmptyState } from "../components/InteractionPatterns";
import { PageHeader } from "../components/PageStructure";
import { useDashboardState } from "../state/DashboardStateProvider";
import "./tickets.css";
import { BacklogBrowser } from "./BacklogBrowser";

type Schemas = components["schemas"];

export function TicketsPage() {
  const { state } = useDashboardState();
  const [selection, setSelection] = useState("");
  const projectId = selection || state.snapshot.projects[0]?.id || "";
  return (
    <div className="page">
      <PageHeader eyebrow="Project backlog" title="Tickets" description="Ticket content, business status, and retained revisions." breadcrumbs={[{ label: "Tickets" }]} actions={<AppLink to="/board" className="navigation-action">Execution board</AppLink>} />
      <label className="ticket-project">Project
        <select value={projectId} onChange={(event) => setSelection(event.target.value)}>
          {state.snapshot.projects.map((project) => <option key={project.id} value={project.id}>{project.name}</option>)}
        </select>
      </label>
      {projectId ? <>
        <BacklogBrowser key={projectId} projectId={projectId} renderNativeDetail={(ticketId, refresh, onChanged) => <NativeTicketSelection key={`${projectId}/${ticketId}`} projectId={projectId} ticketId={ticketId} refresh={refresh} onChanged={onChanged} />} />
        <details className="ticket-native-history"><summary>Built-in ticket history and editing</summary><NativeTicketBrowser key={projectId} projectId={projectId} /></details>
      </> : <EmptyState kind="empty" title="No projects" message="Register a project to browse its tickets." />}
    </div>
  );
}

function NativeTicketBrowser({ projectId }: { projectId: string }) {
  const [query, setQuery] = useState("");
  const [filter, setFilter] = useState("");
  const [cursor, setCursor] = useState("");
  const [refresh, setRefresh] = useState(0);
  const [page, setPage] = useState<Schemas["NativeTicketPage"]>();
  const [error, setError] = useState("");
  const [selected, setSelected] = useState("");

  useEffect(() => {
    const abort = new AbortController();
    setPage(undefined);
    setError("");
    void apiClient.getNativeTickets(projectId, { q: filter, cursor, pageSize: 30 }, abort.signal)
      .then((value) => {
        if (!abort.signal.aborted) {
          setPage(value);
        }
      })
      .catch(() => {
        if (!abort.signal.aborted) {
          setError("Tickets could not be loaded. Your existing execution history is still available on the board.");
        }
      });
    return () => abort.abort();
  }, [projectId, filter, cursor, refresh]);

  function search(event: FormEvent) {
    event.preventDefault();
    setCursor("");
    setFilter(query.trim());
    setRefresh((value) => value + 1);
  }

  return (
    <>
      <form className="ticket-toolbar" onSubmit={search}>
        <label>Search tickets<input value={query} onChange={(event) => setQuery(event.target.value)} placeholder="Title or description" /></label>
        <button className="button" type="submit">Search</button>
        <button className="button" type="button" onClick={() => setRefresh((value) => value + 1)}>Refresh</button>
        <AppLink to="/board?create=1" className="navigation-action">Create work</AppLink>
      </form>
      {error && <AsyncPanel compact state="error" title="Tickets unavailable" message={error} />}
      {!page && !error && <AsyncPanel compact state="loading" title="Loading tickets" message="Reading the selected project source." />}
      <div className="ticket-layout">
        <section aria-label="Project tickets" className="ticket-list">
          {page?.tickets.map((ticket) => (
            <button type="button" key={ticket.id} className="ticket-list__item" aria-pressed={selected === ticket.id} onClick={() => setSelected(ticket.id)}>
              <strong>{ticket.title}</strong>
              <span>{ticket.businessState.name} · Priority {ticket.priority}</span>
            </button>
          ))}
          {page?.tickets.length === 0 && <EmptyState compact kind={filter ? "filtered" : "empty"} title="No matching tickets" message="Try a different search or create work in this project." />}
          {page && <div className="ticket-toolbar">
            {cursor && <button className="button" type="button" onClick={() => setCursor("")}>First page</button>}
            {page.nextCursor && <button className="button" type="button" onClick={() => setCursor(page.nextCursor)}>Next page</button>}
          </div>}
        </section>
        {selected && <NativeTicketSelection key={`${projectId}/${selected}`} projectId={projectId} ticketId={selected} refresh={refresh} onChanged={() => setRefresh((value) => value + 1)} />}
      </div>
    </>
  );
}

function NativeTicketSelection({ projectId, ticketId, refresh, onChanged }: { projectId: string; ticketId: string; refresh: number; onChanged(): void }) {
  const [detail, setDetail] = useState<Schemas["NativeTicketDetail"]>();
  const [pending, setPending] = useState(false);
  const [error, setError] = useState("");
  useEffect(() => {
    const abort = new AbortController();
    void apiClient.getNativeTicket(projectId, ticketId, abort.signal).then((value) => {
      if (!abort.signal.aborted) {
        setDetail(value);
        setError("");
      }
    }).catch(() => {
      if (!abort.signal.aborted) {
        setError("This ticket could not be refreshed. Reload before changing it.");
      }
    });
    return () => abort.abort();
  }, [projectId, ticketId, refresh]);

  async function mutate(action: () => Promise<Schemas["NativeTicketDetail"]>) {
    if (pending) {
      return;
    }
    setPending(true);
    setError("");
    try {
      setDetail(await action());
      onChanged();
    } catch {
      setError("The change was not confirmed. Refresh the ticket and review its current revision before trying again.");
    } finally {
      setPending(false);
    }
  }

  return <section className="ticket-detail" aria-label="Ticket details" aria-busy={pending}>
    {error && <AsyncPanel compact state="error" title="Ticket needs attention" message={error} />}
    {detail ? <TicketEditor key={detail.ticket.revision} detail={detail} pending={pending || Boolean(error)} onEdit={(fields) => void mutate(() => apiClient.editNativeTicket(projectId, ticketId, { schemaVersion: 1, revision: detail.ticket.revision, ...fields }, `ticket-edit-${crypto.randomUUID()}`))} onTransition={(transitionId) => void mutate(() => apiClient.transitionNativeTicket(projectId, ticketId, { schemaVersion: 1, revision: detail.ticket.revision, transitionId }, `ticket-state-${crypto.randomUUID()}`))} /> : <p>Loading ticket details…</p>}
  </section>;
}

export function TicketEditor({ detail, pending, onEdit, onTransition }: { detail: Schemas["NativeTicketDetail"]; pending: boolean; onEdit(fields: Pick<Schemas["NativeTicketEditRequest"], "title" | "description" | "priority">): void; onTransition(id: string): void }) {
  const [title, setTitle] = useState(detail.ticket.title);
  const [description, setDescription] = useState(detail.ticket.description);
  const [priority, setPriority] = useState(detail.ticket.priority);
  const editable = new Set(detail.fields.map((field) => field.id));
  const changed = (editable.has("title") && title !== detail.ticket.title)
    || (editable.has("description") && description !== detail.ticket.description)
    || (editable.has("priority") && priority !== detail.ticket.priority);

  function save(event: FormEvent) {
    event.preventDefault();
    onEdit({
      ...(editable.has("title") && title !== detail.ticket.title ? { title } : {}),
      ...(editable.has("description") && description !== detail.ticket.description ? { description } : {}),
      ...(editable.has("priority") && priority !== detail.ticket.priority ? { priority } : {}),
    });
  }

  return (
    <>
      <h2>{detail.ticket.title}</h2>
      <p>Ticket status: <strong>{detail.ticket.businessState.name}</strong></p>
      <form className="ticket-editor" onSubmit={save}>
        <fieldset disabled={pending || !detail.capabilities.edit}>
          <legend>Ticket content</legend>
          <label>Title<input required value={title} disabled={!editable.has("title")} onChange={(event) => setTitle(event.target.value)} /></label>
          <label>Description<textarea rows={8} value={description} disabled={!editable.has("description")} onChange={(event) => setDescription(event.target.value)} /></label>
          <label>Priority<input type="number" min={0} step={1} value={priority} disabled={!editable.has("priority")} onChange={(event) => setPriority(Number(event.target.value))} /></label>
          <button className="button" type="submit" disabled={!changed}>Save ticket</button>
        </fieldset>
      </form>
      {detail.capabilities.transitions && <div className="ticket-toolbar" aria-label="Ticket status actions">
        {detail.transitions.map((transition) => <button className="button" type="button" disabled={pending} key={transition.id} onClick={() => onTransition(transition.id)}>{transition.name}</button>)}
      </div>}
      <details className="ticket-history"><summary>Ticket history ({detail.history.length} revisions)</summary>
        {[...detail.history].reverse().map((revision) => <article key={revision.revision}>
          <strong>{revision.title}</strong>
          <p>{revision.businessState} · Priority {revision.priority} · {new Date(revision.recordedAt).toLocaleString()}</p>
          <pre>{revision.description}</pre>
        </article>)}
      </details>
    </>
  );
}
