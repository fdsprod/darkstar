import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

import {
  buildReviewDecision, buildReviewFeedback, chooseSafeTextRepresentation, iterationActivity, nextReviewSession,
  mergeDiffPages, orderedReviewSessions, parseReviewView, previousReviewedVersion, representationContentDigestMatches, reviewSessionChanged, splitDiffRows, validateArtifactDiff, verifyCandidateArtifact,
} from "../src/pages/artifactReviewModel.ts";

const digest = (letter) => letter.repeat(64);
function session(overrides = {}) {
  return {
    schemaVersion: 1, id: "approval_00000000000000000000000000", checkpointId: "checkpoint_1", runId: "run_1", visitId: "visit_1", nodeId: "review",
    revision: 2, candidate: { artifactId: "artifact_design", version: 3 }, candidateDigest: digest("c"), scopeDigest: digest("s"), policyDigest: digest("p"),
    mode: "approve", revisionLimitReached: false, state: "awaiting_human", turns: [], affectedArtifacts: [], allowedActions: ["approve", "request_changes", "reject"],
    resourceVersion: 7, createdAt: "2026-09-05T10:00:00Z", updatedAt: "2026-09-05T10:01:00Z", ...overrides,
  };
}
function representation(overrides = {}) {
  return {
    representationId: "representation_1", artifact: { artifactId: "artifact_design", version: 3 }, representationKind: "text",
    processor: { name: "safe-text", version: "1" }, mediaType: "text/markdown", locator: "safe/1", digest: digest("r"), size: 100,
    tokenEstimate: 20, truncated: false, disclosure: "redacted", diagnostics: [], metadata: {}, createdAt: "2026-09-05T10:00:01Z", ...overrides,
  };
}
function subject(overrides = {}) { return { artifactId: "artifact_design", version: 3, status: "stored", sensitivity: "internal", ...overrides }; }

test("review feedback and decisions bind only the exact current session", () => {
  const value = session();
  assert.deepEqual(buildReviewFeedback(value, "  Add rollback evidence.  "), { candidateDigest: digest("c"), scopeDigest: digest("s"), message: "Add rollback evidence." });
  assert.deepEqual(buildReviewDecision(value, "approve", " Looks good. "), { candidateDigest: digest("c"), scopeDigest: digest("s"), policyDigest: digest("p"), action: "approve", comment: "Looks good." });
  assert.deepEqual(buildReviewDecision(value, "reject", "Unsafe."), { candidateDigest: digest("c"), scopeDigest: digest("s"), policyDigest: digest("p"), action: "reject", comment: "Unsafe." });
  assert.throws(() => buildReviewFeedback(session({ state: "superseded" }), "Again"), /no longer accepts/);
  assert.throws(() => buildReviewDecision(value, "reject", " "), /Explain why/);
  assert.throws(() => buildReviewDecision(value, "approve", "😀".repeat(1025)), /4096 bytes/);
  assert.throws(() => buildReviewFeedback(value, "😀".repeat(4097)), /16384 bytes/);
});

test("safe text selection fails closed on identity, policy, media type, and size", () => {
  assert.equal(chooseSafeTextRepresentation(subject(), [representation()]).kind, "available");
  assert.throws(() => chooseSafeTextRepresentation(subject(), [representation({ artifact: { artifactId: "artifact_other", version: 3 } })]), /identity does not match/);
  assert.throws(() => chooseSafeTextRepresentation(subject(), [representation({ artifact: { artifactId: "artifact_design", version: 2 } })]), /identity does not match/);
  assert.deepEqual(chooseSafeTextRepresentation(subject(), [representation({ disclosure: "withheld" })]), { kind: "unavailable", reason: "withheld" });
  assert.deepEqual(chooseSafeTextRepresentation(subject(), [representation({ mediaType: "text/html" })]), { kind: "unavailable", reason: "unsupported" });
  assert.deepEqual(chooseSafeTextRepresentation(subject(), [representation({ mediaType: "text/plain; charset=windows-1252" })]), { kind: "unavailable", reason: "unsupported" });
  assert.deepEqual(chooseSafeTextRepresentation(subject(), [representation({ size: 2_097_153 })]), { kind: "unavailable", reason: "too_large" });
  assert.deepEqual(chooseSafeTextRepresentation(subject(), [representation({ size: -1 })]), { kind: "unavailable", reason: "too_large" });
  for (const status of ["stored_uninspectable", "quarantined"]) assert.deepEqual(chooseSafeTextRepresentation(subject({ status }), [representation()]), { kind: "unavailable", reason: "withheld" });
  assert.deepEqual(chooseSafeTextRepresentation(subject({ sensitivity: "unknown" }), [representation()]), { kind: "unavailable", reason: "withheld" });
  for (const sensitivity of ["sensitive", "secret"]) {
    assert.deepEqual(chooseSafeTextRepresentation(subject({ sensitivity }), [representation({ disclosure: "raw" })]), { kind: "unavailable", reason: "withheld" });
    assert.equal(chooseSafeTextRepresentation(subject({ sensitivity }), [representation({ disclosure: "redacted" })]).kind, "available");
  }
  const redacted = representation({ representationId: "representation_redacted", disclosure: "redacted" });
  assert.equal(chooseSafeTextRepresentation(subject(), [representation({ disclosure: "raw" }), redacted]).representation.representationId, "representation_redacted");
});

