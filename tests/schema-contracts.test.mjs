import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { checkCatalog, compareContracts, loadContracts, validateContracts } from "../scripts/schema-tool.mjs";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");

function contracts(documents) {
  return new Map(Object.entries(documents).map(([name, document]) => [name, { name, document, text: JSON.stringify(document) }]));
}

test("all versioned contracts are structurally valid and references resolve", () => {
  assert.deepEqual(validateContracts(loadContracts(resolve(root, "schemas"))), []);
});

test("generated schema catalog is deterministic and current", () => {
  assert.equal(checkCatalog(loadContracts(resolve(root, "schemas"))).matches, true);
});

test("v1 run evolution preserves legacy operations and models new requests as a distinct variant", () => {
  const api = JSON.parse(readFileSync(resolve(root, "schemas", "openapi-v1alpha1.json"), "utf8"));
  const runs = api.paths["/api/v1/runs"];

  assert.equal(runs.get.operationId, "listRuns");
  assert.ok(runs.post.responses["201"]);
  assert.ok(runs.post.responses["202"]);
  assert.deepEqual(
    runs.post.requestBody.content["application/json"].schema.oneOf.map((variant) => variant.$ref),
    ["#/components/schemas/CreateRunRequest", "#/components/schemas/StartFakeRunRequest"]
  );
  assert.equal(api.components.schemas.Health.required.includes("recovery"), false);
  assert.equal(api.components.schemas.ApiRoot.required.includes("recovery"), false);
  assert.equal(api.components.schemas.Run.required.includes("lastGlobalPosition"), false);
});

test("provider and artifact boundaries are published as strict schemas", () => {
  for (const name of ["provider-v1alpha1.schema.json", "artifact-v1alpha1.schema.json"]) {
    const schema = JSON.parse(readFileSync(resolve(root, "schemas", name), "utf8"));
    assert.equal(schema.$schema, "https://json-schema.org/draft/2020-12/schema");
    assert.ok(Object.values(schema.$defs).filter((definition) => definition.type === "object").every((definition) => definition.additionalProperties === false));
  }
});

test("provider state combinations are encoded as tagged variants", () => {
  const schema = JSON.parse(readFileSync(resolve(root, "schemas", "provider-v1alpha1.schema.json"), "utf8"));
  assert.equal(schema.$defs.health.properties.authenticated, undefined);
  assert.equal(schema.$defs.capability.oneOf.length, 2);
  assert.deepEqual(schema.$defs.capability.oneOf.map((variant) => variant.properties.state.const), ["available", "unavailable"]);
  assert.equal(schema.$defs.interactionResponse.oneOf.length, 2);
  assert.equal(schema.$defs.attemptResult.oneOf.length, 3);
  assert.deepEqual(schema.$defs.input.properties.detail.enum, ["auto", "low", "high", "original"]);
});

test("provider events publish the normalized adapter vocabulary", () => {
  const schema = JSON.parse(readFileSync(resolve(root, "schemas", "provider-v1alpha1.schema.json"), "utf8"));
  assert.deepEqual(schema.$defs.providerEvent.properties.eventKind["x-darkstar-canonicalValues"], [
    "attempt.started", "attempt.waiting", "attempt.completed", "attempt.failed", "attempt.cancelled",
    "turn.started", "turn.completed", "turn.interrupted",
    "message.delta", "message.completed", "plan.updated", "structured_output.completed",
    "command.started", "command.output", "command.completed", "file_change.started", "file_change.completed", "tool.started", "tool.completed",
    "permission.requested", "permission.response_recorded", "user_input.requested", "user_input.response_recorded",
    "usage.updated", "warning", "error", "unknown.provider_event"
  ]);
  assert.equal(schema.$defs.providerEvent.properties.eventKind.enum, undefined);
});

