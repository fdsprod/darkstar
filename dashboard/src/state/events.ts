export interface ServerSentEvent {
  id?: string;
  event: string;
  data: string;
}

export interface DomainEvent {
  schemaVersion: number;
  id: string;
  globalPosition: number;
  aggregateType: string;
  aggregateId: string;
  aggregateRevision: number;
  kind: string;
  occurredAt: string;
  recordedAt: string;
  correlationId: string;
  causationId: string | null;
  commandId: string;
  actor: { type: string; id: string };
  data: unknown;
  metadata: unknown;
}

export interface EventCursorAdvance {
  cursor: number;
  accepted: boolean;
}

export interface ReplayRecovery {
  rehydrate: true;
  cursor: number;
}

export class EventSequenceError extends Error {
  readonly expected: number;
  readonly received: number;

  constructor(expected: number, received: number) {
    super("The event stream is not contiguous.");
    this.name = "EventSequenceError";
    this.expected = expected;
    this.received = received;
  }
}

export interface EventStreamProblemDetail {
  field?: string;
  code?: string;
  message?: string;
}

export class EventStreamHttpError extends Error {
  readonly status: number;
  readonly code: string;
  readonly retryable: boolean;
  readonly details: readonly EventStreamProblemDetail[];

  constructor(
    status: number,
    code: string,
    message: string,
    retryable: boolean,
    details: readonly EventStreamProblemDetail[],
  ) {
    super(message);
    this.name = "EventStreamHttpError";
    this.status = status;
    this.code = code;
    this.retryable = retryable;
    this.details = details;
  }
}

export interface OpenEventStreamOptions {
  fetcher?: typeof globalThis.fetch;
  authorization?: string;
  cursor: number;
  signal?: AbortSignal;
}

/**
 * Opens the authenticated stream using fetch rather than EventSource. EventSource
 * cannot attach the in-memory bearer credential and must not put it in a URL.
 */
export async function openEventStream(options: OpenEventStreamOptions): Promise<Response> {
  const headers = new Headers({
    Accept: "text/event-stream",
    "Last-Event-ID": String(options.cursor),
  });
  if (options.authorization) headers.set("Authorization", options.authorization);

  const fetcher = options.fetcher ?? globalThis.fetch.bind(globalThis);
  const response = await fetcher("/api/v1/events", {
    method: "GET",
    headers,
    credentials: "same-origin",
    cache: "no-store",
    signal: options.signal,
  });
  if (!response.ok) throw await readEventStreamError(response);
  if (!response.headers.get("content-type")?.toLowerCase().includes("text/event-stream")) {
    throw new EventStreamHttpError(response.status, "EVENT_STREAM_PROTOCOL_INVALID", "The daemon returned an invalid event stream.", true, []);
  }
  if (!response.body) {
    throw new EventStreamHttpError(response.status, "EVENT_STREAM_PROTOCOL_INVALID", "The daemon returned an empty event stream.", true, []);
  }
  return response;
}

/** Exponential reconnect delay with bounded jitter. */
export function reconnectDelay(attempt: number, random: () => number = Math.random): number {
  const boundedAttempt = Math.max(0, Math.min(8, Math.floor(attempt)));
  const base = Math.min(15_000, 500 * (2 ** boundedAttempt));
  const jitter = 0.75 + Math.max(0, Math.min(1, random())) * 0.5;
  return Math.round(base * jitter);
}

/** Returns the only safe restart point advertised by an authoritative 410. */
export function replayRecovery(error: unknown): ReplayRecovery | undefined {
  if (!(error instanceof EventStreamHttpError) || error.status !== 410 || error.code !== "EVENT_REPLAY_UNAVAILABLE") return undefined;
  const detail = error.details.find((candidate) => candidate.field === "oldestAvailablePosition");
  if (!detail?.message || !/^\d+$/.test(detail.message)) return undefined;
  const oldest = Number(detail.message);
  if (!Number.isSafeInteger(oldest) || oldest < 1) return undefined;
  return { rehydrate: true, cursor: oldest - 1 };
}

