import type { components } from "../api/schema.generated";

type Schemas = components["schemas"];
export type ReadinessView = Schemas["RunReadinessView"];
export type AllowedAction = Schemas["ReadinessAllowedAction"];
export type ReadinessFinding = Schemas["ReadinessFinding"];

export interface FindingGroups {
  information: Schemas["ReadinessInformationFinding"][];
  recommendation: Schemas["ReadinessRecommendationFinding"][];
  policy_gate: Schemas["ReadinessPolicyGateFinding"][];
  invariant: Schemas["ReadinessInvariantFinding"][];
}

export function groupFindings(findings: readonly ReadinessFinding[]): FindingGroups {
  const groups: FindingGroups = { information: [], recommendation: [], policy_gate: [], invariant: [] };
  for (const finding of findings) {
    switch (finding.level) {
      case "information": groups.information.push(finding); break;
      case "recommendation": groups.recommendation.push(finding); break;
      case "policy_gate": groups.policy_gate.push(finding); break;
      case "invariant": groups.invariant.push(finding); break;
    }
  }
  return groups;
}

export function readinessActionKey(action: AllowedAction) {
  return action.choice === "supply_input" ? `${action.choice}:${action.remedy?.code ?? ""}` : action.choice;
}

export function readinessActionPresentation(action: AllowedAction) {
  switch (action.choice) {
    case "continue": return { label: "Continue unchanged", description: "Record this readiness choice without invoking the separate run-continue command.", tone: "primary" as const };
    case "accept_route_change": return { label: "Accept route change", description: "Accept only the validated server-bound proposal shown here.", tone: "primary" as const };
    case "supply_input": return { label: action.remedy ? `Supply input · ${action.remedy.code}` : "Supply input", description: action.remedy?.description ?? "Record the selected server-provided input remedy.", tone: "neutral" as const };
    case "cancel": return { label: "Cancel readiness decision", description: "Record cancellation of this readiness decision; this does not cancel the run.", tone: "danger" as const };
  }
}

export function buildReadinessDecisionRequest(view: ReadinessView, selectedActionKey: string, reason: string): Schemas["ReadinessDecisionRequest"] {
  if (view.status !== "pending") throw new Error("This readiness assessment already has a recorded decision.");
  const action = view.allowedActions.find((candidate) => readinessActionKey(candidate) === selectedActionKey);
  if (!action) throw new Error("Choose an action currently allowed by the daemon.");
  const normalizedReason = reason.trim();
  if (!normalizedReason) throw new Error("Explain why this readiness choice is appropriate.");
  if (action.choice === "supply_input" && !action.remedy?.code) throw new Error("The selected supply-input action has no server-provided remedy.");
  if (action.choice === "supply_input") {
    return { action: action.choice, assessmentDigest: view.assessment.digest, reason: normalizedReason, remedyCode: action.remedy.code };
  }
  return { action: action.choice, assessmentDigest: view.assessment.digest, reason: normalizedReason };
}

export function assessmentChanged(previous: ReadinessView, next: ReadinessView) {
  return previous.assessment.assessmentId !== next.assessment.assessmentId || previous.assessment.digest !== next.assessment.digest || previous.resourceVersion !== next.resourceVersion || previous.status !== next.status;
}

/** Clones server-provided impact for display; it never derives patch operations. */
export function routeChangePresentation(change: Schemas["ReadinessRouteChange"]) {
  return {
    addedNodes: [...(change.impact.addedNodes ?? [])],
    removedNodes: [...(change.impact.removedNodes ?? [])],
    addedTransitions: [...(change.impact.addedTransitions ?? [])],
    removedTransitions: [...(change.impact.removedTransitions ?? [])],
    previousTerminals: [...change.impact.previousTerminals],
    proposedTerminals: [...change.impact.proposedTerminals],
    candidate: change.candidate.route,
    authorizationMode: change.authorizationMode,
  };
}
