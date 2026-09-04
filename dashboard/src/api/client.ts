import { getDashboardAuthorization } from "./bootstrap";
import { operationDefinitions, type ApiOperations, type ApiOperationId, type components } from "./schema.generated";

type Schemas = components["schemas"];

export interface ApiClientOptions {
  authorization?: string | (() => string | undefined);
  fetch?: typeof globalThis.fetch;
}

export interface RequestOptions<TBody = never> {
  path?: Readonly<Record<string, string | number>>;
  query?: Readonly<Record<string, string | number | boolean | undefined>>;
  body?: TBody;
  idempotencyKey?: string;
  resourceVersion?: number;
  signal?: AbortSignal;
}

export interface AgentLogChunk {
  offset: number;
  nextOffset: number;
  size: number;
  complete: boolean;
  bytes: Uint8Array;
}

export class ApiRequestError extends Error {
  readonly status: number;
  readonly code: string;

  constructor(status: number, code: string, message: string) {
    super(message);
    this.name = "ApiRequestError";
    this.status = status;
    this.code = code;
  }
}

export class DarkstarApiClient {
  private readonly authorization: string | (() => string | undefined);
  private readonly fetcher: typeof globalThis.fetch;

  constructor(options: ApiClientOptions = {}) {
    this.authorization = options.authorization ?? getDashboardAuthorization ?? (() => undefined);
    this.fetcher = options.fetch ?? globalThis.fetch.bind(globalThis);
  }

