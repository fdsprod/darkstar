import type { components } from "../api/schema.generated";

type Schemas = components["schemas"];

export const workContextTabs = ["overview", "runs", "evidence", "diagnostics"] as const;
export const runContextTabs = ["overview", "execution", "agents", "evidence", "activity", "diagnostics"] as const;

export type WorkContextTab = typeof workContextTabs[number];
export type RunContextTab = typeof runContextTabs[number];

export function parseWorkContextTab(value: string | null): WorkContextTab {
  return workContextTabs.includes(value as WorkContextTab) ? value as WorkContextTab : "overview";
}

export function parseRunContextTab(value: string | null): RunContextTab {
  return runContextTabs.includes(value as RunContextTab) ? value as RunContextTab : "overview";
}

export function contextLocation(path: string, current: URLSearchParams, tab: WorkContextTab | RunContextTab, updates: Record<string, string | undefined> = {}) {
  const next = new URLSearchParams(current);
  if (tab === "overview") next.delete("tab"); else next.set("tab", tab);
  for (const [name, value] of Object.entries(updates)) value ? next.set(name, value) : next.delete(name);
  return `${path}${next.size ? `?${next}` : ""}`;
}

export function latestRun(runs: readonly Schemas["Run"][]) {
  return [...runs].sort((left, right) => (right.lastGlobalPosition ?? 0) - (left.lastGlobalPosition ?? 0) || right.updatedAt.localeCompare(left.updatedAt) || right.id.localeCompare(left.id))[0];
}

export function workNextAction(run: Schemas["Run"] | undefined): { label: string; tab: WorkContextTab | RunContextTab } {
  if (!run) return { label: "Prepare the first run", tab: "runs" };
  if (["queued", "running", "waiting", "blocked", "reconcile_required"].includes(run.status)) return { label: "Continue current run", tab: "overview" };
  if (run.status === "failed") return { label: "Review failure", tab: "activity" };
  if (run.status === "completed") return { label: "Review produced evidence", tab: "evidence" };
  return { label: "Review run readiness", tab: "overview" };
}

export function runAgents<T extends { runId: string }>(agents: readonly T[], runId: string): T[] {
  return agents.filter((agent) => agent.runId === runId);
}

export function runPermissions<T extends { runId: string }>(permissions: readonly T[], runId: string): T[] {
  return permissions.filter((permission) => permission.runId === runId);
}
