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
  assert.ok(api.paths["/api/v1/approvals/{approvalId}/decisions"].post.requestBody.content["application/json"].schema.properties.action.enum.includes("allow_once"));
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

test("SQLite logical model contains append, idempotency, outbox, and projection boundaries", () => {
  const sql = readFileSync(resolve(root, "schemas/sqlite-v1alpha1.sql"), "utf8");
  for (const table of ["events","commands","outbox","run_projection","approval_projection","external_refs","projection_checkpoints"])
    assert.match(sql, new RegExp(`CREATE TABLE ${table} \\(`));
  assert.match(sql, /UNIQUE\(stream_id, stream_sequence\)/);
  assert.match(sql, /PRIMARY KEY\(scope, idempotency_key\)/);
});

test("SQLite logical model matches the executable initial migration", () => {
  const logical = readFileSync(resolve(root, "schemas/sqlite-v1alpha1.sql"), "utf8").replaceAll("\r\n", "\n");
  const executable = readFileSync(resolve(root, "runtime/src/adapters/statestore/sqlite/migrations/0001_initial.sql"), "utf8").replaceAll("\r\n", "\n");
  assert.equal(executable, logical);
});
