import type { components } from "../api/schema.generated";
import type { DashboardSnapshot } from "../state/dashboardState";

type Schemas = components["schemas"];

export const LIFECYCLE_COLUMNS = ["backlog", "ready", "running", "waiting", "blocked", "review", "failed", "done"] as const;

export type BoardLifecycle = typeof LIFECYCLE_COLUMNS[number];
export type BoardView = "all" | "attention";
export type BoardCardAction = "start" | "pause" | "resume" | "retry" | "cancel";

export interface BoardCard {
  work: Schemas["WorkItem"];
  project?: Schemas["Project"];
  run?: Schemas["Run"];
  lifecycle: BoardLifecycle;
}

export interface BoardFilters {
  projectId?: string;
  workflowId?: string;
  query?: string;
  view?: BoardView;
}

export interface CreateWorkInput {
  projectId: string;
  title: string;
  priority?: number;
}

/** Builds board cards only from query projections; this layer never predicts a transition. */
export function deriveBoardCards(snapshot: DashboardSnapshot): BoardCard[] {
  const projects = new Map(snapshot.projects.map((project) => [project.id, project]));
  const runsByWork = new Map<string, Schemas["Run"][]>();
  for (const run of snapshot.runs) {
    const existing = runsByWork.get(run.workItemId) ?? [];
    existing.push(run);
    runsByWork.set(run.workItemId, existing);
  }

  return snapshot.workItems
    .map((work) => {
      const run = newestRun(runsByWork.get(work.id) ?? []);
      return { work, project: projects.get(work.projectId), run, lifecycle: lifecycleFor(work, run) };
    })
    .sort((left, right) => {
      const priority = right.work.priority - left.work.priority;
      if (priority !== 0) return priority;
      return right.work.updatedAt.localeCompare(left.work.updatedAt) || left.work.id.localeCompare(right.work.id);
    });
}

export function filterBoardCards(cards: readonly BoardCard[], filters: BoardFilters): BoardCard[] {
  const query = filters.query?.trim().toLocaleLowerCase();
  return cards.filter((card) => {
    if (filters.projectId && card.work.projectId !== filters.projectId) return false;
    if (filters.workflowId && card.run?.workflowId !== filters.workflowId) return false;
    if (filters.view === "attention" && !(["waiting", "blocked", "review", "failed"] as BoardLifecycle[]).includes(card.lifecycle)) return false;
    if (query && !`${card.work.title} ${card.work.id} ${card.project?.name ?? ""} ${card.run?.workflowId ?? ""}`.toLocaleLowerCase().includes(query)) return false;
    return true;
  });
}

/** Mirrors the legal run-control edges exposed by the daemon and CLI. */
export function availableCardActions(card: BoardCard): BoardCardAction[] {
  const status = card.run?.status;
  if (!status) return card.work.status === "open" && card.project?.status === "active" ? ["start"] : [];
  if (status === "completed" || status === "cancelled" || status === "reconcile_required") return [];
  if (status === "queued" || status === "running") return ["pause", "cancel"];
  if (status === "waiting" || status === "blocked") return ["resume", "cancel"];
  if (status === "failed") return ["retry", "cancel"];
  if (status === "pending" || status === "draft" || status === "ready") return ["cancel"];
  return [];
}

export function buildCreateWorkItemRequest(input: CreateWorkInput): Schemas["CreateWorkItemRequest"] {
  const title = input.title.trim();
  const projectId = input.projectId.trim();
  const priority = input.priority ?? 0;
  if (!projectId) throw new Error("Choose a project.");
  if (!title) throw new Error("Describe the requested outcome.");
  if (!Number.isSafeInteger(priority) || priority < 0) throw new Error("Priority must be a whole number of zero or greater.");
  return { projectId, title, priority };
}

function newestRun(runs: readonly Schemas["Run"][]) {
  return [...runs].sort((left, right) => {
    const position = (right.lastGlobalPosition ?? 0) - (left.lastGlobalPosition ?? 0);
    if (position !== 0) return position;
    return right.updatedAt.localeCompare(left.updatedAt) || right.id.localeCompare(left.id);
  })[0];
}

function lifecycleFor(work: Schemas["WorkItem"], run?: Schemas["Run"]): BoardLifecycle {
  if (work.status === "completed" || work.status === "cancelled") return "done";
  if (!run) return "backlog";
  switch (run.status) {
    case "pending":
    case "draft": return "backlog";
    case "ready":
    case "queued": return "ready";
    case "running": return "running";
    case "waiting": return "waiting";
    case "blocked":
    case "reconcile_required": return "blocked";
    case "failed": return "failed";
    case "completed":
    case "cancelled": return "done";
  }
}
