import { useEffect, useRef, useState, type FormEvent, type ReactNode } from "react";

import { apiClient } from "../api/client";
import type { components } from "../api/schema.generated";
import { AsyncPanel, EmptyState } from "../components/InteractionPatterns";
import { TrackerSourcePicker, type SourceSelection } from "./TrackerSourcePicker";
import { TicketExecutionAdmission } from "./WorkSourcePanel";
import { AppLink } from "../app/router";
import { trackerMappingApi, type TrackerBoardView } from "../api/trackerMapping";
import { TrackerBoard } from "./TrackerBoard";
import { TrackerMappingSettings } from "./TrackerMappingSettings";
import { trackerTicketKey, type TrackerAction } from "./trackerBoardModel";

type Schemas = components["schemas"];

export function BacklogBrowser({ projectId, renderNativeDetail }: {
  projectId: string;
  renderNativeDetail(ticketId: string, refresh: number, onChanged: () => void): ReactNode;
}) {
  const [view, setView] = useState<Schemas["BacklogView"]>();
  const [cursor, setCursor] = useState("");
  const [includePrevious, setIncludePrevious] = useState(false);
  const [onlyReturned, setOnlyReturned] = useState(false);
  const [text, setText] = useState("");
  const [settings, setSettings] = useState(false);
  const [selectedKey, setSelectedKey] = useState("");
  const [pending, setPending] = useState(false);
  const [error, setError] = useState("");
  const [reload, setReload] = useState(0);
  const initializedQuery = useRef(false);
  const [presentation, setPresentation] = useState<"list" | "board">("list");
  const [mappingSettings, setMappingSettings] = useState(false);
  const [board, setBoard] = useState<TrackerBoardView>();
  const [executions, setExecutions] = useState<Schemas["WorkSourceView"][]>([]);
  const [boardError, setBoardError] = useState("");

  useEffect(() => {
    if (presentation !== "board") {
      return;
    }
    const abort = new AbortController();
    void Promise.all([trackerMappingApi.board(projectId, abort.signal), apiClient.listWorkSourceViews({ projectId }, abort.signal)]).then(([projection, activity]) => {
      if (!abort.signal.aborted) {
        setBoard(projection);
        setExecutions(activity.items);
        setBoardError("");
      }
    }).catch(() => {
      if (!abort.signal.aborted) {
        setBoardError("Board configuration or execution activity could not be refreshed. Source actions are unavailable until reload succeeds.");
      }
    });
    return () => abort.abort();
  }, [projectId, presentation, view, reload]);

  async function transitionTicket(ticket: Schemas["BacklogTicket"], action: TrackerAction) {
    if (!view || !board || pending || boardError || action.availability !== "available") {
      return;
    }
    const consequences = action.automation.length > 0 ? ` Configured automation: ${action.automation.join(" ")}` : "";
    if (!window.confirm(`Request “${action.name}” for ${ticket.key || ticket.title}?${consequences}`)) {
      return;
    }
    setPending(true);
    setError("");
    try {
      await trackerMappingApi.transition(projectId, ticket.observationId, view.binding.revision, board.activeRevision, action.id);
      setReload((value) => value + 1);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "Source transition was not confirmed. Reload before retrying.");
      setBoardError("Reload the board before requesting another source transition.");
      setReload((value) => value + 1);
    } finally {
      setPending(false);
    }
  }

  useEffect(() => {
    const abort = new AbortController();
    async function read() {
      try {
        const value = await apiClient.operation("getProjectBacklog", { path: { projectId }, query: { cursor, limit: 50, includePrevious }, signal: abort.signal });
        if (!abort.signal.aborted) {
          setView(value);
          if (!initializedQuery.current) {
            initializedQuery.current = true;
            setText(value.query.text);
          }
        }
      } catch {
        if (!abort.signal.aborted) {
          setError("The backlog could not be loaded. Retained content remains visible; reload to check the current source.");
        }
      }
    }
    void read();
    const timer = window.setInterval(() => void read(), 15000);
    return () => {
      abort.abort();
      window.clearInterval(timer);
    };
  }, [projectId, cursor, includePrevious, reload]);

  async function refreshSource(event?: FormEvent) {
    event?.preventDefault();
    if (!view || pending) {
      return;
    }
    setPending(true);
    setError("");
    try {
      await apiClient.operation("refreshProjectBacklog", {
        path: { projectId },
        body: { schemaVersion: 1, expectedBindingRevision: view.binding.revision, query: { ...view.query, text: text.trim(), pageSize: 50 } },
      });
      setCursor("");
      setReload((value) => value + 1);
    } catch {
      setError("Refresh was not confirmed. Reload the backlog and check its source revision and connection access before retrying.");
      setReload((value) => value + 1);
    } finally {
      setPending(false);
    }
  }

  async function selectSource(selection: SourceSelection) {
    if (!view || pending) {
      return;
    }
    setPending(true);
    setError("");
    const source = selection.kind === "built_in" ? { kind: "built_in" as const } : {
      kind: "external" as const, connectionId: selection.connectionId, connectionRevision: selection.connectionRevision,
      scope: {
        namespace: { provider: selection.destination.provider, host: selection.destination.host, tenantId: selection.destination.tenantId, scopeId: selection.destination.scopeId },
        containerId: selection.destination.containerId,
      },
    };
    try {
      await apiClient.operation("selectProjectBacklogSource", { path: { projectId }, body: { schemaVersion: 1, expectedRevision: view.binding.revision, source } });
      setCursor("");
      setSelectedKey("");
      setText("");
      initializedQuery.current = false;
      setSettings(false);
      setView(undefined);
      setReload((value) => value + 1);
    } catch {
      setError("The source change was not confirmed. Reload to review the current selection before retrying.");
    } finally {
      setPending(false);
    }
  }

  async function refreshTicket(ticket: Schemas["BacklogTicket"]) {
    if (!view || pending || !ticket.currentSource) {
      return;
    }
    setPending(true);
    setError("");
    try {
      await apiClient.operation("refreshProjectBacklogTicket", { path: { projectId }, body: { schemaVersion: 1, expectedBindingRevision: view.binding.revision, ref: ticket.ref } });
      setReload((value) => value + 1);
    } catch {
      setError("This ticket could not be checked. Its last retained observation is still shown.");
      setReload((value) => value + 1);
    } finally {
      setPending(false);
    }
  }

  const selected = view?.tickets.find((ticket) => `${ticket.bindingRevision}/${ticket.ticketKey}` === selectedKey);
  const visible = view?.tickets.filter((ticket) => !onlyReturned || ticket.currentQueryMatch);
  return <section aria-label="Configured project backlog">
    <div className="ticket-toolbar">
      <button className="button" type="button" aria-pressed={presentation === "list"} onClick={() => setPresentation("list")}>Ticket list</button>
      <button className="button" type="button" aria-pressed={presentation === "board"} onClick={() => setPresentation("board")}>Tracker board</button>
      <button className="button" type="button" aria-pressed={mappingSettings} onClick={() => setMappingSettings((value) => !value)}>Workflow mappings</button>
      <strong>{view ? sourceLabel(view.binding.source) : "Project source"}</strong>
      {view && <span>Source revision {view.binding.revision}</span>}
      <button className="button" type="button" disabled={pending || !view} onClick={() => setSettings((value) => !value)}>{settings ? "Close source settings" : "Change source"}</button>
      <button className="button" type="button" disabled={pending} onClick={() => {
        setError("");
        setReload((value) => value + 1);
      }}>Reload backlog</button>
    </div>
    {settings && view && <TrackerSourcePicker key={view.binding.revision} pending={pending} onSelect={selectSource} />}
    {mappingSettings && view && <TrackerMappingSettings projectId={projectId} bindingRevision={view.binding.revision} tickets={view.tickets} onChanged={() => setReload((value) => value + 1)} />}
    {error && <AsyncPanel compact state="error" title="Backlog needs attention" message={error} />}
    {!view && !error && <AsyncPanel compact state="loading" title="Loading backlog" message="Reading retained source observations." />}
    {view && <>
      {view.binding.source.kind === "built_in" ? <AppLink to={`/board?create=1&project=${encodeURIComponent(projectId)}`} className="navigation-action">Create ticket and work</AppLink> : <p>Create ticket is unavailable from this source browser. Create it in the selected tracker, then refresh this backlog. Approved story drafts publish through the selected destination writer.</p>}
      <form className="ticket-toolbar" onSubmit={(event) => void refreshSource(event)}>
        <label>Refresh filter<input value={text} onChange={(event) => setText(event.target.value)} placeholder="Title or description" disabled={pending} /></label>
        <button className="button button--primary" type="submit" disabled={pending}>{pending ? "Checking source…" : view.refresh?.phase === "refreshing" ? "Continue refresh" : "Refresh source"}</button>
      </form>
      <BacklogRefreshStatus refresh={view.refresh} />
      <div className="ticket-toolbar">
        <label><input type="checkbox" checked={onlyReturned} onChange={(event) => setOnlyReturned(event.target.checked)} /> Only tickets returned by this refresh</label>
        <label><input type="checkbox" checked={includePrevious} onChange={(event) => {
          setIncludePrevious(event.target.checked);
          setCursor("");
          setSelectedKey("");
        }} /> Include previous sources</label>
      </div>
      {presentation === "board" && <>
        {boardError && <AsyncPanel compact state="error" title="Tracker board needs attention" message={boardError} />}
        {board?.reason && <p role="status">{board.reason}</p>}
        <TrackerBoard tickets={visible ?? []} columns={board?.columns ?? []} unknownGroup={board?.unknownGroup} executions={executions} actions={boardError ? {} : Object.fromEntries((view.tickets ?? []).map((ticket) => [trackerTicketKey(ticket), board?.actions[ticket.observationId] ?? []]))} pending={pending} selectedKey={selectedKey} onSelect={setSelectedKey} onTransition={(ticket, action) => void transitionTicket(ticket, action)} />
        {view.nextCursor && <p>This board shows one retained page. Use Next page to inspect additional tickets.</p>}
      </>}
      <div className="ticket-layout">
        <section aria-label="Retained backlog tickets" className="ticket-list">
          {presentation === "list" && visible?.map((ticket) => <button type="button" key={`${ticket.bindingRevision}/${ticket.ticketKey}`} className="ticket-list__item" aria-pressed={selectedKey === `${ticket.bindingRevision}/${ticket.ticketKey}`} onClick={() => setSelectedKey(`${ticket.bindingRevision}/${ticket.ticketKey}`)}>
            <strong>{ticket.title}</strong>
            <span>{namedObservation(ticket.businessState)} · {statusLabel(ticket.status)}</span>
            <span>{ticket.ref.namespace.provider} · {ticket.key || ticket.ref.id}</span>
            {!ticket.currentSource && <span>Previous source · revision {ticket.bindingRevision}</span>}
            {ticket.currentSource && !ticket.currentQueryMatch && <span>Earlier observation · not returned by this refresh</span>}
          </button>)}
          {visible?.length === 0 && <EmptyState compact kind="empty" title="No retained tickets in this view" message="Refresh the source to load tickets, or adjust the view filters. Loading tickets does not start work." />}
          <div className="ticket-toolbar">
            {cursor && <button className="button" type="button" onClick={() => setCursor("")}>First page</button>}
            {view.nextCursor && <button className="button" type="button" onClick={() => setCursor(view.nextCursor)}>Next page</button>}
          </div>
        </section>
        {selected && <section className="ticket-detail" aria-label="Source ticket observation">
          <TicketObservation ticket={selected} />
          <TicketExecutionAdmission key={`${projectId}/${selected.bindingRevision}/${selected.ticketKey}`} projectId={projectId} ticket={selected} bindingRevision={view.binding.revision} />
          {selected.currentSource && <button className="button" type="button" disabled={pending} onClick={() => void refreshTicket(selected)}>Check this ticket</button>}
          {selected.ref.namespace.provider === "built_in" && renderNativeDetail(selected.ref.id, reload, () => {
            if (selected.currentSource) {
              void refreshTicket(selected);
            } else {
              setReload((value) => value + 1);
            }
          })}
        </section>}
      </div>
    </>}
  </section>;
}

