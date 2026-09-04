import type { components } from "../api/schema.generated";

type Schemas = components["schemas"];

export interface AuthoredReadinessContract {
  recommendedEvidence: Array<{ role: string; description: string }>;
  policyGates: Array<{ policy: string; enforcement: "advisory" | "blocking" | "external"; description: string }>;
  invariants: string[];
  remedies: Array<{ code: string; target: string; action: "supply_input" | "revise_artifact" | "clarify_decision" | "install_capability" | "rerun_validation"; description: string }>;
}

export interface AuthoredNode {
  id: string;
  displayName?: string;
  type: string;
  entry: boolean;
  terminal: boolean;
  requiredInputs: string[];
  readiness?: AuthoredReadinessContract;
}

export interface AuthoredWorkflow {
  apiVersion: string;
  displayName?: string;
  routeDefaults?: { entry: string; terminals: string[] };
  profiles: Array<{ id: string; description?: string; entry: string; terminals: string[] }>;
  nodes: AuthoredNode[];
}

export interface WorkflowPreviewInput {
  from?: string;
  until: readonly string[];
  requiredNodes: readonly string[];
  runInputsText: string;
}

export type AuthoredAdvice =
  | { kind: "required_input"; code: string; summary: string }
  | { kind: "recommended_evidence"; code: string; summary: string }
  | { kind: "policy_gate"; code: string; summary: string; enforcement: "advisory" | "blocking" | "external" }
  | { kind: "invariant"; code: string; summary: string }
  | { kind: "remedy"; code: string; summary: string; action: AuthoredReadinessContract["remedies"][number]["action"]; target: string };

export function sortWorkflowVersions(values: readonly Schemas["WorkflowVersionSummary"][]) {
  return [...values].sort((left, right) => left.name.localeCompare(right.name) || right.version.localeCompare(left.version) || left.digest.localeCompare(right.digest));
}

export function parseRunInputs(source: string): Record<string, unknown> {
  let value: unknown;
  try { value = JSON.parse(source); } catch { throw new Error("Run inputs must be one valid JSON object."); }
  if (!isRecord(value)) throw new Error("Run inputs must be a JSON object, not an array or scalar.");
  return value;
}

export function buildWorkflowPreviewRequest(input: WorkflowPreviewInput): Schemas["WorkflowPreviewRequest"] {
  const from = input.from?.trim();
  if (from && !identifierPattern.test(from)) throw new Error("Entry node must be a canonical workflow identifier.");
  const until = uniqueIdentifiers(input.until, "Terminal nodes");
  const requiredNodes = uniqueIdentifiers(input.requiredNodes, "Required nodes");
  const runInputs = parseRunInputs(input.runInputsText);
  for (const key of Object.keys(runInputs)) if (!identifierPattern.test(key)) throw new Error(`Run input “${key}” is not a canonical workflow identifier.`);
  return {
    range: { ...(from ? { from } : {}), ...(until.length ? { until } : {}) },
    // An explicit empty object is intentional: the API requires `context`, and
    // `{}` records that no run input facts participated in this preview.
    context: { runInputs, ...(requiredNodes.length ? { requiredNodes } : {}) },
  };
}

/** Flattens authored contracts for display; none of these entries is live run status. */
export function readinessAdvice(node: AuthoredNode): AuthoredAdvice[] {
  const contract = node.readiness;
  return [
    ...node.requiredInputs.map((input) => ({ kind: "required_input" as const, code: input, summary: `Required input ${input}` })),
    ...(contract?.recommendedEvidence.map((item) => ({ kind: "recommended_evidence" as const, code: item.role, summary: item.description })) ?? []),
    ...(contract?.policyGates.map((item) => ({ kind: "policy_gate" as const, code: item.policy, summary: item.description, enforcement: item.enforcement })) ?? []),
    ...(contract?.invariants.map((summary, index) => ({ kind: "invariant" as const, code: `invariant_${index + 1}`, summary })) ?? []),
    ...(contract?.remedies.map((item) => ({ kind: "remedy" as const, code: item.code, summary: item.description, action: item.action, target: item.target })) ?? []),
  ];
}

/** Summarizes one stateless candidate preview, not mutation impact on a run. */
export function previewImpact(route: Schemas["FrozenRoute"]) {
  return {
    entry: route.entry,
    terminalNodeIds: [...route.terminals],
    includedNodeIds: route.nodes.map((node) => node.id),
    excludedNodes: route.excludedNodes.map((node) => ({ id: node.id, reason: node.reason })),
    unresolvedInputs: route.inputRequirements.map((item) => ({ ...item })),
  };
}

