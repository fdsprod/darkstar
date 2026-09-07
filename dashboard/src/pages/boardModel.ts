import type { components } from "../api/schema.generated";
import type { DashboardSnapshot } from "../state/dashboardState";

type Schemas = components["schemas"];

export const LIFECYCLE_COLUMNS = ["backlog", "ready", "running", "waiting", "blocked", "review", "failed", "done"] as const;

export type BoardLifecycle = typeof LIFECYCLE_COLUMNS[number];
export type BoardView = "all" | "attention";
export type BoardCardAction = "prepare" | "launch" | "pause" | "resume" | "retry" | "cancel";
export type WorkTransitionSource = "drag" | "keyboard" | "menu";

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
  details?: string;
  evidence?: readonly string[];
  routingIntent?: Schemas["WorkRoutingIntent"];
  priority?: number;
}

export interface PrepareRunInput {
  workItemId: string;
  workflowId: string;
  workflowVersion: string;
  profile?: string;
}

export interface WorkflowProfileOption {
  id: string;
  description: string;
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

/** Converts the server decision table into presentation actions without a client legality matrix. */
export function availableCardActions(card: BoardCard, plan?: Schemas["WorkTransitionPlan"]): BoardCardAction[] {
  if (!plan) return [];
  const actions: BoardCardAction[] = [];
  const ready = transitionDecision(plan, "ready");
  if (plan.state === "backlog" && (ready?.availability === "enabled" || (ready?.disabledReasons.length === 1 && ready.disabledReasons[0] === "preparation_required"))) actions.push("prepare");
  if (transitionDecision(plan, "running")?.availability === "enabled") {
    if (plan.state === "ready") actions.push("launch");
    else if (plan.state === "failed") actions.push("retry");
    else actions.push("resume");
  }
  if (transitionDecision(plan, "waiting")?.availability === "enabled") actions.push("pause");
  if (transitionDecision(plan, "done")?.availability === "enabled") actions.push("cancel");
  return actions;
}

export function transitionDecision(plan: Schemas["WorkTransitionPlan"], target: Schemas["WorkLifecycleState"]) {
  return plan.targets.find((decision) => decision.target === target);
}

export function transitionTargetForAction(action: BoardCardAction): Schemas["WorkLifecycleState"] {
  if (action === "prepare") return "ready";
  if (action === "pause") return "waiting";
  if (action === "cancel") return "done";
  return "running";
}

/** Drag, keyboard, and menu input deliberately produce the identical API command body. */
export function buildWorkTransitionRequest(_source: WorkTransitionSource, target: Schemas["WorkLifecycleState"], preparation?: Schemas["WorkTransitionPreparation"]): Schemas["WorkTransitionApplyRequest"] {
  if (target === "ready") {
    return preparation ? { target, preparation } : { target };
  }
  if (target === "done") return { target, confirmation: "confirmed" };
  return { target };
}

export function buildCreateWorkItemRequest(input: CreateWorkInput): Schemas["CreateWorkItemRequest"] {
  const title = input.title.trim();
  const projectId = input.projectId.trim();
  const details = input.details?.trim();
  const evidence = [...new Set((input.evidence ?? []).map((value) => value.trim()).filter(Boolean))];
  const priority = input.priority ?? 0;
  if (!projectId) throw new Error("Choose a project.");
  if (!title) throw new Error("Describe the requested outcome.");
  if (!Number.isSafeInteger(priority) || priority < 0) throw new Error("Priority must be a whole number of zero or greater.");
  const routingIntent = normalizeRoutingIntent(input.routingIntent ?? { mode: "automatic" });
  return { projectId, title, ...(details ? { details } : {}), ...(evidence.length ? { evidence } : {}), routingIntent, ...(priority ? { priority } : {}) };
}

function normalizeRoutingIntent(intent: Schemas["WorkRoutingIntent"]): Schemas["WorkRoutingIntent"] {
  if (intent.mode === "automatic") return { mode: "automatic" };
  const workflowId = intent.workflowId.trim();
  const workflowVersion = intent.workflowVersion?.trim();
  const entryNodeId = intent.entryNodeId?.trim();
  const terminalNodeIds = [...new Set((intent.terminalNodeIds ?? []).map((value) => value.trim()).filter(Boolean))];
  if (!workflowId) throw new Error("Choose a workflow for the routing override.");
  return { mode: "override", workflowId, ...(workflowVersion ? { workflowVersion } : {}), ...(entryNodeId ? { entryNodeId } : {}), ...(terminalNodeIds.length ? { terminalNodeIds } : {}) };
}

export function buildPrepareRunRequest(input: PrepareRunInput): Schemas["CreateRunRequest"] {
  const workItemId = input.workItemId.trim();
  const workflowId = input.workflowId.trim();
  const workflowVersion = input.workflowVersion.trim();
  const profile = input.profile?.trim();
  if (!workItemId) throw new Error("Choose a work item.");
  if (!workflowId || !workflowVersion) throw new Error("Choose a workflow version.");
  return { workItemId, workflowId, workflowVersion, ...(profile ? { profile } : {}) };
}

export function workflowProfiles(definition: Schemas["WorkflowDefinition"]): WorkflowProfileOption[] {
  if (definition.document.apiVersion !== "darkstar.local/v1alpha2") return [];
  return Object.entries(definition.document.spec.profiles ?? {})
    .map(([id, profile]) => ({ id, description: profile.description }))
    .sort((left, right) => left.id.localeCompare(right.id));
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
