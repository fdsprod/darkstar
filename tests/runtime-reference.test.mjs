import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { evaluateIdempotency, loadTrace, projectTrace, replayAfter, validateTrace } from "../scripts/runtime-reference.mjs";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const trace = loadTrace(resolve(root, "examples/runtime/fake-run.json"));

test("fake run preserves event ordering, revisions, causation, and boundaries", () => {
  assert.deepEqual(validateTrace(trace), []);
});

test("event aggregate type must match its tagged identity", () => {
  const invalid = structuredClone(trace);
  invalid.events[0].aggregateType = "work";
  assert.ok(validateTrace(invalid).includes("event 1: aggregate type"));
});

test("fake run reconstructs the documented final projection", () => {
  assert.deepEqual(projectTrace(trace), trace.expectedProjection);
});

test("SSE replay returns the strict suffix after Last-Event-ID", () => {
  for (let position = 0; position <= trace.events.length; position++) {
    const suffix = replayAfter(trace, position);
    assert.equal(suffix.length, trace.events.length - position);
    assert.ok(suffix.every((event) => event.globalPosition > position));
  }
});

test("command replay is side-effect free and changed payload conflicts", () => {
  assert.deepEqual(evaluateIdempotency(trace), { replayNewEvents:0, conflict:"IDEMPOTENCY_CONFLICT" });
});

test("initial OpenAPI surface is versioned and references shared errors", () => {
  const api = JSON.parse(readFileSync(resolve(root, "schemas/openapi-v1alpha1.json"), "utf8"));
  assert.equal(api.openapi, "3.1.0");
  assert.ok(Object.keys(api.paths).every((path) => path.startsWith("/api/v1/")));
  assert.equal(api.components.schemas.ApiError.$ref, "./api-error-v1alpha1.schema.json");
  assert.ok(api.paths["/api/v1/events"].get.responses["410"]);
  assert.equal(api.components.schemas.RunPage.properties.pageInfo.properties.hasNextPage, undefined);
  const checkpointRequest = api.paths["/api/v1/approvals/{approvalId}/decisions"].post.requestBody.content["application/json"].schema;
  assert.equal(checkpointRequest.$ref, "#/components/schemas/ArtifactCheckpointDecisionRequest");
  const checkpointVariants = api.components.schemas.ArtifactCheckpointDecisionRequest.oneOf;
  assert.equal(checkpointVariants.length, 2);
  assert.deepEqual(checkpointVariants.map((variant) => variant.properties.action), [
    { const: "approve" }, { enum: ["request_changes", "reject"] },
  ]);
  for (const variant of checkpointVariants) {
    assert.equal(variant.additionalProperties, false);
    for (const required of ["action", "scopeDigest", "policyDigest"]) assert.ok(variant.required.includes(required));
  }
  assert.equal(checkpointVariants[0].required.includes("comment"), false);
  assert.ok(checkpointVariants[1].required.includes("comment"));
  assert.equal(checkpointVariants[1].properties.comment.minLength, 1);
  const permissionRequest = api.paths["/api/v1/agents/permissions/{permissionRequestId}/decisions"].post.requestBody.content["application/json"].schema;
  assert.equal(permissionRequest.$ref, "#/components/schemas/ProviderPermissionDecisionRequest");
  const permissionDecision = api.components.schemas.ProviderPermissionDecisionRequest;
  assert.equal(permissionDecision.additionalProperties, false);
  assert.deepEqual(permissionDecision.required, ["decision", "scopeDigest"]);
  assert.deepEqual(permissionDecision.properties.decision.enum, ["allow_once", "deny", "cancel"]);
  assert.equal(api.components.schemas.Health.properties.recovery.$ref, "#/components/schemas/RecoveryStatus");
  assert.equal(api.components.schemas.Health.required.includes("recovery"), false);
  assert.deepEqual(api.components.schemas.RecoveryStatus.required, ["reconciled", "reconcileRequired"]);
});