/** Decodes only the installed definition fields this view understands. */
export function decodeAuthoredWorkflow(value: unknown): AuthoredWorkflow | undefined {
  if (!isRecord(value) || typeof value.apiVersion !== "string" || !isRecord(value.spec) || !isRecord(value.spec.nodes)) return undefined;
  const metadata = isRecord(value.metadata) ? value.metadata : {};
  const nodes = Object.entries(value.spec.nodes).flatMap(([id, raw]) => {
    if (!isRecord(raw) || typeof raw.type !== "string" || typeof raw.entry !== "boolean" || typeof raw.terminal !== "boolean") return [];
    const inputs = isRecord(raw.inputs) ? raw.inputs : {};
    const requiredInputs = Object.entries(inputs).filter(([, binding]) => !isRecord(binding) || binding.required !== false).map(([name]) => name);
    return [{
      id,
      displayName: typeof raw.displayName === "string" ? raw.displayName : undefined,
      type: raw.type,
      entry: raw.entry,
      terminal: raw.terminal,
      requiredInputs,
      readiness: decodeReadiness(raw.readiness),
    } satisfies AuthoredNode];
  });
  if (nodes.length !== Object.keys(value.spec.nodes).length) return undefined;
  return {
    apiVersion: value.apiVersion,
    displayName: typeof metadata.displayName === "string" ? metadata.displayName : undefined,
    routeDefaults: decodeRouteRange(value.spec.routeDefaults),
    profiles: decodeProfiles(value.spec.profiles),
    nodes,
  };
}

function decodeReadiness(value: unknown): AuthoredReadinessContract | undefined {
  if (!isRecord(value)) return undefined;
  const recommendedEvidence = decodeObjects(value.recommendedEvidence, (item) => typeof item.role === "string" && typeof item.description === "string" ? { role: item.role, description: item.description } : undefined);
  const policyGates = decodeObjects(value.policyGates, (item) => typeof item.policy === "string" && isEnforcement(item.enforcement) && typeof item.description === "string" ? { policy: item.policy, enforcement: item.enforcement, description: item.description } : undefined);
  const invariants = stringArray(value.invariants);
  const remedies = decodeObjects(value.remedies, (item) => typeof item.code === "string" && typeof item.target === "string" && isRemedyAction(item.action) && typeof item.description === "string" ? { code: item.code, target: item.target, action: item.action, description: item.description } : undefined);
  if (!recommendedEvidence || !policyGates || !invariants || !remedies) return undefined;
  return { recommendedEvidence, policyGates, invariants, remedies };
}

function decodeRouteRange(value: unknown) {
  if (!isRecord(value) || typeof value.entry !== "string") return undefined;
  const terminals = stringArray(value.terminals);
  return terminals ? { entry: value.entry, terminals } : undefined;
}

function decodeProfiles(value: unknown) {
  if (!isRecord(value)) return [];
  return Object.entries(value).flatMap(([id, item]) => {
    const range = decodeRouteRange(item);
    if (!range) return [];
    return [{ id, ...range, description: isRecord(item) && typeof item.description === "string" ? item.description : undefined }];
  });
}

function decodeObjects<T>(value: unknown, decode: (item: Record<string, unknown>) => T | undefined): T[] | undefined {
  if (!Array.isArray(value)) return undefined;
  const result: T[] = [];
  for (const candidate of value) {
    if (!isRecord(candidate)) return undefined;
    const decoded = decode(candidate);
    if (!decoded) return undefined;
    result.push(decoded);
  }
  return result;
}

function stringArray(value: unknown): string[] | undefined {
  return Array.isArray(value) && value.every((item) => typeof item === "string") ? value : undefined;
}

const identifierPattern = /^[a-z][a-z0-9_]{0,63}$/;

function uniqueIdentifiers(values: readonly string[], label: string) {
  const result = [...new Set(values.map((value) => value.trim()).filter(Boolean))].sort();
  if (result.some((value) => !identifierPattern.test(value))) throw new Error(`${label} must use canonical workflow identifiers.`);
  return result;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function isEnforcement(value: unknown): value is "advisory" | "blocking" | "external" {
  return value === "advisory" || value === "blocking" || value === "external";
}

function isRemedyAction(value: unknown): value is AuthoredReadinessContract["remedies"][number]["action"] {
  return value === "supply_input" || value === "revise_artifact" || value === "clarify_decision" || value === "install_capability" || value === "rerun_validation";
}
