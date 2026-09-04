import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

import { buildCheckpointDecisionRequest, buildInputAnswerRequest, checkpointActionKey, checkpointActionPresentation, checkpointRoundChanged, orderedCheckpointHistory, parseJSONAnswer, selectedCheckpointRound } from "../src/pages/checkpointModel.ts";

function round(overrides = {}) {
  return { approvalId: "approval_1", checkpointId: "checkpoint_1", revision: 1, state: "pending", allowedActions: ["approve", "request_changes", "reject"], scopeDigest: "scope", policyDigest: "policy", resourceVersion: 3, ...overrides };
}

test("checkpoint decisions are bound to the exact pending round and server-allowed action", () => {
  assert.deepEqual(buildCheckpointDecisionRequest(round(), "approve", " Reviewed. "), { action: "approve", scopeDigest: "scope", policyDigest: "policy", comment: "Reviewed." });
  assert.deepEqual(buildCheckpointDecisionRequest(round(), "approve", "  "), { action: "approve", scopeDigest: "scope", policyDigest: "policy" });
  assert.deepEqual(buildCheckpointDecisionRequest(round(), "request_changes", " Add evidence. "), { action: "request_changes", scopeDigest: "scope", policyDigest: "policy", comment: "Add evidence." });
  assert.equal(checkpointActionKey(round(), "reject"), "approval_1:3:reject");
  assert.throws(() => buildCheckpointDecisionRequest(round({ allowedActions: ["approve"] }), "reject", "No."), /currently allowed/);
  assert.throws(() => buildCheckpointDecisionRequest(round({ state: "approved", allowedActions: [] }), "approve", "Again."), /terminal decision/);
  assert.throws(() => buildCheckpointDecisionRequest(round(), "request_changes", " "), /explanatory comment/);
});

test("checkpoint action copy treats effects as intent and labels destructive rejection", () => {
  assert.match(checkpointActionPresentation("request_changes").description, /not claimed until it appears durably/);
  assert.equal(checkpointActionPresentation("reject").destructive, true);
  assert.match(checkpointActionPresentation("approve").description, /Record intent/);
});

test("checkpoint history is copied into stable revision order and rejects duplicate rounds", () => {
  const values = [round({ approvalId: "approval_2", revision: 2 }), round()];
  assert.deepEqual(orderedCheckpointHistory(values).map(({ revision }) => revision), [1, 2]);
  assert.deepEqual(values.map(({ revision }) => revision), [2, 1]);
  assert.throws(() => orderedCheckpointHistory([round(), round({ approvalId: "approval_other" })]), /duplicate revision/);
  assert.equal(selectedCheckpointRound(values, "approval_1").revision, 1);
  assert.equal(selectedCheckpointRound(values).revision, 2);
});

test("resource, digest, identity, revision, and state changes require checkpoint rereview", () => {
  const value = round();
  assert.equal(checkpointRoundChanged(value, { ...value }), false);
  assert.equal(checkpointRoundChanged(value, { ...value, resourceVersion: 4 }), true);
  assert.equal(checkpointRoundChanged(value, { ...value, scopeDigest: "new" }), true);
  assert.equal(checkpointRoundChanged(value, { ...value, state: "approved" }), true);
});

test("input answers accept one exact JSON value only while pending", () => {
  const input = { status: "pending", scopeDigest: "digest", request: { questions: [{ id: "choice", prompt: "Continue?", options: ["yes", "no"], schema: { type: "string", allowedValues: ["yes", "no"] } }] } };
  const envelope = { answers: { choice: { answers: "yes" } } };
  assert.deepEqual(buildInputAnswerRequest(input, JSON.stringify(envelope)), { scopeDigest: "digest", answer: envelope });
  assert.throws(() => parseJSONAnswer("null"), /answers object/);
  assert.throws(() => parseJSONAnswer(""), /Enter a JSON answer/);
  assert.throws(() => parseJSONAnswer("yes"), /valid JSON/);
  assert.throws(() => buildInputAnswerRequest(input, '{"answers":{"choice":{"answers":"other"}}}'), /recorded option/);
  assert.throws(() => buildInputAnswerRequest({ ...input, status: "answer_recorded" }, JSON.stringify(envelope)), /awaiting authoritative delivery/);
  assert.throws(() => buildInputAnswerRequest({ ...input, status: "answered" }, JSON.stringify(envelope)), /already been delivered/);
});

test("generated checkpoint and input contracts are typed, closed, and version fenced by the client", async () => {
  const [generated, client] = await Promise.all([
    readFile(new URL("../src/api/schema.generated.ts", import.meta.url), "utf8"),
    readFile(new URL("../src/api/client.ts", import.meta.url), "utf8"),
  ]);
  assert.match(generated, /"ArtifactCheckpointRound": \{[^\n]+"allowedActions": Array<"approve" \| "request_changes" \| "reject">/);
  assert.match(generated, /"InputRequest": \{[^\n]+"allowedActions": Array<"answer" \| "retry_delivery">/);
  assert.match(generated, /"listCheckpoints": \{ method: "GET"; path: "\/api\/v1\/checkpoints"/);
  assert.match(generated, /"retryInputRequestDelivery": \{ method: "POST"; path: "\/api\/v1\/input-requests\/\{inputRequestId\}\/delivery-retries"/);
  assert.match(client, /decideApproval\(approvalId: string, resourceVersion: number, idempotencyKey: string, body: Schemas\["ArtifactCheckpointDecisionRequest"\]/);
  assert.match(client, /answerInputRequest\(inputRequestId: string, resourceVersion: number, idempotencyKey: string, body: Schemas\["InputRequestAnswerRequest"\]/);
  assert.match(client, /retryInputDelivery\(inputRequestId: string, resourceVersion: number, signal\?: AbortSignal\)/);
  assert.doesNotMatch(client, /retryInputDelivery\(inputRequestId: string, resourceVersion: number, idempotencyKey/);
});

test("checkpoint page renders only server actions and refreshes stale dialogs from SSE state", async () => {
  const [page, router] = await Promise.all([
    readFile(new URL("../src/pages/CheckpointsPage.tsx", import.meta.url), "utf8"),
    readFile(new URL("../src/app/router.tsx", import.meta.url), "utf8"),
  ]);
  assert.match(page, /round\.allowedActions\.map/);
  assert.match(page, /selected\.allowedActions\.map/);
  assert.match(page, /state\.cursor/);
  assert.match(page, /cause\.status === 409 \|\| cause\.status === 412/);
  assert.match(page, /Confirm rejection/);
  assert.match(page, /does not approve permissions or any artifact checkpoint/);
  assert.doesNotMatch(page, /setSelected\([^)]*state:/);
  assert.match(router, /case "checkpoints": return <CheckpointsPage/);
});
