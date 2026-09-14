import { useState } from "react";

import type { components } from "../api/schema.generated";
import { executionLabel, ticketExecutions, trackerColumnFor, trackerDropActions, trackerTicketKey, UNMAPPED_COLUMN, type TrackerAction, type TrackerColumn } from "./trackerBoardModel";

type Schemas = components["schemas"];

export function TrackerBoard({ tickets, columns, unknownGroup, executions, actions, pending, selectedKey, onSelect, onTransition }: {
  tickets: Schemas["BacklogTicket"][];
  columns: TrackerColumn[];
  unknownGroup?: TrackerColumn;
  executions: Schemas["WorkSourceView"][];
  actions: Record<string, TrackerAction[]>;
  pending: boolean;
  selectedKey: string;
  onSelect(key: string): void;
  onTransition(ticket: Schemas["BacklogTicket"], action: TrackerAction): void;
}) {
  const [draggedKey, setDraggedKey] = useState("");
  const [keyboardColumn, setKeyboardColumn] = useState<number>();
  const unmapped = unknownGroup ?? { id: UNMAPPED_COLUMN, name: "Unmapped / previous source", statusIds: [] };
  const displayed = [...columns, unmapped];
  const dragged = tickets.find((ticket) => trackerTicketKey(ticket) === draggedKey);

  function requestDrop(ticket: Schemas["BacklogTicket"], column: TrackerColumn) {
    const matches = trackerDropActions(ticket, column, actions[trackerTicketKey(ticket)] ?? []);
    if (matches.length === 1 && !pending) {
      onTransition(ticket, matches[0]);
      setDraggedKey("");
      setKeyboardColumn(undefined);
    }
  }

  return <>
    <p id="tracker-board-help">Move a card to request its source tracker transition. Space picks up a focused card, arrow keys select a column, Enter requests the move, and Escape cancels. If a column has several legal transitions, use the named action on the card.</p>
    <section className="tracker-board" aria-label="Tracker board" aria-busy={pending}>
      {displayed.map((column) => {
        const items = tickets.filter((ticket) => trackerColumnFor(ticket, columns, unmapped.id) === column.id);
        const drops = dragged ? trackerDropActions(dragged, column, actions[draggedKey] ?? []) : [];
        return <article key={column.id} className="board-column" data-tracker-column={column.id} data-drop-available={Boolean(dragged && drops.length === 1 && !pending)}
          onDragOver={(event) => {
            if (drops.length === 1 && !pending) {
              event.preventDefault();
            }
          }}
          onDrop={(event) => {
            event.preventDefault();
            if (dragged) {
              requestDrop(dragged, column);
            }
          }}>
          <header className="board-column__header"><h2>{column.name}</h2><span>{items.length}</span></header>
          {dragged && <p className="board-drop-hint">{drops.length === 1 ? `Request ${drops[0].name}. ${drops[0].automation.join(" ")}` : drops.length > 1 ? "Choose a named source action on the card." : "No available source transition to this column."}</p>}
          <div className="board-column__cards">
            {items.map((ticket) => {
              const key = trackerTicketKey(ticket);
              const activity = ticketExecutions(ticket, executions);
              return <article key={key} className="work-card" draggable tabIndex={0} aria-label={`${ticket.title}, tracker status ${ticket.businessState.state === "known" ? ticket.businessState.value.name : ticket.businessState.state}`} aria-describedby="tracker-board-help"
                onDragStart={(event) => {
                  event.dataTransfer.setData("text/plain", key);
                  event.dataTransfer.effectAllowed = "move";
                  setDraggedKey(key);
                }}
                onDragEnd={() => setDraggedKey("")}
                onKeyDown={(event) => {
                  if (event.target !== event.currentTarget) {
                    return;
                  }
                  if (event.key === " ") {
                    event.preventDefault();
                    setDraggedKey(key);
                    setKeyboardColumn(displayed.findIndex((item) => item.id === column.id));
                  } else if (draggedKey === key && keyboardColumn !== undefined && ["ArrowLeft", "ArrowRight"].includes(event.key)) {
                    event.preventDefault();
                    setKeyboardColumn(Math.max(0, Math.min(displayed.length - 1, keyboardColumn + (event.key === "ArrowRight" ? 1 : -1))));
                  } else if (event.key === "Enter" && keyboardColumn !== undefined && draggedKey === key) {
                    event.preventDefault();
                    requestDrop(ticket, displayed[keyboardColumn]);
                  } else if (event.key === "Escape") {
                    setDraggedKey("");
                    setKeyboardColumn(undefined);
                  }
                }}
                onBlur={(event) => {
                  if (!event.currentTarget.contains(event.relatedTarget)) {
                    setDraggedKey("");
                    setKeyboardColumn(undefined);
                  }
                }}>
                <button type="button" className="work-card__title" aria-pressed={key === selectedKey} onClick={() => onSelect(key)}>{ticket.title}</button>
                <p>Tracker: {ticket.businessState.state === "known" ? ticket.businessState.value.name || ticket.businessState.value.id : ticket.businessState.state}</p>
                <p>{ticket.ref.namespace.provider} · {ticket.url && /^https?:\/\//i.test(ticket.url) ? <a href={ticket.url} target="_blank" rel="noreferrer">{ticket.key || ticket.ref.id}</a> : ticket.key || ticket.ref.id}</p>
                <p>{ticket.freshness.replaceAll("_", " ")} · {ticket.status.replaceAll("_", " ")}{ticket.checkedAt && ` · Checked ${new Date(ticket.checkedAt).toLocaleString()}`}</p>
                {ticket.reason && <p>{ticket.reason}</p>}
                {!ticket.currentSource && <p>Previous source · revision {ticket.bindingRevision}</p>}
                {activity.length === 0 && <p>DARKSTAR: Not started</p>}
                {activity.map((execution) => <div key={execution.workItemId} className="tracker-execution">
                  <strong>DARKSTAR: {executionLabel(execution)}</strong>
                  <p>External acceptance: {execution.externalAcceptance.state === "known" ? execution.externalAcceptance.value : execution.externalAcceptance.state}</p>
                  <a href={`/work/${encodeURIComponent(execution.workItemId)}`}>Execution controls, artifacts and review</a>
                  {execution.runs.map((run) => <a key={run.id} href={`/work/${encodeURIComponent(execution.workItemId)}/run/${encodeURIComponent(run.id)}?tab=activity`}>{run.workflowId} · History and logs</a>)}
                </div>)}
                <div className="tracker-actions" aria-label="Source tracker actions">
                  {(actions[key] ?? []).map((action) => <div key={action.id}>
                    <button className="button" type="button" disabled={pending || action.availability !== "available" || trackerDropActions(ticket, { id: "action", name: "", statusIds: [action.targetStateId] }, [action]).length === 0} onClick={() => onTransition(ticket, action)}>{action.name}</button>
                    {action.reason && <p>{action.reason}</p>}
                    {action.automation.length > 0 && <p>Configured automation: {action.automation.join(" · ")}</p>}
                  </div>)}
                </div>
                {draggedKey === key && keyboardColumn !== undefined && <p role="status">Target: {displayed[keyboardColumn].name}. {trackerDropActions(ticket, displayed[keyboardColumn], actions[key] ?? []).length === 1 ? "Enter requests this transition." : "No unique source transition. Choose a named action."}</p>}
              </article>;
            })}
            {items.length === 0 && <p className="board-column__empty">No tickets</p>}
          </div>
        </article>;
      })}
    </section>
  </>;
}