test("safe representation content requires the exact digest header syntax", () => {
  assert.equal(representationContentDigestMatches(`sha256=${digest("r")}`, digest("r")), true);
  assert.equal(representationContentDigestMatches(digest("r"), digest("r")), false);
  assert.equal(representationContentDigestMatches(undefined, digest("r")), false);
  assert.equal(representationContentDigestMatches(`sha256=${digest("x")}`, digest("r")), false);
});

test("candidate evidence must exist at the exact version and digest", () => {
  const candidate = { artifactId: "artifact_design", version: 3 };
  const exact = { artifact: { ...candidate, blobDigest: digest("c") } };
  assert.equal(verifyCandidateArtifact(candidate, digest("c"), [exact]), exact);
  assert.throws(() => verifyCandidateArtifact(candidate, digest("c"), []), /absent/);
  assert.throws(() => verifyCandidateArtifact(candidate, digest("x"), [exact]), /digest does not match/);
});

test("iteration stages are derived only from session, turn, and agent projections", () => {
  const feedback = { kind: "human_feedback", sequence: 1, actor: { type: "user", id: "local" }, occurredAt: "2026-09-05T10:02:00Z", runId: "run_1", attemptId: "attempt_old", candidate: { artifactId: "artifact_design", version: 3 }, candidateDigest: digest("c"), message: "Revise." };
  const waiting = session({ state: "awaiting_agent", allowedActions: [], turns: [feedback] });
  assert.deepEqual(iterationActivity(waiting).map((item) => item.stage), ["feedback_recorded", "awaiting_dispatch"]);
  assert.match(iterationActivity(waiting).at(-1).message, /no revision attempt is recorded/);
  const active = session({ state: "awaiting_agent", allowedActions: [], turns: [feedback], activeIteration: { attemptId: "attempt_new", runId: "run_1", resumedBy: { type: "system", id: "runtime" }, resumedAt: "2026-09-05T10:03:00Z" } });
  assert.deepEqual(iterationActivity(active).map((item) => item.stage), ["feedback_recorded", "iteration_recorded"]);
  const agent = { attemptId: "attempt_new", status: "validating", updatedAt: "2026-09-05T10:04:00Z" };
  assert.deepEqual(iterationActivity(active, agent).map((item) => item.stage), ["feedback_recorded", "validating"]);
  assert.equal(iterationActivity(session()).length, 0);
});

test("review history retains exact rounds and finds the prior reviewed candidate", () => {
  const first = session({ id: "approval_first", revision: 1, candidate: { artifactId: "artifact_design", version: 2 } });
  const current = session(); const next = session({ id: "approval_next", revision: 3, candidate: { artifactId: "artifact_design", version: 4 } });
  const history = { schemaVersion: 1, checkpointId: "checkpoint_1", sessions: [next, first, current] };
  assert.deepEqual(orderedReviewSessions(history.sessions).map((item) => item.revision), [1, 2, 3]);
  assert.equal(previousReviewedVersion(history, current), 2);
  assert.equal(nextReviewSession(history, current).id, "approval_next");
  assert.equal(reviewSessionChanged(current, { ...current }), false);
  assert.equal(reviewSessionChanged(current, { ...current, resourceVersion: 8 }), true);
  assert.throws(() => orderedReviewSessions([current, { ...current, id: "approval_duplicate" }]), /duplicate revisions/);
});

test("split diff pairs replacements and preserves omitted-policy rows", () => {
  const diff = { textDiff: { status: "available", hunks: [{ fromStart: 1, toStart: 1, entries: [
    { kind: "unchanged", fromLine: 1, toLine: 1, text: "same" }, { kind: "removed", fromLine: 2, text: "old" },
    { kind: "added", toLine: 2, text: "new" }, { kind: "omitted", fromLine: 3, toLine: 3, omittedLines: 8 },
  ] }] } };
  assert.deepEqual(splitDiffRows(diff).map((row) => row.kind), ["unchanged", "changed", "omitted"]);
  assert.deepEqual(splitDiffRows(diff)[1], { kind: "changed", left: "old", right: "new", leftLine: 2, rightLine: 2 });
});

