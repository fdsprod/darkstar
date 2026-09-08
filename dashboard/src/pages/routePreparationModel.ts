import type { components } from "../api/schema.generated";

type Schemas = components["schemas"];
export type RouteAssessment = Schemas["RoutePreparationAssessment"];
export type FrozenRoute = Schemas["FrozenRoute"];

export type PreparationDraft = {
  workflowId: string;
  workflowVersion: string;
  profile: string;
  entryNodeId: string;
  terminalNodeIds: string;
  answers: Record<string, string>;
  runInputs: string;
  evidence: string;
};

export type PreparationState =
  | { kind: "idle"; draft: PreparationDraft }
  | { kind: "preparing"; draft: PreparationDraft; priorRoute?: FrozenRoute }
  | { kind: "stale"; draft: PreparationDraft; message: string }
  | { kind: "error"; draft: PreparationDraft; message: string };

export type AssessmentPresentation = {
  readiness: "automatic" | "advisory" | "input_required" | "confirmation_required";
  routeSource: "automatic" | "override";
  selectedAssumptions: string[];
  evidence: Array<{ reference: string; available: boolean; used: boolean }>;
};

export function emptyPreparationDraft(): PreparationDraft {
  return { workflowId: "", workflowVersion: "", profile: "", entryNodeId: "", terminalNodeIds: "", answers: {}, runInputs: "", evidence: "" };
}

export function assessmentPresentation(assessment: RouteAssessment): AssessmentPresentation {
  const questions = assessment.questions ?? [];
  const confirmationReasons = assessment.confirmationReasons ?? [];
  const selected = (assessment.advice.candidates ?? []).find((candidate) =>
    candidate.entry === assessment.route.entry && sameStrings(candidate.terminals, assessment.route.terminals));
  const assumptions = [...(selected?.assumptions ?? [])];
  const cited = new Set(assessment.advice.evidenceUsed ?? []);
  const evidence = (assessment.input.evidence ?? []).map((item) => ({ reference: item.reference, available: Boolean(item.digest && item.content), used: cited.has(item.reference) }));
  let readiness: AssessmentPresentation["readiness"] = "automatic";
  if (questions.length > 0 || assessment.route.inputRequirements.length > 0) readiness = "input_required";
  else if (confirmationReasons.length > 0) readiness = "confirmation_required";
  else if (assumptions.length > 0 || evidence.length > 0) readiness = "advisory";
  return { readiness, routeSource: assessment.input.override ? "override" : "automatic", selectedAssumptions: assumptions, evidence };
}

export function buildPreparationRequest(workItemId: string, draft: PreparationDraft): Schemas["CreateRunRequest"] {
  const workflowId = draft.workflowId.trim();
  const workflowVersion = draft.workflowVersion.trim();
  const profile = draft.profile.trim();
  const entryNodeId = draft.entryNodeId.trim();
  const terminalNodeIds = [...new Set(draft.terminalNodeIds.split(",").map((value) => value.trim()).filter(Boolean))];
  if ((workflowId && !workflowVersion) || (!workflowId && workflowVersion)) throw new Error("Choose both a workflow and an exact version for an advanced override.");
  let runInputs: Record<string, unknown> | undefined;
  if (draft.runInputs.trim()) {
    const parsed: unknown = JSON.parse(draft.runInputs);
    if (!parsed || Array.isArray(parsed) || typeof parsed !== "object") throw new Error("Run inputs must be a JSON object.");
    runInputs = parsed as Record<string, unknown>;
  }
  const answers = Object.fromEntries(Object.entries(draft.answers).map(([key, value]) => [key, value.trim()]).filter(([, value]) => value));
  const evidence = [...new Set(draft.evidence.split("\n").map((value) => value.trim()).filter(Boolean))];
  if (profile && (entryNodeId || terminalNodeIds.length)) throw new Error("Choose a route profile or explicit entry and terminals, not both.");
  const routeOverride = entryNodeId || terminalNodeIds.length ? { ...(entryNodeId ? { from: entryNodeId } : {}), ...(terminalNodeIds.length ? { until: terminalNodeIds } : {}) } : undefined;
  const preparation = Object.keys(answers).length || runInputs || evidence.length || routeOverride ? { ...(Object.keys(answers).length ? { answers } : {}), ...(runInputs ? { runInputs } : {}), ...(evidence.length ? { evidence } : {}), ...(routeOverride ? { routeOverride } : {}) } : undefined;
  return { workItemId, ...(workflowId ? { workflowId, workflowVersion } : {}), ...(profile ? { profile } : {}), ...(preparation ? { preparation } : {}) };
}

export function routeDifference(before: FrozenRoute | undefined, after: FrozenRoute) {
  if (!before) return undefined;
  const beforeNodes = new Set(before.nodes.map((node) => node.id));
  const afterNodes = new Set(after.nodes.map((node) => node.id));
  const added = after.nodes.map((node) => node.id).filter((id) => !beforeNodes.has(id));
  const removed = before.nodes.map((node) => node.id).filter((id) => !afterNodes.has(id));
  const changed = before.entry !== after.entry || !sameStrings(before.terminals, after.terminals) || added.length > 0 || removed.length > 0;
  return changed ? { before, after, added, removed } : undefined;
}

function sameStrings(left: readonly string[], right: readonly string[]) {
  return left.length === right.length && [...left].sort().every((value, index) => value === [...right].sort()[index]);
}
