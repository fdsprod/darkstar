import { expect, test } from "@playwright/test";

import { installEmptyControlPlane } from "./acceptance.fixtures";

const d = (value: string) => value.repeat(64).slice(0, 64);
const now = "2026-09-07T12:00:00Z";

test("two-revision HITL preserves anchored feedback and rejects a stale exact decision", async ({ page }) => {
  await installEmptyControlPlane(page);
  const texts = { 1: "Draft title\nThe risky sentence needs evidence.\n", 2: "Final title\nThe supported sentence includes evidence.\n" } as const;
  const rep = (version: 1 | 2) => ({ representationId: `rep_${version}`, artifact: { artifactId: "artifact_review", version }, representationKind: "text", processor: { name: "plain", version: "1", mediaTypes: ["text/plain"] }, mediaType: "text/plain; charset=utf-8", locator: `rep/${version}`, digest: d(String(version + 2)), size: new TextEncoder().encode(texts[version]).length, tokenEstimate: 12, truncated: false, disclosure: "raw", diagnostics: [], metadata: {}, createdAt: now });
  const artifact = (version: 1 | 2) => ({ artifact: { artifactId: "artifact_review", version, sourceKind: "generated", sourceName: `candidate-v${version}.md`, blobDigest: d(String(version)), size: new TextEncoder().encode(texts[version]).length, declaredMediaType: "text/markdown", detectedMediaType: "text/markdown", locator: `artifact/${version}`, sensitivity: "internal", trust: "untrusted", creator: "agent", status: "stored", producer: { name: "test-agent", version: "1" }, roles: ["deliverable"], tags: [], metadata: {}, provenance: { origin: "attempt", runId: "run_1", nodeId: "write", attemptId: `attempt_${version}`, operationId: `operation_${version}` }, createdAt: now }, freshness: "current", representations: [rep(version)] });
  const base = (id: string, revision: number, version: 1 | 2) => ({ schemaVersion: 1, id, checkpointId: "checkpoint_1", runId: "run_1", visitId: `visit_${revision}`, nodeId: "write", revision, candidate: { artifactId: "artifact_review", version }, candidateDigest: d(String(version)), scopeDigest: d("a"), policyDigest: d("b"), mode: "approve", maxRevisions: 3, revisionLimitReached: false, state: "awaiting_human", turns: [], affectedArtifacts: [], allowedActions: ["approve", "request_changes", "reject"], resourceVersion: revision, createdAt: now, updatedAt: now });
  let first: any = base("approval_1", 1, 1);
  let second: any = base("approval_2", 2, 2);
  let revisionReady = false;
  let staleOnce = true;
  let submittedBody: any;
  let finalDecision: any;

  await page.route("**/api/v1/artifacts", route => route.fulfill({ json: [artifact(2), artifact(1)] }));
  await page.route("**/api/v1/artifacts/artifact_review/representations**", route => { const version = Number(new URL(route.request().url()).searchParams.get("version")) as 1 | 2; return route.fulfill({ json: [rep(version)] }); });
  await page.route("**/api/v1/representations/rep_*/content", route => { const version = route.request().url().includes("rep_2") ? 2 : 1; return route.fulfill({ body: texts[version], headers: { "content-type": "text/plain; charset=utf-8", "x-darkstar-content-digest": `sha256=${rep(version).digest}` } }); });
  await page.route("**/api/v1/artifacts/artifact_review/diff**", route => route.fulfill({ json: { artifactId: "artifact_review", from: 1, to: 2, changed: ["content"], fromDigest: d("1"), toDigest: d("2"), representations: { from: ["rep_1"], to: ["rep_2"] }, textDiff: { status: "available", from: { artifact: { artifactId: "artifact_review", version: 1 }, representationId: "rep_1", digest: rep(1).digest, representationKind: "text", mediaType: rep(1).mediaType, disclosure: "raw" }, to: { artifact: { artifactId: "artifact_review", version: 2 }, representationId: "rep_2", digest: rep(2).digest, representationKind: "text", mediaType: rep(2).mediaType, disclosure: "raw" }, policy: { algorithm: "darkstar-line-dp/v1", contextLines: 3, maxInputBytes: 2097152, maxWorkUnits: 1000000, pageSize: 100, maxPageBytes: 3145728 }, policyDigest: d("c"), resultDigest: d("d"), hunks: [{ fromStart: 1, toStart: 1, entries: [{ kind: "removed", fromLine: 1, text: "Draft title" }, { kind: "added", toLine: 1, text: "Final title" }] }], totalEntries: 2 } } }));
  await page.route("**/api/v1/review-sessions?**", route => route.fulfill({ json: { schemaVersion: 1, checkpointId: "checkpoint_1", sessions: revisionReady ? [first, second] : [first] } }));
  await page.route("**/api/v1/review-sessions/**", async route => {
    const path = new URL(route.request().url()).pathname;
    if (path === "/api/v1/review-sessions/approval_1/feedback-sets" && route.request().method() === "POST") { await route.fulfill({ json: { schemaVersion: 1, state: "draft", id: "feedback_1", approvalId: first.id, candidate: first.candidate, candidateDigest: first.candidateDigest, scopeDigest: first.scopeDigest, policyDigest: first.policyDigest, representation: { representationId: "rep_1", digest: rep(1).digest, disclosure: "raw" }, overallInstruction: "", annotations: [] } }); return; }
    if (path === "/api/v1/review-sessions/approval_1/feedback-sets/feedback_1/submit") {
      submittedBody = route.request().postDataJSON();
      const feedbackSet = { schemaVersion: 1, state: "submitted", id: "feedback_1", digest: d("e"), approvalId: first.id, ...submittedBody, author: { type: "user", id: "local" }, submittedAt: now, receipt: { eventId: "event_1", aggregateRevision: 2, commandId: "command_1" }, lineage: { checkpointId: "checkpoint_1", checkpointRevision: 1, runId: "run_1", attemptId: "attempt_2" } };
      first = { ...first, state: "awaiting_agent", allowedActions: [], resourceVersion: 2, turns: [{ kind: "human_feedback", sequence: 1, actor: { type: "user", id: "local" }, occurredAt: now, runId: "run_1", attemptId: "attempt_2", candidate: first.candidate, candidateDigest: first.candidateDigest, message: submittedBody.overallInstruction, feedbackSet }] };
      second = { ...second, turns: [...first.turns, { kind: "agent_response", sequence: 2, actor: { type: "provider", id: "agent" }, occurredAt: now, runId: "run_1", attemptId: "attempt_2", outcome: "revised", message: "Addressed review feedback.", resultingCandidate: second.candidate, resultingDigest: second.candidateDigest }] };
      revisionReady = true; await route.fulfill({ json: first }); return;
    }
    if (path === "/api/v1/review-sessions/approval_2/decisions") {
      finalDecision = route.request().postDataJSON();
      if (staleOnce) { staleOnce = false; second = { ...second, resourceVersion: 3 }; await route.fulfill({ status: 409, json: { schemaVersion: 1, code: "revision_conflict", message: "stale", requestId: "req", retryable: false } }); return; }
      second = { ...second, state: "approved", allowedActions: [], decision: { action: "approve", effect: "accept_candidate", actor: { type: "user", id: "local" }, comment: finalDecision.comment, decidedAt: now }, resourceVersion: 4 };
      await route.fulfill({ json: second }); return;
    }
    if (path === "/api/v1/review-sessions/approval_1") { await route.fulfill({ json: first }); return; }
    if (path === "/api/v1/review-sessions/approval_2") { await route.fulfill({ json: second }); return; }
    await route.fulfill({ status: 404, json: { schemaVersion: 1, code: "missing", message: path, requestId: "req", retryable: false } });
  });

  await page.goto("/checkpoints/approval_1/review?view=current");
  await expect(page.getByRole("heading", { level: 1, name: "Candidate revision 1" })).toBeVisible();
  const reader = page.locator("pre.annotation-reader");
  await expect(reader).toContainText("risky sentence");
  await reader.evaluate(element => { const node = element.firstChild!; const text = element.textContent!; const start = text.indexOf("risky sentence"); const range = document.createRange(); range.setStart(node, start); range.setEnd(node, start + "risky sentence".length); const selection = window.getSelection()!; selection.removeAllRanges(); selection.addRange(range); element.dispatchEvent(new MouseEvent("mouseup", { bubbles: true })); });
  await page.getByLabel(/Comment on selected text/).fill("Cite the source for this risk.");
  await page.getByRole("button", { name: "Add comment" }).click();
  await page.getByLabel("Overall instruction for candidate v1").fill("Revise the title and support the risk statement.");
  await page.getByRole("button", { name: "Submit feedback set · 1" }).click();
  await expect.poll(() => submittedBody?.annotations?.length).toBe(1);
  expect(submittedBody.annotations).toHaveLength(1);
  expect(submittedBody.annotations[0].anchor.quotedText).toBe("risky sentence");
  await page.goto("/checkpoints/approval_2/review?view=inline");
  await expect(page.getByRole("heading", { level: 1, name: "Candidate revision 2" })).toBeVisible();
  await expect(page.getByText("Addressed review feedback.")).toBeVisible();
  await expect(page.getByRole("navigation", { name: "Document view" }).getByRole("button", { name: "Inline diff" })).toHaveAttribute("aria-current", "page");
  await expect(page.getByLabel("Inline diff from revision 1 to 2")).toContainText("Final title");

  await page.getByLabel("Overall instruction for candidate v2").fill("Keep this approval note through a stale response.");
  await page.getByRole("button", { name: "Approve" }).click();
  await expect(page.locator("section.review-stale", { hasText: "Displayed decision binding is stale" })).toBeVisible();
  await expect(page.getByLabel("Overall instruction for candidate v2")).toHaveValue("Keep this approval note through a stale response.");
  await page.getByRole("button", { name: "Review refreshed state" }).click();
  await page.getByRole("button", { name: "Approve" }).click();
  await expect(page.getByText(/approve was durably recorded/i)).toBeVisible();
  expect(finalDecision).toMatchObject({ action: "approve", candidateDigest: d("2"), scopeDigest: d("a"), policyDigest: d("b") });
});
