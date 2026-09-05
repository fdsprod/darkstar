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
  assert.ok(api.components.schemas.Run.properties.routeSnapshot);
  assert.equal(api.components.schemas.CreateRunRequest.additionalProperties, false);
  assert.deepEqual(api.components.schemas.WorkflowDocument.oneOf.map((variant) => variant.$ref), [
    "./workflow-v1alpha1.schema.json", "./workflow-v1alpha2.schema.json"
  ]);
  assert.ok(api.components.schemas.WorkflowGraph.properties.nodes.items.properties.type.enum.includes("point_execution"));
});

test("project and work command contracts publish exact request variants and aggregate views", () => {
  const api = JSON.parse(readFileSync(resolve(root, "schemas", "openapi-v1alpha1.json"), "utf8"));
  assert.equal(api.paths["/api/v1/projects"].post.operationId, "registerProject");
  assert.equal(api.paths["/api/v1/projects/{projectId}"].get.operationId, "getProject");
  assert.equal(api.paths["/api/v1/work-items"].post.operationId, "createWorkItem");
  assert.equal(api.paths["/api/v1/work-items/import"].post.operationId, "importWorkItem");
  assert.equal(api.components.schemas.ProjectRegistration.additionalProperties, false);
  assert.equal(api.components.schemas.CreateWorkItemRequest.additionalProperties, false);
  assert.equal(api.components.schemas.ImportWorkItemRequest.additionalProperties, false);
  assert.deepEqual(api.components.schemas.WorkItem.properties.status.enum, ["open", "active", "completed", "cancelled"]);
});

test("run controls publish the complete optimistic-concurrency surface", () => {
  const api = JSON.parse(readFileSync(resolve(root, "schemas", "openapi-v1alpha1.json"), "utf8"));
  for (const action of ["pause", "resume", "retry", "continue", "cancel"]) {
    const operation = api.paths[`/api/v1/runs/{runId}/${action}`].post;
    assert.equal(operation.operationId, `${action}Run`);
    assert.deepEqual(operation.parameters.map((parameter) => parameter.$ref), [
      "#/components/parameters/RunId", "#/components/parameters/IdempotencyKey", "#/components/parameters/IfMatch"
    ]);
    assert.equal(operation.responses["200"].content["application/json"].schema.$ref, "#/components/schemas/Run");
  }
  assert.equal(api.components.schemas.RetryRunRequest.additionalProperties, false);
  assert.deepEqual(api.components.schemas.ContinueRunRequest.required, ["until"]);
});

test("provider and artifact boundaries are published as strict schemas", () => {
  for (const name of ["provider-v1alpha1.schema.json", "artifact-v1alpha1.schema.json", "artifact-v1alpha2.schema.json"]) {
    const schema = JSON.parse(readFileSync(resolve(root, "schemas", name), "utf8"));
    assert.equal(schema.$schema, "https://json-schema.org/draft/2020-12/schema");
    assert.ok(Object.values(schema.$defs).filter((definition) => definition.type === "object").every((definition) => definition.additionalProperties === false));
  }
});