function BacklogRefreshStatus({ refresh }: { refresh: Schemas["BacklogView"]["refresh"] }) {
  if (!refresh) {
    return <p>No source refresh has completed yet.</p>;
  }
  return <div className="ticket-refresh-status" role="status">
    <p>{refresh.phase === "complete" ? "Refresh complete." : refresh.phase === "refreshing" ? "Refresh is incomplete. More source pages remain." : "Source refresh needs attention. Retained observations remain available."}</p>
    {refresh.lastSuccessAt && <p>Last successful refresh: {new Date(refresh.lastSuccessAt).toLocaleString()}</p>}
    {refresh.error && <p>{refresh.error.message}</p>}
    {refresh.nextAttemptAt && refresh.phase !== "complete" && <p>Next check: {new Date(refresh.nextAttemptAt).toLocaleString()}</p>}
  </div>;
}

function TicketObservation({ ticket }: { ticket: Schemas["BacklogTicket"] }) {
  return <>
    <h2>{ticket.title}</h2>
    <p>Tracker status: {namedObservation(ticket.businessState)} · Observation: {statusLabel(ticket.status)}</p>
    {ticket.reason && <p>{ticket.reason}</p>}
    {ticket.url && /^https?:\/\//i.test(ticket.url) && <a href={ticket.url} target="_blank" rel="noreferrer">Open source ticket</a>}
    <div className="ticket-description">{ticket.description}</div>
    <dl>
      <dt>Priority</dt><dd>{namedObservation(ticket.priority)}</dd>
      <dt>Assignees</dt><dd>{ticket.assignees.state === "known" ? ticket.assignees.value.map((item) => item.name).join(", ") || "None" : ticket.assignees.state}</dd>
      <dt>Labels</dt><dd>{ticket.labels.state === "known" ? ticket.labels.value.map((item) => item.name).join(", ") || "None" : ticket.labels.state}</dd>
      <dt>Observed</dt><dd>{ticket.observedAt ? new Date(ticket.observedAt).toLocaleString() : "Not recorded"}</dd>
      <dt>Last checked</dt><dd>{ticket.checkedAt ? new Date(ticket.checkedAt).toLocaleString() : "Not recorded"}</dd>
    </dl>
    <details><summary>Observation provenance</summary><dl>
      <dt>Source revision</dt><dd>{ticket.bindingRevision}</dd>
      <dt>Ticket revision</dt><dd>{ticket.revision}</dd>
      <dt>Retained observation</dt><dd>{ticket.observationId}</dd>
      <dt>Original evidence</dt><dd>{ticket.evidenceRef}</dd>
    </dl></details>
  </>;
}

function namedObservation(value: Schemas["BacklogNamedObservation"]) {
  return value.state === "known" ? value.value.name || value.value.id : value.state === "unknown" ? "Unknown" : "Unsupported";
}

function statusLabel(value: string) {
  return value.replaceAll("_", " ");
}

function sourceLabel(source: Schemas["BacklogBinding"]["source"]) {
  if (source.kind === "built_in") {
    return "Built-in tickets";
  }
  return `${source.scope.namespace.provider === "linear" ? "Linear" : "GitHub Issues"} · ${source.connectionId} (${source.connectionRevision})`;
}