test("bounded diff continuation preserves exact identity and appends only matching pages", () => {
  const base = { artifactId: "artifact_design", from: 2, to: 3, changed: ["content"], fromDigest: digest("a"), toDigest: digest("b"), representations: { from: ["rep_from"], to: ["rep_to"] }, textDiff: { status: "available", from: { artifact: { artifactId: "artifact_design", version: 2 }, representationId: "rep_from", digest: digest("f"), representationKind: "text", mediaType: "text/plain", disclosure: "redacted" }, to: { artifact: { artifactId: "artifact_design", version: 3 }, representationId: "rep_to", digest: digest("e"), representationKind: "text", mediaType: "text/plain", disclosure: "redacted" }, policy: { algorithm: "darkstar-line-dp/v1", contextLines: 3, maxInputBytes: 2_097_152, maxWorkUnits: 1_000_000, pageSize: 200, maxPageBytes: 3_145_728 }, policyDigest: digest("d"), resultDigest: digest("c"), hunks: [{ fromStart: 1, toStart: 1, entries: [] }], nextCursor: "next", totalEntries: 2 } };
  const next = structuredClone(base); next.textDiff.hunks = [{ fromStart: 2, toStart: 2, entries: [] }]; delete next.textDiff.nextCursor;
  const expected = { artifactId: "artifact_design", from: 2, to: 3, fromDigest: digest("a"), toDigest: digest("b"), fromRepresentations: [{ representationId: "rep_from", digest: digest("f"), representationKind: "text", mediaType: "text/plain", disclosure: "redacted" }], toRepresentations: [{ representationId: "rep_to", digest: digest("e"), representationKind: "text", mediaType: "text/plain", disclosure: "redacted" }] };
  assert.equal(validateArtifactDiff(expected, base).artifactId, "artifact_design");
  assert.equal(mergeDiffPages(base, next, "next").textDiff.hunks.length, 2);
  assert.throws(() => mergeDiffPages(base, { ...next, toDigest: digest("x") }, "next"), /does not match/);
  const wrongRepresentation = structuredClone(next); wrongRepresentation.textDiff.to.representationId = "rep_other";
  assert.throws(() => mergeDiffPages(base, wrongRepresentation, "next"), /does not match/);
  assert.throws(() => validateArtifactDiff(expected, wrongRepresentation), /representation evidence/);
  const wrongArtifact = structuredClone(base); wrongArtifact.textDiff.from.artifact.version = 1;
  assert.throws(() => validateArtifactDiff(expected, wrongArtifact), /representation evidence/);
  const wrongTotal = structuredClone(next); wrongTotal.textDiff.totalEntries = 3;
  assert.throws(() => mergeDiffPages(base, wrongTotal, "next"), /does not match/);
  assert.throws(() => mergeDiffPages(base, next, "other"), /does not match/);
});

test("review workspace is routed, reconnect-safe, escaped, and keeps separate authority links", async () => {
  const [page, router, checkpoints, client, styles] = await Promise.all([
    readFile(new URL("../src/pages/ArtifactReviewPage.tsx", import.meta.url), "utf8"), readFile(new URL("../src/app/router.tsx", import.meta.url), "utf8"),
    readFile(new URL("../src/pages/CheckpointsPage.tsx", import.meta.url), "utf8"), readFile(new URL("../src/api/client.ts", import.meta.url), "utf8"), readFile(new URL("../src/styles.css", import.meta.url), "utf8"),
  ]);
  assert.match(router, /\/checkpoints\/:approvalId\/review/); assert.match(checkpoints, /Open review workspace/);
  for (const value of ["current", "prior", "inline", "split"]) assert.ok(page.includes(`"${value}"`));
  assert.match(page, /state\.cursor/); assert.match(page, /setFeedback/); assert.match(page, /cause\.status === 409 \|\| cause\.status === 412/);
  assert.match(page, /readingExactCandidate/); assert.match(page, /aria-live="off"/); assert.match(page, /Provider permissions/); assert.match(page, /Required input/);
  assert.match(page, /<ArtifactReviewWorkspace key=\{approvalId\}/); assert.match(page, /diff\?\.kind === "available"/);
  assert.match(page, /type AgentLogState =/); assert.match(page, /agentAttemptRef\.current === attemptId/); assert.match(page, /log\.attemptId === agent\.attemptId/);
  assert.match(page, /setCandidateState\(\{ kind: "stale", reason: "resource_changed" \}\)/); assert.match(page, /partial bounded comparison/);
  assert.match(page, /prefix.*v\{value\.artifact\.version\} provenance/); assert.match(page, /affectedArtifacts/); assert.match(page, /session\.decision/);
  assert.match(page, /readRepresentationContent/); assert.doesNotMatch(page, /readArtifactContent|dangerouslySetInnerHTML|resumeCheckpointRevision/);
  assert.match(client, /submitCheckpointFeedback\(approvalId: string, resourceVersion: number, idempotencyKey: string/);
  assert.match(client, /decideCheckpointReviewSession\(approvalId: string, resourceVersion: number, idempotencyKey: string/);
  assert.match(styles, /grid-template-columns: 225px minmax\(0, 1fr\) 300px/); assert.match(styles, /@media \(max-width: 600px\)/);
  assert.match(styles, /@media \(max-width: 820px\) \{ \.review-decision-bar \{ left: 20px; \} \}/);
});

test("reader view vocabulary defaults closed", () => {
  assert.equal(parseReviewView("split"), "split"); assert.equal(parseReviewView("html"), "current"); assert.equal(parseReviewView(null), "current");
});