  async operation<TId extends ApiOperationId>(id: TId, options: RequestOptions<ApiOperations[TId]["body"]> = {}): Promise<ApiOperations[TId]["response"]> {
    const definition = operationDefinitions[id];
    let path = definition.path as string;
    for (const [name, value] of Object.entries(options.path ?? {})) path = path.replace(`{${name}}`, encodeURIComponent(String(value)));
    if (/\{[^}]+\}/.test(path)) throw new Error(`Missing path parameter for ${String(id)}`);
    const query = new URLSearchParams();
    for (const [name, value] of Object.entries(options.query ?? {})) if (value !== undefined) query.set(name, String(value));
    const headers = new Headers({ Accept: "application/json" });
    const authorization = typeof this.authorization === "function" ? this.authorization() : this.authorization;
    if (authorization) headers.set("Authorization", authorization);
    if (options.idempotencyKey) headers.set("Idempotency-Key", options.idempotencyKey);
    if (options.resourceVersion !== undefined) headers.set("If-Match", `\"${options.resourceVersion}\"`);
    if (options.body !== undefined) headers.set("Content-Type", "application/json");
    const response = await this.fetcher(`${path}${query.size ? `?${query}` : ""}`, {
      method: definition.method,
      headers,
      body: options.body === undefined ? undefined : JSON.stringify(options.body),
      credentials: "same-origin",
      signal: options.signal,
    });
    if (!response.ok) throw await toApiError(response);
    if (response.status === 204) return undefined as ApiOperations[TId]["response"];
    return response.json() as Promise<ApiOperations[TId]["response"]>;
  }

  getHealth(signal?: AbortSignal) { return this.operation("getHealth", { signal }); }
  getApiRoot(signal?: AbortSignal) { return this.operation("getApiRoot", { signal }); }
  listProjects(signal?: AbortSignal) { return this.operation("listProjects", { signal }); }
  listWorkItems(projectId?: string, signal?: AbortSignal) { return this.operation("listWorkItems", { query: { projectId }, signal }); }
  listRuns(query: { after?: string; limit?: number } = {}, signal?: AbortSignal) { return this.operation("listRuns", { query, signal }); }
  getRun(runId: string, signal?: AbortSignal) { return this.operation("getRun", { path: { runId }, signal }); }
  getRunReadiness(runId: string, signal?: AbortSignal) { return this.operation("getRunReadiness", { path: { runId }, signal }); }
  decideRunReadiness(runId: string, resourceVersion: number, idempotencyKey: string, body: Schemas["ReadinessDecisionRequest"], signal?: AbortSignal) { return this.operation("decideRunReadiness", { path: { runId }, body, resourceVersion, idempotencyKey, signal }); }
  getWorkItem(workItemId: string, signal?: AbortSignal) { return this.operation("getWorkItem", { path: { workItemId }, signal }); }
  listWorkflows(name?: string, signal?: AbortSignal) { return this.operation("listWorkflows", { query: { name }, signal }); }
  showWorkflow(name: string, version?: string, signal?: AbortSignal) { return this.operation("showWorkflow", { query: { name, version }, signal }); }
  graphWorkflow(name: string, version?: string, signal?: AbortSignal) { return this.operation("graphWorkflow", { query: { name, version }, signal }); }
  previewWorkflowRoute(name: string, version: string | undefined, body: Schemas["WorkflowPreviewRequest"], signal?: AbortSignal) { return this.operation("previewWorkflowRoute", { query: { name, version }, body, signal }); }
  createWorkItem(body: Schemas["CreateWorkItemRequest"], idempotencyKey: string, signal?: AbortSignal) { return this.operation("createWorkItem", { body, idempotencyKey, signal }); }
  createRun(body: Schemas["CreateRunRequest"], idempotencyKey: string, signal?: AbortSignal) { return this.operation("createOrStartRun", { body, idempotencyKey, signal }); }
  pauseRun(runId: string, resourceVersion: number, idempotencyKey: string, signal?: AbortSignal) { return this.operation("pauseRun", { path: { runId }, resourceVersion, idempotencyKey, signal }); }
  resumeRun(runId: string, resourceVersion: number, idempotencyKey: string, signal?: AbortSignal) { return this.operation("resumeRun", { path: { runId }, resourceVersion, idempotencyKey, signal }); }
  retryRun(runId: string, resourceVersion: number, idempotencyKey: string, signal?: AbortSignal) { return this.operation("retryRun", { path: { runId }, body: {}, resourceVersion, idempotencyKey, signal }); }
  cancelRun(runId: string, resourceVersion: number, idempotencyKey: string, signal?: AbortSignal) { return this.operation("cancelRun", { path: { runId }, resourceVersion, idempotencyKey, signal }); }
  listArtifacts(targetKind?: Schemas["ArtifactTargetKind"], targetId?: string, signal?: AbortSignal) { return this.operation("listArtifacts", { query: { targetKind, targetId }, signal }); }
  getArtifact(artifactId: string, version?: number, signal?: AbortSignal) { return this.operation("getArtifact", { path: { artifactId }, query: { version }, signal }); }
  reviseArtifact(artifactId: string, resourceVersion: number, body: Schemas["ArtifactIngestRequest"], idempotencyKey: string, signal?: AbortSignal) { return this.operation("reviseArtifact", { path: { artifactId }, body, resourceVersion, idempotencyKey, signal }); }
  diffArtifactVersions(artifactId: string, from: number, to: number, signal?: AbortSignal) { return this.operation("diffArtifactVersions", { path: { artifactId }, query: { from, to }, signal }); }
  lintArtifact(artifactId: string, version: number, signal?: AbortSignal) { return this.operation("lintArtifact", { path: { artifactId }, query: { version }, signal }); }
  assessArtifactImpact(artifactId: string, version: number, body: Schemas["ArtifactImpactRequest"], signal?: AbortSignal) { return this.operation("assessArtifactImpact", { path: { artifactId }, query: { version }, body, signal }); }
  listCheckpoints(query: { class?: "workflow_checkpoint"; runId?: string; status?: "pending" | "approved" | "changes_requested" | "rejected" | "denied" | "cancelled" | "expired" } = {}, signal?: AbortSignal) { return this.operation("listCheckpoints", { query, signal }); }
  getCheckpointHistory(checkpointId: string, signal?: AbortSignal) { return this.operation("getCheckpointHistory", { path: { checkpointId }, signal }); }
  getApproval(approvalId: string, signal?: AbortSignal) { return this.operation("getApproval", { path: { approvalId }, signal }); }
  decideApproval(approvalId: string, resourceVersion: number, idempotencyKey: string, body: Schemas["ArtifactCheckpointDecisionRequest"], signal?: AbortSignal) { return this.operation("decideApproval", { path: { approvalId }, body, resourceVersion, idempotencyKey, signal }); }
  listInputRequests(query: { runId?: string; attemptId?: string; status?: "pending" | "answer_recorded" | "answered" } = {}, signal?: AbortSignal) { return this.operation("listInputRequests", { query, signal }); }
  getInputRequest(inputRequestId: string, signal?: AbortSignal) { return this.operation("getInputRequest", { path: { inputRequestId }, signal }); }
  answerInputRequest(inputRequestId: string, resourceVersion: number, idempotencyKey: string, body: Schemas["InputRequestAnswerRequest"], signal?: AbortSignal) { return this.operation("answerInputRequest", { path: { inputRequestId }, body, resourceVersion, idempotencyKey, signal }); }
  retryInputDelivery(inputRequestId: string, resourceVersion: number, signal?: AbortSignal) { return this.operation("retryInputRequestDelivery", { path: { inputRequestId }, resourceVersion, signal }); }
  listAgents(signal?: AbortSignal) { return this.operation("listAgents", { signal }); }
  getAgent(attemptId: string, signal?: AbortSignal) { return this.operation("getAgent", { path: { attemptId }, signal }); }
  cancelAgent(attemptId: string, resourceVersion: number, idempotencyKey: string, signal?: AbortSignal) { return this.operation("cancelAgent", { path: { attemptId }, resourceVersion, idempotencyKey, signal }); }
  listProviderPermissions(query: { attemptId?: string; status?: "pending" | "decision_recorded" | "responded" } = {}, signal?: AbortSignal) { return this.operation("listProviderPermissions", { query, signal }); }
  getProviderPermission(permissionRequestId: string, signal?: AbortSignal) { return this.operation("getProviderPermission", { path: { permissionRequestId }, signal }); }
  decideProviderPermission(permissionRequestId: string, resourceVersion: number, idempotencyKey: string, body: Schemas["ProviderPermissionDecisionRequest"], signal?: AbortSignal) { return this.operation("decideProviderPermission", { path: { permissionRequestId }, resourceVersion, idempotencyKey, body, signal }); }
  retryProviderPermissionDelivery(permissionRequestId: string, resourceVersion: number, signal?: AbortSignal) { return this.operation("retryProviderPermissionDelivery", { path: { permissionRequestId }, resourceVersion, signal }); }

  async readAgentLog(attemptId: string, after = 0, limit = 65_536, signal?: AbortSignal): Promise<AgentLogChunk> {
    const headers = new Headers({ Accept: "application/octet-stream" });
    const authorization = typeof this.authorization === "function" ? this.authorization() : this.authorization;
    if (authorization) headers.set("Authorization", authorization);
    const query = new URLSearchParams({ after: String(after), limit: String(limit) });
    const path = operationDefinitions.readAgentLog.path.replace("{attemptId}", encodeURIComponent(attemptId));
    const response = await this.fetcher(`${path}?${query}`, { headers, credentials: "same-origin", signal });
    if (!response.ok) throw await toApiError(response);
    const offset = requiredIntegerHeader(response, "X-Darkstar-Log-Offset");
    const nextOffset = requiredIntegerHeader(response, "X-Darkstar-Log-Next-Offset");
    const size = requiredIntegerHeader(response, "X-Darkstar-Log-Size");
    const completeValue = response.headers.get("X-Darkstar-Log-Complete");
    if (completeValue !== "true" && completeValue !== "false") throw new Error("Agent log response omitted its completion cursor.");
    const bytes = new Uint8Array(await response.arrayBuffer());
    if (nextOffset !== offset + bytes.length || nextOffset > size) throw new Error("Agent log response returned inconsistent cursor metadata.");
    return { offset, nextOffset, size, complete: completeValue === "true", bytes };
  }
}

function requiredIntegerHeader(response: Response, name: string) {
  const raw = response.headers.get(name);
  const value = raw === null ? Number.NaN : Number(raw);
  if (!Number.isSafeInteger(value) || value < 0) throw new Error(`Agent log response omitted ${name}.`);
  return value;
}

async function toApiError(response: Response) {
  let body: { code?: string; message?: string } | undefined;
  if (response.headers.get("content-type")?.includes("application/json")) {
    try { body = await response.json() as typeof body; } catch { body = undefined; }
  }
  return new ApiRequestError(response.status, body?.code ?? "request_failed", body?.message ?? `API request failed with status ${response.status}`);
}

export const apiClient = new DarkstarApiClient();