test("event and error schemas are strict", () => {
  for (const name of ["runtime-event-v1alpha1.schema.json","api-error-v1alpha1.schema.json"]) {
    const schema = JSON.parse(readFileSync(resolve(root, "schemas", name), "utf8"));
    assert.equal(schema.additionalProperties, false, name);
    assert.equal(schema.$schema, "https://json-schema.org/draft/2020-12/schema");
  }
  const eventSchema = JSON.parse(readFileSync(resolve(root, "schemas", "runtime-event-v1alpha1.schema.json"), "utf8"));
  assert.equal(eventSchema.properties.streamId, undefined);
  assert.equal(eventSchema.properties.streamSequence, undefined);
});

test("SQLite logical model contains append, idempotency, coordination, outbox, and projection boundaries", () => {
  const sql = readFileSync(resolve(root, "schemas/sqlite-v1alpha1.sql"), "utf8");
  for (const table of ["events","commands","outbox","run_projection","node_projection","attempt_projection","approval_projection","external_refs","projection_checkpoints","lease_scopes","leases","queue_entries","recovery_decisions"])
    assert.match(sql, new RegExp(`CREATE TABLE ${table} \\(`));
  assert.doesNotMatch(sql, /stream_id|stream_sequence|aggregate_type TEXT NOT NULL REFERENCES/);
  assert.match(sql, /UNIQUE\(aggregate_id, aggregate_revision\)/);
  assert.match(sql, /UNIQUE INDEX events_aggregate_command ON events\(aggregate_id, command_id\)/);
  assert.match(sql, /PRIMARY KEY\(scope, idempotency_key\)/);
});

test("startup recovery decisions are closed, atomic, and append-only", () => {
  const logical = readFileSync(resolve(root, "schemas/sqlite-v1alpha1.sql"), "utf8").replaceAll("\r\n", "\n");
  const migration = readFileSync(resolve(root, "runtime/src/adapters/statestore/sqlite/migrations/0005_startup_recovery.sql"), "utf8").replaceAll("\r\n", "\n");
  for (const sql of [logical, migration]) {
    assert.match(sql, /'adopt'.*'resume'.*'retry'.*'interrupt'.*'reconcile_required'/s);
    assert.match(sql, /PRIMARY KEY\(startup_id, subject_kind, subject_id\)/);
    assert.match(sql, /recovery_decisions_reject_update/);
    assert.match(sql, /recovery_decisions_reject_delete/);
  }
});

test("SQLite coordination model fences one repository writer and orders its durable queue", () => {
  const logical = readFileSync(resolve(root, "schemas/sqlite-v1alpha1.sql"), "utf8").replaceAll("\r\n", "\n");
  const migration = readFileSync(resolve(root, "runtime/src/adapters/statestore/sqlite/migrations/0004_leases_queues.sql"), "utf8").replaceAll("\r\n", "\n");
  for (const sql of [logical, migration]) {
    assert.match(sql, /last_fencing_token INTEGER NOT NULL/);
    assert.match(sql, /WHERE state <> 'released'/);
    assert.match(sql, /priority DESC, enqueued_at, item_id/);
    assert.match(sql, /'held'.*'releasing'.*'released'.*'reconcile_required'/s);
  }
});

test("SQLite logical model is advanced by a forward state-constraint migration", () => {
  const logical = readFileSync(resolve(root, "schemas/sqlite-v1alpha1.sql"), "utf8").replaceAll("\r\n", "\n");
  const migration = readFileSync(resolve(root, "runtime/src/adapters/statestore/sqlite/migrations/0002_constrain_state.sql"), "utf8").replaceAll("\r\n", "\n");
  for (const state of ["pending", "completed", "prepared", "leased", "committed", "reconcile_required"])
    assert.match(`${logical}\n${migration}`, new RegExp(`'${state}'`));
  assert.match(migration, /DROP TABLE events/);
});
