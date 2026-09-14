import { apiClient } from "./client";
import type { components } from "./schema.generated";
import type { TrackerAction, TrackerColumn } from "../pages/trackerBoardModel";

export interface WorkflowPin {
  id: string;
  version: string;
  digest: string;
}
export interface Conditions {
  fields: { fieldId: string; values: string[] }[];
  sprintIds?: string[];
}
export type IntakeAction = { kind: "noop" } | {
  kind: "admit";
  workflow: WorkflowPin;
  readinessPolicy: string;
  mode: "manual" | "automatic";
  repair: { maxAdmissions: number };
};
export type OutboundAction = { kind: "noop" } | { kind: "report"; body: string } | {
  kind: "transition";
  transitionId: string;
  fields: Record<string, { kind: "text"; value: string } | { kind: "number"; value: number } | { kind: "ids"; value: string[] }>;
};
export interface Milestone {
  id: string;
  workflow: WorkflowPin;
  evidenceTypes: string[];
}
export interface TrackerRules {
  version: "darkstar.tracker-rules/v1alpha1";
  id: string;
  revision: number;
  scope: { projectId: string; bindingRevision: number; pin: components["schemas"]["FrozenSourcePin"]; source: components["schemas"]["FrozenSourceScope"] };
  intake: { id: string; when: Conditions; action: IntakeAction }[];
  outbound: { id: string; when: Conditions; milestone: Milestone; action: OutboundAction }[];
  display: { groups: { id: string; name: string; stateIds: string[] }[]; unknownGroup: { id: string; name: string; stateIds: string[] } };
}
export interface MappingRevision {
  revision: number;
  bindingRevision: number;
  rules: TrackerRules;
  createdAt: string;
}
export interface MappingState {
  schemaVersion: 1;
  revisions: MappingRevision[];
  activeRevision: number;
}
export interface MappingDiscovery {
  schemaVersion: 1;
  bindingRevision: number;
  template: TrackerRules;
  fields: { id: string; values: { ID: string; Name: string }[] }[];
  sprints: { ID: string; Name: string }[];
  sprintReason?: string;
  transitions: { id: string; name: string; targetStateId: string; requiredFields: string[]; available: boolean; reason: string }[];
  workflows: WorkflowPin[];
  readinessPolicies: string[];
  milestones: Milestone[];
  capabilities: { id: string; state: string; value?: boolean; reason?: string }[];
  creation: { available: boolean; reason: string };
  reason?: string;
}
export interface MappingPreview {
  schemaVersion: 1;
  valid: boolean;
  issues: { field: string; code: string; message: string }[];
  intake?: { ruleId?: string; matched: boolean; action?: IntakeAction; group: { groupId: string; groupName: string; reason?: string } };
  outbound?: { ruleId?: string; matched: boolean };
  requestedAction?: unknown;
  requiredFields?: string[];
  requiredEvidence?: string[];
  approval?: string;
  reason?: string;
}
export interface TrackerBoardView {
  schemaVersion: 1;
  columns: TrackerColumn[];
  unknownGroup?: TrackerColumn;
  actions: Record<string, TrackerAction[]>;
  activeRevision: number;
  reason?: string;
}

export const trackerMappingApi = {
  get(projectId: string, signal?: AbortSignal) {
    return apiClient.operation("getTrackerMappingHistory", { path: { projectId }, signal }) as Promise<MappingState>;
  },
  async discovery(projectId: string, observationId: string, signal?: AbortSignal) {
    const result = await apiClient.operation("discoverTrackerMapping", { path: { projectId }, query: observationId ? { observationId } : undefined, signal }) as MappingDiscovery;
    return { ...result, fields: result.fields ?? [], sprints: result.sprints ?? [], transitions: result.transitions ?? [], workflows: result.workflows ?? [], readinessPolicies: result.readinessPolicies ?? [], milestones: result.milestones ?? [], capabilities: result.capabilities ?? [] };
  },
  save(projectId: string, rules: TrackerRules, observationId: string) {
    return apiClient.operation("saveTrackerMapping", { path: { projectId }, body: { schemaVersion: 1, rules, ...(observationId ? { observationId } : {}) } }) as Promise<MappingRevision>;
  },
  activate(projectId: string, revision: number, expectedActiveRevision: number, observationId: string) {
    return apiClient.operation("activateTrackerMapping", { path: { projectId }, body: { schemaVersion: 1, revision, expectedActiveRevision, ...(observationId ? { observationId } : {}) } }) as Promise<MappingState>;
  },
  preview(projectId: string, rules: TrackerRules, observationId: string, event?: components["schemas"]["TrackerMappingEvent"]) {
    return apiClient.operation("previewTrackerMapping", { path: { projectId }, body: { schemaVersion: 1, rules, ...(observationId ? { observationId } : {}), ...(event ? { event } : {}) } }) as Promise<MappingPreview>;
  },
  board(projectId: string, signal?: AbortSignal) {
    return apiClient.operation("getTrackerBoard", { path: { projectId }, signal }) as Promise<TrackerBoardView>;
  },
  transition(projectId: string, observationId: string, expectedBindingRevision: number, expectedMappingRevision: number, transitionId: string) {
    return apiClient.operation("transitionTrackerBoardTicket", { path: { projectId }, body: { schemaVersion: 1, observationId, expectedBindingRevision, expectedMappingRevision, transitionId }, idempotencyKey: `tracker-transition-${crypto.randomUUID()}` });
  },
};