test("artifact provenance and context order have one source of truth", () => {
  const schema = JSON.parse(readFileSync(resolve(root, "schemas", "artifact-v1alpha1.schema.json"), "utf8"));
  assert.deepEqual(schema.$defs.provenance.oneOf.map((variant) => variant.properties.origin.const), ["attempt", "operation"]);
  for (const field of ["version", "producer", "roles", "tags", "metadata"])
    assert.ok(schema.$defs.artifact.properties[field], `artifact is missing ${field}`);
  assert.equal(schema.$defs.contextEntry.properties.order, undefined);
  assert.equal(schema.$defs.contextEntry.required.includes("order"), false);
  assert.ok(schema.$defs.contextEntry.properties.artifactVersion);
  for (const field of ["instructions", "schemas", "permissions", "workspace", "capabilities", "reservedTokens"])
    assert.ok(schema.$defs.contextManifest.properties[field], `context manifest is missing ${field}`);
});

test("late-evidence impact uses closed coverage and proposal variants", () => {
  const schema = JSON.parse(readFileSync(resolve(root, "schemas", "artifact-v1alpha1.schema.json"), "utf8"));
  assert.deepEqual(
    schema.$defs.impactProposal.oneOf.map((variant) => variant.properties.action.const),
    ["continue", "refresh", "revise", "insert", "invalidate"]
  );
  assert.deepEqual(schema.$defs.attemptCoverage.oneOf[0].properties.state.enum, ["supplied", "not_supplied"]);
  assert.deepEqual(schema.$defs.attemptCoverage.oneOf[1].properties.state.enum, ["pending_freeze", "unavailable"]);
  assert.equal(schema.$defs.impactAssessment.properties.sawLateEvidence, undefined);
});

test("compatibility permits additive optional fields", () => {
  const before = { $schema: "https://json-schema.org/draft/2020-12/schema", type: "object", additionalProperties: false, properties: { id: { type: "string" } } };
  const after = structuredClone(before);
  after.properties.label = { type: "string" };
  assert.deepEqual(compareContracts(contracts({ "sample-v1.schema.json": before }), contracts({ "sample-v1.schema.json": after })), []);
});

test("compatibility rejects removal, narrowing, and new requirements", () => {
  const before = {
    $schema: "https://json-schema.org/draft/2020-12/schema",
    type: "object",
    additionalProperties: false,
    required: ["state"],
    properties: { state: { enum: ["ready", "waiting"] }, note: { type: ["string", "null"] } }
  };
  const after = structuredClone(before);
  after.required.push("note");
  after.properties.state.enum = ["ready"];
  after.properties.note.type = "string";
  after.properties.note.minLength = 1;
  const issues = compareContracts(contracts({ "sample-v1.schema.json": before }), contracts({ "sample-v1.schema.json": after }));
  assert.ok(issues.some((issue) => issue.includes("required property/properties added: note")));
  assert.ok(issues.some((issue) => issue.includes("enum value(s) removed")));
  assert.ok(issues.some((issue) => issue.includes("accepted type(s) removed: null")));
  assert.ok(issues.some((issue) => issue.includes("minLength became more restrictive")));
});

test("compatibility rejects newly introduced constraints", () => {
  const before = { $schema: "https://json-schema.org/draft/2020-12/schema" };
  const after = { ...before, type: "string", pattern: "^[a-z]+$", minLength: 1 };
  const issues = compareContracts(contracts({ "sample-v1.schema.json": before }), contracts({ "sample-v1.schema.json": after }));
  assert.ok(issues.some((issue) => issue.includes("type constraint was added")));
  assert.ok(issues.some((issue) => issue.includes("pattern constraint was added")));
  assert.ok(issues.some((issue) => issue.includes("minLength became more restrictive")));
});

test("compatibility rejects removing a versioned contract or API operation", () => {
  const schema = { $schema: "https://json-schema.org/draft/2020-12/schema", type: "object" };
  assert.match(compareContracts(contracts({ "sample-v1.schema.json": schema }), contracts({}))[0], /versioned contract file was removed/);

  const beforeApi = { openapi: "3.1.0", paths: { "/api/v1/items": { get: { operationId: "listItems", responses: { "200": { description: "ok" } } } } }, components: { schemas: {} } };
  const afterApi = { ...beforeApi, paths: {} };
  assert.ok(compareContracts(contracts({ "openapi-v1.json": beforeApi }), contracts({ "openapi-v1.json": afterApi })).some((issue) => issue.includes("API path removed")));
});
