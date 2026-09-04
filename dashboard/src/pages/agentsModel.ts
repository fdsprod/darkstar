export const AGENT_LOG_WINDOW_BYTES = 256 * 1024;
export const AGENT_LOG_PAGE_BYTES = 64 * 1024;

export type AgentLifecycle = "created" | "starting" | "running" | "validating" | "succeeded" | "failed" | "cancelled" | "interrupted" | "reconcile_required";

export interface AgentSummaryLike {
  attemptId: string;
  status: AgentLifecycle;
  allowedActions: readonly string[];
  resourceVersion: number;
  updatedAt: string;
  logReference?: string;
}

export interface AgentGroups<T extends AgentSummaryLike> {
  queued: T[];
  active: T[];
}

export interface AgentLogPage {
  offset: number;
  nextOffset: number;
  size: number;
  complete: boolean;
  bytes: Uint8Array;
}

export interface AgentLogWindow extends AgentLogPage {
  omittedBefore: boolean;
}

export type ProviderPermissionAction = "allow_once" | "deny" | "cancel" | "retry_delivery";

export interface ProviderPermissionLike {
  id: string;
  providerTurnId: string;
  scope: { target: string; operation: string; subject: string };
  scopeDigest: string;
  policyDigest: string;
  status: "pending" | "decision_recorded" | "responded";
  allowedActions: readonly ProviderPermissionAction[];
  resourceVersion: number;
  updatedAt: string;
}

export function permissionActionPresentation(action: ProviderPermissionAction) {
  switch (action) {
    case "allow_once": return { label: "Allow once", description: "Authorize only this recorded provider interaction.", tone: "primary" as const };
    case "deny": return { label: "Deny", description: "Refuse this provider interaction without approving another scope.", tone: "danger" as const };
    case "cancel": return { label: "Cancel request", description: "Cancel this provider interaction request; this is not run cancellation.", tone: "danger" as const };
    case "retry_delivery": return { label: "Retry delivery", description: "Ask the daemon to redeliver the already recorded decision.", tone: "neutral" as const };
  }
}

export function buildProviderPermissionDecision(permission: ProviderPermissionLike, action: Exclude<ProviderPermissionAction, "retry_delivery">) {
  if (permission.status !== "pending" || !permission.allowedActions.includes(action)) throw new Error("This provider permission action is not currently allowed.");
  return { decision: action, scopeDigest: permission.scopeDigest };
}

export function providerPermissionChanged(previous: ProviderPermissionLike, next: ProviderPermissionLike): boolean {
  return previous.id !== next.id
    || previous.providerTurnId !== next.providerTurnId
    || previous.scope.target !== next.scope.target
    || previous.scope.operation !== next.scope.operation
    || previous.scope.subject !== next.scope.subject
    || previous.scopeDigest !== next.scopeDigest
    || previous.policyDigest !== next.policyDigest
    || previous.status !== next.status
    || previous.resourceVersion !== next.resourceVersion
    || previous.updatedAt !== next.updatedAt
    || previous.allowedActions.join("\u0000") !== next.allowedActions.join("\u0000");
}

/** Preserves the server's queue order while separating waiting and executing attempts. */
export function groupAgents<T extends AgentSummaryLike>(items: readonly T[]): AgentGroups<T> {
  return {
    queued: items.filter((item) => item.status === "created"),
    active: items.filter((item) => item.status === "starting" || item.status === "running" || item.status === "validating"),
  };
}

/** Cancellation authority is never inferred from lifecycle state. */
export function canCancelAgent(agent: AgentSummaryLike): boolean {
  return agent.allowedActions.includes("cancel");
}

export function agentChanged(previous: AgentSummaryLike, next: AgentSummaryLike): boolean {
  return previous.attemptId !== next.attemptId
    || previous.resourceVersion !== next.resourceVersion
    || previous.status !== next.status
    || previous.updatedAt !== next.updatedAt
    || previous.logReference !== next.logReference
    || previous.allowedActions.join("\u0000") !== next.allowedActions.join("\u0000");
}

export function formatElapsed(milliseconds: number): string {
  if (!Number.isFinite(milliseconds) || milliseconds < 0) return "Unavailable";
  const seconds = Math.floor(milliseconds / 1000);
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m ${seconds % 60}s`;
  const hours = Math.floor(minutes / 60);
  return `${hours}h ${minutes % 60}m`;
}

export function logTailOffset(size: number, limit = AGENT_LOG_WINDOW_BYTES): number {
  if (!Number.isSafeInteger(size) || size < 0) throw new Error("Log size is invalid.");
  return Math.max(0, size - limit);
}

export function mergeAgentLogWindow(current: AgentLogWindow | undefined, page: AgentLogPage, limit = AGENT_LOG_WINDOW_BYTES): AgentLogWindow {
  validateLogPage(page);
  if (!Number.isSafeInteger(limit) || limit < 1) throw new Error("Log window limit is invalid.");
  let offset = page.offset;
  let bytes = page.bytes;

  if (current && page.offset >= current.offset && page.offset <= current.nextOffset && page.size >= current.nextOffset) {
    const overlap = current.nextOffset - page.offset;
    const suffix = overlap >= page.bytes.length ? new Uint8Array() : page.bytes.slice(overlap);
    bytes = concatBytes(current.bytes, suffix);
    offset = current.offset;
  }

  if (bytes.length > limit) {
    const trim = bytes.length - limit;
    bytes = bytes.slice(trim);
    offset += trim;
  }
  return { ...page, offset, bytes, omittedBefore: offset > 0 };
}

export function decodeAgentLog(window: AgentLogWindow | undefined): string {
  return window ? new TextDecoder("utf-8", { fatal: false }).decode(window.bytes) : "";
}

function validateLogPage(page: AgentLogPage) {
  for (const [name, value] of [["offset", page.offset], ["next offset", page.nextOffset], ["size", page.size]] as const) {
    if (!Number.isSafeInteger(value) || value < 0) throw new Error(`Log ${name} is invalid.`);
  }
  if (page.nextOffset !== page.offset + page.bytes.length || page.nextOffset > page.size) throw new Error("Log cursor metadata does not match the returned bytes.");
}

function concatBytes(left: Uint8Array, right: Uint8Array) {
  const value = new Uint8Array(left.length + right.length);
  value.set(left);
  value.set(right, left.length);
  return value;
}
