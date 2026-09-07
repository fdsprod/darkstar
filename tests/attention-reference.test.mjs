import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const root = new URL("../", import.meta.url);

test("unified attention API is a closed five-variant projection beside the legacy queue", async () => {
  const api = JSON.parse(await readFile(new URL("schemas/openapi-v1alpha1.json", root), "utf8"));
  assert.equal(api.paths["/api/v1/attention"].get.operationId, "listAttention");
  assert.equal(api.paths["/api/v1/checkpoints"].get.operationId, "listCheckpoints");
  assert.equal(api.paths["/api/v1/checkpoints"].get.responses["200"].content["application/json"].schema.$ref, "#/components/schemas/ArtifactCheckpointQueue");
  assert.deepEqual(api.components.schemas.AttentionKind.enum, ["workflow_checkpoint", "input_required", "provider_permission", "workflow_control", "external_delivery"]);
  assert.deepEqual(api.components.schemas.AttentionCheckpoint.oneOf.map((value) => value.$ref), [
    "#/components/schemas/WorkflowCheckpointAttention",
    "#/components/schemas/InputRequiredAttention",
    "#/components/schemas/ProviderPermissionAttention",
    "#/components/schemas/WorkflowControlAttention",
    "#/components/schemas/ExternalDeliveryAttention",
  ]);
});

test("attention siblings expose only their class-specific subject and action vocabulary", async () => {
  const api = JSON.parse(await readFile(new URL("schemas/openapi-v1alpha1.json", root), "utf8"));
  const schemas = api.components.schemas;
  const expected = {
    WorkflowCheckpointAttention: ["approve", "request_changes", "reject"],
    InputRequiredAttention: ["answer", "retry_delivery"],
    ProviderPermissionAttention: ["allow_once", "deny", "cancel", "retry_delivery"],
    WorkflowControlAttention: ["approve", "deny", "cancel"],
    ExternalDeliveryAttention: ["approve", "deny", "cancel"],
  };
  for (const [name, actions] of Object.entries(expected)) {
    assert.deepEqual(schemas[name].properties.allowedActions.items.enum, actions, name);
    assert.equal(schemas[name].additionalProperties, false, name);
    assert.equal(schemas[name].properties.subject.additionalProperties, false, `${name} subject`);
  }
  assert.ok(!("candidateArtifactId" in schemas.ProviderPermissionAttention.properties.subject.properties));
  assert.deepEqual(schemas.ProviderPermissionAttention.properties.subject.required, ["attemptId", "nodeId", "providerThreadId", "providerTurnId", "providerRequestId", "interactionKind", "scope", "scopeDigest", "policyDigest", "evidence", "status"]);
  assert.deepEqual(schemas.ProviderPermissionAttention.properties.subject.properties.status.enum, ["pending", "decision_recorded"]);
  assert.equal(schemas.ProviderPermissionAttention.properties.subject.properties.scope.$ref, "#/components/schemas/ProviderInteractionScope");
  assert.equal(schemas.ProviderPermissionAttention.properties.subject.properties.evidence.$ref, "#/components/schemas/ProviderPermissionEvidence");
  assert.ok(!("providerRequestId" in schemas.WorkflowControlAttention.properties.subject.properties));
  assert.ok(!("policyDigest" in schemas.InputRequiredAttention.properties.subject.properties));
});
