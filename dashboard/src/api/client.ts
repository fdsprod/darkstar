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
  getWorkItem(workItemId: string, signal?: AbortSignal) { return this.operation("getWorkItem", { path: { workItemId }, signal }); }
  listWorkflows(name?: string, signal?: AbortSignal) { return this.operation("listWorkflows", { query: { name }, signal }); }
  createWorkItem(body: Schemas["CreateWorkItemRequest"], idempotencyKey: string, signal?: AbortSignal) { return this.operation("createWorkItem", { body, idempotencyKey, signal }); }
  createRun(body: Schemas["CreateRunRequest"], idempotencyKey: string, signal?: AbortSignal) { return this.operation("createOrStartRun", { body, idempotencyKey, signal }); }
  pauseRun(runId: string, resourceVersion: number, idempotencyKey: string, signal?: AbortSignal) { return this.operation("pauseRun", { path: { runId }, resourceVersion, idempotencyKey, signal }); }
  resumeRun(runId: string, resourceVersion: number, idempotencyKey: string, signal?: AbortSignal) { return this.operation("resumeRun", { path: { runId }, resourceVersion, idempotencyKey, signal }); }
  retryRun(runId: string, resourceVersion: number, idempotencyKey: string, signal?: AbortSignal) { return this.operation("retryRun", { path: { runId }, body: {}, resourceVersion, idempotencyKey, signal }); }
  cancelRun(runId: string, resourceVersion: number, idempotencyKey: string, signal?: AbortSignal) { return this.operation("cancelRun", { path: { runId }, resourceVersion, idempotencyKey, signal }); }
}

async function toApiError(response: Response) {
  let body: { code?: string; message?: string } | undefined;
  if (response.headers.get("content-type")?.includes("application/json")) {
    try { body = await response.json() as typeof body; } catch { body = undefined; }
  }
  return new ApiRequestError(response.status, body?.code ?? "request_failed", body?.message ?? `API request failed with status ${response.status}`);
}

export const apiClient = new DarkstarApiClient();
