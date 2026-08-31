import { readFileSync } from "node:fs";
import { pathToFileURL } from "node:url";

const ENVELOPE_FIELDS = new Set(["schemaVersion","id","globalPosition","streamId","streamSequence","aggregateType","aggregateId","aggregateRevision","kind","occurredAt","recordedAt","correlationId","causationId","commandId","actor","data","metadata"]);
const FORBIDDEN_FIELDS = new Set(["providerThreadId","providerTurnId","providerItemId","uiState","dashboardState","component","rawProviderEvent"]);
const EVENT_ID = /^event_[0-9A-HJKMNP-TV-Z]{26}$/;
const RESOURCE_ID = /^(project|work|run|visit|attempt|artifact|approval|operation)_[0-9A-HJKMNP-TV-Z]{26}$/;

function findForbidden(value, path = "$") {
  const failures = [];
  if (!value || typeof value !== "object") return failures;
  for (const [key, child] of Object.entries(value)) {
    if (FORBIDDEN_FIELDS.has(key)) failures.push(`${path}.${key}`);
    failures.push(...findForbidden(child, `${path}.${key}`));
  }
  return failures;
}

export function validateTrace(trace) {
  const failures = [];
  const streamSequence = new Map();
  const aggregateRevision = new Map();
  const eventIds = new Set();
  trace.events.forEach((event, index) => {
    const position = index + 1;
    if (event.schemaVersion !== 1) failures.push(`event ${position}: schemaVersion`);
    if (!EVENT_ID.test(event.id) || !RESOURCE_ID.test(event.streamId) || !RESOURCE_ID.test(event.aggregateId)) failures.push(`event ${position}: id`);
    if (event.globalPosition !== position) failures.push(`event ${position}: globalPosition`);
    if ([...Object.keys(event)].some((key) => !ENVELOPE_FIELDS.has(key))) failures.push(`event ${position}: envelope field`);
    const nextSequence = (streamSequence.get(event.streamId) ?? 0) + 1;
    if (event.streamSequence !== nextSequence) failures.push(`event ${position}: streamSequence`);
    streamSequence.set(event.streamId, event.streamSequence);
    const nextRevision = (aggregateRevision.get(event.aggregateId) ?? 0) + 1;
    if (event.aggregateRevision !== nextRevision) failures.push(`event ${position}: aggregateRevision`);
    aggregateRevision.set(event.aggregateId, event.aggregateRevision);
    if (event.aggregateId !== event.streamId || event.aggregateRevision !== event.streamSequence) failures.push(`event ${position}: MVP stream mapping`);
    if (event.causationId && !eventIds.has(event.causationId)) failures.push(`event ${position}: causation`);
    failures.push(...findForbidden(event.data, `event[${position}].data`));
    failures.push(...findForbidden(event.metadata, `event[${position}].metadata`));
    eventIds.add(event.id);
  });
  return failures;
}

export function projectTrace(trace) {
  const projection = { runId: null, status: null, resourceVersion: 0, visits: {}, attempts: {}, approvals: {}, artifacts: [] };
  for (const event of trace.events) {
    if (event.aggregateType === "run") projection.resourceVersion = event.aggregateRevision;
    if (event.kind === "run.created") { projection.runId = event.aggregateId; projection.status = "running"; }
    if (event.kind === "run.resumed") projection.status = "running";
    if (event.kind === "run.completed") projection.status = "completed";
    if (event.kind === "node_visit.started") projection.visits[event.data.visitId] = "running";
    if (event.kind === "node_visit.completed") projection.visits[event.data.visitId] = "completed";
    if (event.kind === "attempt.started") projection.attempts[event.aggregateId] = "running";
    if (event.kind === "attempt.completed") projection.attempts[event.aggregateId] = "completed";
    if (event.kind === "artifact.created") projection.artifacts.push(event.aggregateId);
    if (event.kind === "approval.requested") { projection.approvals[event.aggregateId] = "pending"; projection.status = "waiting"; }
    if (event.kind === "approval.decided") projection.approvals[event.aggregateId] = event.data.action === "approve" ? "approved" : event.data.action;
  }
  return projection;
}

export function replayAfter(trace, lastEventId) {
  return trace.events.filter((event) => event.globalPosition > lastEventId);
}

export function evaluateIdempotency(trace) {
  const command = trace.events.find((event) => event.commandId === trace.idempotency.replay.key);
  return {
    replayNewEvents: command && trace.idempotency.replay.requestDigest === "sha256:complete" ? 0 : null,
    conflict: command && trace.idempotency.conflict.requestDigest !== trace.idempotency.replay.requestDigest ? "IDEMPOTENCY_CONFLICT" : null,
  };
}

export function loadTrace(path) { return JSON.parse(readFileSync(path, "utf8")); }

export function main(argv = process.argv.slice(2)) {
  if (argv.length !== 1) { process.stderr.write("Usage: node scripts/runtime-reference.mjs <fake-run.json>\n"); return 2; }
  const trace = loadTrace(argv[0]);
  const failures = validateTrace(trace);
  const projection = projectTrace(trace);
  const pass = failures.length === 0 && JSON.stringify(projection) === JSON.stringify(trace.expectedProjection);
  process.stdout.write(`${JSON.stringify({ schemaVersion:trace.schemaVersion, pass, failures, projection }, null, 2)}\n`);
  return pass ? 0 : 1;
}

if (import.meta.url === pathToFileURL(process.argv[1]).href) process.exitCode = main();
