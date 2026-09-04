import type { DarkstarApiClient } from "../api/client";
import type { components } from "../api/schema.generated";
import { advanceEventCursor, type DomainEvent } from "./events";

type Schemas = components["schemas"];

export interface DashboardSnapshot {
  projects: Schemas["Project"][];
  workItems: Schemas["WorkItem"][];
  runs: Schemas["Run"][];
}

export type ConnectionStatus = "connecting" | "live" | "reconnecting" | "offline";
export type HydrationStatus = "loading" | "ready" | "refreshing" | "error";

export interface DashboardState {
  snapshot: DashboardSnapshot;
  cursor: number;
  recentEvents: readonly DomainEvent[];
  connection: ConnectionStatus;
  hydration: HydrationStatus;
  lastSynchronizedAt?: string;
  message?: string;
}

export type DashboardStateAction =
  | { type: "hydrate-started" }
  | { type: "hydrated"; snapshot: DashboardSnapshot; synchronizedAt: string }
  | { type: "hydrate-failed"; message: string }
  | { type: "connection"; status: ConnectionStatus; message?: string }
  | { type: "event"; event: DomainEvent }
  | { type: "reset-cursor"; cursor: number };

export const initialDashboardState: DashboardState = {
  snapshot: { projects: [], workItems: [], runs: [] },
  cursor: 0,
  recentEvents: [],
  connection: "connecting",
  hydration: "loading",
};

export function dashboardStateReducer(state: DashboardState, action: DashboardStateAction): DashboardState {
  switch (action.type) {
    case "hydrate-started":
      return { ...state, hydration: state.hydration === "loading" ? "loading" : "refreshing", message: undefined };
    case "hydrated":
      return { ...state, snapshot: action.snapshot, hydration: "ready", lastSynchronizedAt: action.synchronizedAt, message: undefined };
    case "hydrate-failed":
      return { ...state, hydration: "error", message: action.message };
    case "connection":
      return { ...state, connection: action.status, message: action.message };
    case "reset-cursor":
      return { ...state, cursor: action.cursor, recentEvents: [] };
    case "event": {
      // Events are ordered invalidation signals; only `hydrated` replaces projections.
      const advanced = advanceEventCursor(state.cursor, action.event);
      if (!advanced.accepted) return state;
      return { ...state, cursor: advanced.cursor, recentEvents: [...state.recentEvents, action.event].slice(-50) };
    }
  }
}

export interface DashboardHydrationClient {
  listProjects(signal?: AbortSignal): ReturnType<DarkstarApiClient["listProjects"]>;
  listWorkItems(projectId?: string, signal?: AbortSignal): ReturnType<DarkstarApiClient["listWorkItems"]>;
  listRuns(query?: { after?: string; limit?: number }, signal?: AbortSignal): ReturnType<DarkstarApiClient["listRuns"]>;
}

/** Reads every dashboard collection from query projections; events never fabricate resources. */
export async function hydrateDashboard(client: DashboardHydrationClient, signal?: AbortSignal): Promise<DashboardSnapshot> {
  const [projects, workItems, runs] = await Promise.all([
    client.listProjects(signal),
    client.listWorkItems(undefined, signal),
    readAllRuns(client, signal),
  ]);
  return { projects, workItems, runs };
}

async function readAllRuns(client: DashboardHydrationClient, signal?: AbortSignal): Promise<Schemas["Run"][]> {
  const values: Schemas["Run"][] = [];
  const observedCursors = new Set<string>();
  let after: string | undefined;
  do {
    const page = await client.listRuns({ limit: 200, after }, signal);
    values.push(...page.items);
    const next = page.pageInfo.nextCursor ?? undefined;
    if (next !== undefined && observedCursors.has(next)) throw new Error("The run projection returned a repeated page cursor.");
    if (next !== undefined) observedCursors.add(next);
    after = next;
  } while (after !== undefined);
  return values;
}