test("post-PR delivery evidence is a closed stage union", () => {
  const schema = JSON.parse(readFileSync(resolve(root, "schemas", "delivery-evidence-v1alpha1.schema.json"), "utf8"));
  assert.deepEqual(schema.oneOf.map((variant) => variant.$ref), [
    "#/$defs/reviewCi", "#/$defs/releaseReadiness", "#/$defs/release", "#/$defs/productionVerification"
  ]);
  assert.deepEqual(
    schema.oneOf.map((variant) => schema.$defs[variant.$ref.split("/").at(-1)].properties.evidenceType.const),
    ["review_ci", "release_readiness", "release", "production_verification"]
  );
  for (const variant of schema.oneOf) {
    const contract = schema.$defs[variant.$ref.split("/").at(-1)];
    assert.equal(contract.additionalProperties, false);
    assert.ok(contract.required.includes("schemaVersion"));
    assert.ok(contract.required.includes("status") || contract.required.includes("decision"));
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
  assert.deepEqual(schema.$defs.health.properties.authentication.enum, ["authenticated", "unauthenticated", "unknown"]);
  assert.deepEqual(schema.$defs.health.properties.usage.enum, ["ready", "exhausted", "unknown"]);
  assert.ok(schema.$defs.health.properties.state.enum.includes("usage_exhausted"));
});

test("doctor provider details separate available and unavailable capabilities", () => {
  const schema = JSON.parse(readFileSync(resolve(root, "schemas", "health-report-v1alpha1.schema.json"), "utf8"));
  const details = schema.$defs.providerDetails;
  assert.ok(details.required.includes("authentication"));
  assert.ok(details.required.includes("usage"));
  assert.ok(details.required.includes("instructionSources"));
  assert.ok(details.required.includes("conflictingExecutables"));
  assert.equal(details.properties.availableCapabilities.items.$ref, "#/$defs/availableCapability");
  assert.equal(details.properties.unavailableCapabilities.items.$ref, "#/$defs/unavailableCapability");
  assert.equal(schema.$defs.healthyCheck.properties.providerDetails.$ref, "#/$defs/providerDetails");
  assert.equal(schema.$defs.findingCheck.properties.providerDetails.$ref, "#/$defs/providerDetails");
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
  const schema = JSON.parse(readFileSync(resolve(root, "schemas", "artifact-v1alpha2.schema.json"), "utf8"));
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
  const schema = JSON.parse(readFileSync(resolve(root, "schemas", "artifact-v1alpha2.schema.json"), "utf8"));
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

test("OpenAPI compatibility permits new required response fields but not request or shared fields", () => {
  const before = {
    openapi: "3.1.0",
    paths: {
      "/api/v1/items": {
        post: {
          parameters: [{ in: "header", name: "X-Shared", schema: { $ref: "#/components/schemas/Shared" } }],
          requestBody: { $ref: "#/components/requestBodies/CreateItem" },
          responses: {
            "200": { $ref: "#/components/responses/ItemReport" },
            "201": { description: "shared", content: { "application/json": { schema: { $ref: "#/components/schemas/Shared" } } } }
          }
        }
      }
    },
    components: {
      requestBodies: {
        CreateItem: { content: { "application/json": { schema: { $ref: "#/components/schemas/RequestEnvelope" } } } }
      },
      responses: {
        ItemReport: { description: "ok", content: { "application/json": { schema: { $ref: "#/components/schemas/ResponseEnvelope" } } } }
      },
      schemas: {
        RequestEnvelope: {
          type: "object",
          required: ["item"],
          properties: { item: { $ref: "#/components/schemas/RequestItem" } }
        },
        RequestItem: {
          type: "object",
          required: ["id"],
          properties: { id: { type: "string" }, note: { type: "string" } }
        },
        ResponseEnvelope: {
          type: "object",
          required: ["item"],
          properties: { item: { $ref: "#/components/schemas/ResponseItem" } }
        },
        ResponseItem: {
          type: "object",
          required: ["id"],
          properties: { id: { type: "string" }, documentDigest: { type: "string" } }
        },
        Shared: {
          type: "object",
          required: ["id"],
          properties: { id: { type: "string" }, revision: { type: "integer" } }
        },
        Unreferenced: {
          type: "object",
          properties: { owner: { type: "string" } }
        }
      }
    }
  };
  const after = structuredClone(before);
  after.components.schemas.RequestItem.required.push("note");
  after.components.schemas.ResponseItem.required.push("documentDigest");
  after.components.schemas.Shared.required.push("revision");
  after.components.schemas.Unreferenced.required = ["owner"];

  const issues = compareContracts(contracts({ "openapi-v1.json": before }), contracts({ "openapi-v1.json": after }));
  assert.ok(issues.some((issue) => issue.includes("RequestItem") && issue.includes("required property/properties added: note")));
  assert.equal(issues.some((issue) => issue.includes("ResponseItem") && issue.includes("documentDigest")), false);
  assert.ok(issues.some((issue) => issue.includes("Shared") && issue.includes("required property/properties added: revision")));
  assert.ok(issues.some((issue) => issue.includes("Unreferenced") && issue.includes("required property/properties added: owner")));
});

test("OpenAPI response direction propagates through schema-fragment references and permits safe narrowing", () => {
  const before = {
    openapi: "3.1.0",
    paths: {
      "/api/v1/report": {
        get: {
          responses: {
            "200": {
              description: "ok",
              content: { "application/json": { schema: { $ref: "#/components/schemas/Report/properties/body" } } }
            }
          }
        }
      }
    },
    components: {
      schemas: {
        Report: {
          type: "object",
          properties: {
            body: {
              type: "object",
              required: ["state"],
              properties: { state: { enum: ["ready", "waiting"] }, digest: { type: "string" } }
            }
          }
        }
      }
    }
  };
  const after = structuredClone(before);
  after.components.schemas.Report.properties.body.required.push("digest");
  after.components.schemas.Report.properties.body.properties.state.enum = ["ready"];

  const issues = compareContracts(contracts({ "openapi-v1.json": before }), contracts({ "openapi-v1.json": after }));
  assert.equal(issues.some((issue) => issue.includes("required property/properties added: digest")), false);
  assert.equal(issues.some((issue) => issue.includes("enum value(s) removed")), false);
  assert.deepEqual(issues, []);
});

test("OpenAPI response compatibility rejects widened outputs and required-field removal", () => {
  const before = {
    openapi: "3.1.0",
    paths: {
      "/api/v1/report": {
        get: {
          responses: {
            "200": { description: "ok", content: { "application/json": { schema: { $ref: "#/components/schemas/Report" } } } }
          }
        }
      }
    },
    components: {
      schemas: {
        Report: {
          type: "object",
          additionalProperties: false,
          required: ["state", "kind", "value", "removable"],
          properties: {
            state: { type: "string", enum: ["ready", "waiting"] },
            kind: { type: "string", const: "report" },
            value: { type: "string" },
            removable: { type: "string" },
            optionalGone: { type: "string" }
          }
        }
      }
    }
  };
  const after = structuredClone(before);
  after.components.schemas.Report.properties.state.enum.push("unknown");
  delete after.components.schemas.Report.properties.kind.const;
  after.components.schemas.Report.properties.value.type = ["string", "null"];
  after.components.schemas.Report.required = after.components.schemas.Report.required.filter((name) => name !== "removable");
  after.components.schemas.Report.additionalProperties = true;
  delete after.components.schemas.Report.properties.optionalGone;
  after.components.schemas.Report.properties.added = { type: "string" };

  const issues = compareContracts(contracts({ "openapi-v1.json": before }), contracts({ "openapi-v1.json": after }));
  assert.ok(issues.some((issue) => issue.includes("output enum value(s) added: \"unknown\"")));
  assert.ok(issues.some((issue) => issue.includes("output const constraint was removed or changed")));
  assert.ok(issues.some((issue) => issue.includes("output type(s) added: null")));
  assert.ok(issues.some((issue) => issue.includes("required response property/properties removed: removable")));
  assert.ok(issues.some((issue) => issue.includes("additional properties became more permissive for responses")));
  assert.equal(issues.some((issue) => issue.includes("property removed: optionalGone")), false);
  assert.equal(issues.some((issue) => issue.includes("properties/added")), false);
});

test("OpenAPI response compatibility permits narrowing, new required fields, and optional-property removal", () => {
  const before = {
    openapi: "3.1.0",
    paths: {
      "/api/v1/report": {
        get: {
          responses: {
            "200": { description: "ok", content: { "application/json": { schema: { $ref: "#/components/schemas/Report" } } } }
          }
        }
      }
    },
    components: {
      schemas: {
        Report: {
          type: "object",
          required: ["state"],
          properties: {
            state: { type: ["string", "null"], enum: ["ready", "waiting", null] },
            category: { type: "string" },
            optionalGone: { type: "string" }
          }
        }
      }
    }
  };
  const after = structuredClone(before);
  after.components.schemas.Report.additionalProperties = false;
  after.components.schemas.Report.required.push("category");
  after.components.schemas.Report.properties.state.type = "string";
  after.components.schemas.Report.properties.state.enum = ["ready"];
  after.components.schemas.Report.properties.category.const = "summary";
  after.components.schemas.Report.properties.category.minLength = 3;
  delete after.components.schemas.Report.properties.optionalGone;
  after.components.schemas.Report.properties.added = { type: "string" };

  assert.deepEqual(compareContracts(contracts({ "openapi-v1.json": before }), contracts({ "openapi-v1.json": after })), []);
});

test("OpenAPI request and shared schemas retain consumer safety and become invariant when shared", () => {
  const before = {
    openapi: "3.1.0",
    paths: {
      "/api/v1/items": {
        post: {
          requestBody: { content: { "application/json": { schema: { $ref: "#/components/schemas/Input" } } } },
          responses: {
            "200": { description: "ok", content: { "application/json": { schema: { $ref: "#/components/schemas/Shared" } } } }
          },
          parameters: [{ in: "query", name: "shared", schema: { $ref: "#/components/schemas/Shared" } }]
        }
      }
    },
    components: {
      schemas: {
        Input: {
          type: "object",
          additionalProperties: false,
          required: ["state"],
          properties: { state: { enum: ["ready", "waiting"] }, legacy: { type: "string" }, added: { type: "string" } }
        },
        Shared: { type: "string", enum: ["ready", "waiting"] }
      }
    }
  };
  const narrowedRequest = structuredClone(before);
  narrowedRequest.components.schemas.Input.required.push("added");
  narrowedRequest.components.schemas.Input.properties.state.enum = ["ready"];
  delete narrowedRequest.components.schemas.Input.properties.legacy;
  const requestIssues = compareContracts(contracts({ "openapi-v1.json": before }), contracts({ "openapi-v1.json": narrowedRequest }));
  assert.ok(requestIssues.some((issue) => issue.includes("required property/properties added: added")));
  assert.ok(requestIssues.some((issue) => issue.includes("enum value(s) removed")));
  assert.ok(requestIssues.some((issue) => issue.includes("property removed: legacy")));

  const widenedRequest = structuredClone(before);
  widenedRequest.components.schemas.Input.properties.state.enum.push("unknown");
  widenedRequest.components.schemas.Input.required = [];
  assert.deepEqual(compareContracts(contracts({ "openapi-v1.json": before }), contracts({ "openapi-v1.json": widenedRequest })), []);

  const narrowedShared = structuredClone(before);
  narrowedShared.components.schemas.Shared.enum = ["ready"];
  assert.ok(compareContracts(contracts({ "openapi-v1.json": before }), contracts({ "openapi-v1.json": narrowedShared })).some((issue) => issue.includes("enum value(s) removed")));

  const widenedShared = structuredClone(before);
  widenedShared.components.schemas.Shared.enum.push("unknown");
  assert.ok(compareContracts(contracts({ "openapi-v1.json": before }), contracts({ "openapi-v1.json": widenedShared })).some((issue) => issue.includes("output enum value(s) added")));
});

test("OpenAPI schema direction traversal terminates cycles and preserves nested producer variance", () => {
  const before = {
    openapi: "3.1.0",
    paths: {
      "/api/v1/tree": {
        get: {
          responses: {
            "200": { description: "ok", content: { "application/json": { schema: { $ref: "#/components/schemas/Branch" } } } }
          }
        }
      }
    },
    components: {
      schemas: {
        Branch: { type: "object", properties: { leaf: { $ref: "#/components/schemas/Leaf" } } },
        Leaf: { type: "object", properties: { parent: { $ref: "#/components/schemas/Branch" }, value: { type: "string" } } }
      }
    }
  };
  const after = structuredClone(before);
  after.components.schemas.Leaf.properties.value.type = ["string", "null"];
  const issues = compareContracts(contracts({ "openapi-v1.json": before }), contracts({ "openapi-v1.json": after }));
  assert.ok(issues.some((issue) => issue.includes("Leaf") && issue.includes("output type(s) added: null")));
});

test("compatibility permits preserving a published schema as an explicit version alternative", () => {
  const before = { $ref: "./workflow-v1alpha1.schema.json" };
  const after = { oneOf: [before, { $ref: "./workflow-v1alpha2.schema.json" }] };
  assert.deepEqual(compareContracts(contracts({ "sample-v1.schema.json": before }), contracts({ "sample-v1.schema.json": after })), []);
});

test("compatibility permits an exact corrective promotion to a newer contract version", () => {
  const mutated = { $schema: "https://json-schema.org/draft/2020-12/schema", $id: "https://example.test/sample-v1alpha1.schema.json", title: "Sample v1alpha1", type: "string", minLength: 2 };
  const restored = { ...mutated, minLength: 3 };
  const promoted = { ...mutated, $id: "https://example.test/sample-v1alpha2.schema.json", title: "Sample v1alpha2" };
  const before = contracts({ "sample-v1alpha1.schema.json": mutated });
  const after = contracts({ "sample-v1alpha1.schema.json": restored, "sample-v1alpha2.schema.json": promoted });
  assert.deepEqual(compareContracts(before, after), []);

  promoted.minLength = 3;
  assert.ok(compareContracts(before, after).some((issue) => issue.includes("minLength")));
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
