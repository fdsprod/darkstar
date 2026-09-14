import type { components } from "../api/schema.generated";

type Schemas = components["schemas"];

export interface TrackerColumn {
  id: string;
  name: string;
  statusIds: string[];
}

export type TrackerAction = Schemas["TrackerBoardAction"];

export const UNMAPPED_COLUMN = "__unmapped__";

export function trackerTicketKey(ticket: Schemas["BacklogTicket"]) {
  return `${ticket.bindingRevision}/${ticket.ticketKey}`;
}

export function trackerColumnFor(ticket: Schemas["BacklogTicket"], columns: readonly TrackerColumn[], unknownGroupId = UNMAPPED_COLUMN) {
  if (!ticket.currentSource || ticket.businessState.state !== "known") {
    return unknownGroupId;
  }
  const stateId = ticket.businessState.value.id;
  const matches = columns.filter((column) => column.statusIds.includes(stateId));
  return matches.length === 1 ? matches[0].id : unknownGroupId;
}

export function trackerDropActions(ticket: Schemas["BacklogTicket"], column: TrackerColumn, actions: readonly TrackerAction[]) {
  if (!ticket.currentSource || ticket.freshness !== "fresh" || !["fresh", "cached"].includes(ticket.status)) {
    return [];
  }
  return actions.filter((action) => action.availability === "available" && column.statusIds.includes(action.targetStateId));
}

export function ticketExecutions(ticket: Schemas["BacklogTicket"], views: readonly Schemas["WorkSourceView"][]) {
  return views.filter((view) => view.lineage?.ticketKey === ticket.ticketKey && view.lineage.bindingRevision === ticket.bindingRevision);
}

export function executionLabel(view: Pick<Schemas["WorkSourceView"], "localActivity" | "runOutcome">) {
  if (view.localActivity !== "idle") {
    return view.localActivity.replaceAll("_", " ");
  }
  if (view.runOutcome === "completed") {
    return "Workflow complete";
  }
  return view.runOutcome === "unobserved" ? "Not started" : view.runOutcome;
}