/** Parses an arbitrary byte-chunked SSE response without assuming line boundaries. */
export async function* parseServerSentEvents(source: AsyncIterable<Uint8Array | string>): AsyncGenerator<ServerSentEvent> {
  const decoder = new TextDecoder();
  let buffer = "";
  let firstChunk = true;
  let eventType = "";
  let eventId: string | undefined;
  let data: string[] = [];

  const dispatch = () => {
    if (data.length === 0) return undefined;
    const message: ServerSentEvent = { event: eventType || "message", data: data.join("\n") };
    if (eventId !== undefined) message.id = eventId;
    eventType = "";
    data = [];
    return message;
  };

  const acceptLine = (line: string) => {
    if (line === "") return dispatch();
    if (line.startsWith(":")) return undefined;
    const separator = line.indexOf(":");
    const field = separator < 0 ? line : line.slice(0, separator);
    let value = separator < 0 ? "" : line.slice(separator + 1);
    if (value.startsWith(" ")) value = value.slice(1);
    if (field === "event") eventType = value;
    if (field === "data") data.push(value);
    if (field === "id" && !value.includes("\0")) eventId = value;
    return undefined;
  };

  for await (const chunk of source) {
    buffer += typeof chunk === "string" ? chunk : decoder.decode(chunk, { stream: true });
    if (firstChunk) {
      buffer = buffer.replace(/^\uFEFF/, "");
      firstChunk = false;
    }
    while (true) {
      const match = /\r\n|\r|\n/.exec(buffer);
      if (!match || match.index === undefined) break;
      // A CR may be the first half of CRLF split across byte chunks.
      if (match[0] === "\r" && match.index === buffer.length - 1) break;
      const line = buffer.slice(0, match.index);
      buffer = buffer.slice(match.index + match[0].length);
      const message = acceptLine(line);
      if (message) yield message;
    }
  }
  buffer += decoder.decode();
  while (true) {
    const match = /\r\n|\r|\n/.exec(buffer);
    if (!match || match.index === undefined) break;
    const line = buffer.slice(0, match.index);
    buffer = buffer.slice(match.index + match[0].length);
    const message = acceptLine(line);
    if (message) yield message;
  }
  if (buffer !== "") acceptLine(buffer);
  const final = dispatch();
  if (final) yield final;
}

export function parseDomainEvent(message: ServerSentEvent): DomainEvent {
  if (message.event === "problem") throw parseStreamProblem(message.data);
  let value: unknown;
  try {
    value = JSON.parse(message.data);
  } catch {
    throw new EventSequenceError(-1, -1);
  }
  if (!isDomainEvent(value)) throw new EventSequenceError(-1, -1);
  if (message.id === undefined || !/^\d+$/.test(message.id) || Number(message.id) !== value.globalPosition || message.event !== value.kind) {
    throw new EventSequenceError(value.globalPosition, Number(message.id ?? -1));
  }
  return value;
}

/** Accepts exactly the next event and ignores only already-observed replay duplicates. */
export function advanceEventCursor(current: number, event: DomainEvent): EventCursorAdvance {
  if (!Number.isSafeInteger(current) || current < 0) throw new EventSequenceError(-1, current);
  if (event.globalPosition <= current) return { cursor: current, accepted: false };
  if (event.globalPosition !== current + 1) throw new EventSequenceError(current + 1, event.globalPosition);
  return { cursor: event.globalPosition, accepted: true };
}

function isDomainEvent(value: unknown): value is DomainEvent {
  if (!isRecord(value)) return false;
  return value.schemaVersion === 1 &&
    typeof value.id === "string" &&
    Number.isSafeInteger(value.globalPosition) && Number(value.globalPosition) > 0 &&
    typeof value.aggregateType === "string" && typeof value.aggregateId === "string" &&
    Number.isSafeInteger(value.aggregateRevision) && Number(value.aggregateRevision) > 0 &&
    typeof value.kind === "string" && typeof value.occurredAt === "string" && typeof value.recordedAt === "string" &&
    typeof value.correlationId === "string" && (value.causationId === null || typeof value.causationId === "string") &&
    typeof value.commandId === "string" && isRecord(value.actor) && typeof value.actor.type === "string" && typeof value.actor.id === "string" &&
    Object.hasOwn(value, "data") && Object.hasOwn(value, "metadata");
}

function parseStreamProblem(data: string): EventStreamHttpError {
  let value: unknown;
  try { value = JSON.parse(data); } catch { value = undefined; }
  if (!isRecord(value)) return new EventStreamHttpError(200, "EVENT_STREAM_FAILED", "The event stream could not continue.", true, []);
  return new EventStreamHttpError(
    200,
    typeof value.code === "string" ? value.code : "EVENT_STREAM_FAILED",
    typeof value.message === "string" ? value.message : "The event stream could not continue.",
    value.retryable === true,
    Array.isArray(value.details) ? value.details.filter(isProblemDetail) : [],
  );
}

async function readEventStreamError(response: Response): Promise<EventStreamHttpError> {
  let value: unknown;
  try { value = await response.json(); } catch { value = undefined; }
  if (!isRecord(value)) return new EventStreamHttpError(response.status, "EVENT_STREAM_FAILED", "The event stream is unavailable.", response.status >= 500, []);
  return new EventStreamHttpError(
    response.status,
    typeof value.code === "string" ? value.code : "EVENT_STREAM_FAILED",
    typeof value.message === "string" ? value.message : "The event stream is unavailable.",
    value.retryable === true || response.status >= 500,
    Array.isArray(value.details) ? value.details.filter(isProblemDetail) : [],
  );
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}

function isProblemDetail(value: unknown): value is EventStreamProblemDetail {
  return isRecord(value) &&
    (value.field === undefined || typeof value.field === "string") &&
    (value.code === undefined || typeof value.code === "string") &&
    (value.message === undefined || typeof value.message === "string");
}
