import type { components } from "../api/schema.generated";

type Schemas = components["schemas"];

export type StatusTone = "neutral" | "active" | "waiting" | "success" | "danger";
export type RunEventCategory = "command" | "validation" | "commit" | "lifecycle";

export function sortNodeVisits(nodes: readonly Schemas["NodeVisit"][]) {
  return [...nodes].sort((left, right) => left.createdAt.localeCompare(right.createdAt) || left.id.localeCompare(right.id));
}

export function attemptsForVisit(attempts: readonly Schemas["Attempt"][], visitId: string) {
  return attempts
    .filter((attempt) => attempt.visitId === visitId)
    .sort((left, right) => left.createdAt.localeCompare(right.createdAt) || left.id.localeCompare(right.id));
}

export function statusTone(status: string): StatusTone {
  if (["running", "starting", "validating", "queued", "ready"].includes(status)) return "active";
  if (["waiting", "waiting_checkpoint", "blocked", "pending", "draft", "created"].includes(status)) return "waiting";
  if (["completed", "succeeded"].includes(status)) return "success";
  if (["failed", "rejected", "cancelled", "interrupted", "reconcile_required"].includes(status)) return "danger";
  return "neutral";
}

export function eventCategory(kind: string): RunEventCategory {
  const normalized = kind.toLowerCase();
  if (normalized.includes("validat") || normalized.includes("checkpoint")) return "validation";
  if (normalized.includes("commit") || normalized.includes("delivery") || normalized.includes("pull_request")) return "commit";
  if (normalized.includes("command")) return "command";
  return "lifecycle";
}

export function terminalBoundary(route: Schemas["FrozenRoute"] | undefined) {
  return route?.terminals ?? [];
}

export function shortIdentifier(value: string) {
  return value.length <= 22 ? value : `${value.slice(0, 11)}…${value.slice(-6)}`;
}

export function humanize(value: string) {
  return value.replaceAll(/[._-]+/g, " ").replace(/^./, (character) => character.toUpperCase());
}
