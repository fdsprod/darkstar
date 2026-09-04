import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

import { buildArtifactRevisionRequest, decodeArtifactView, decodeArtifactViews, previousRevision, revisionsForArtifact } from "../src/pages/artifactModel.ts";

function rawView(version, overrides = {}) {
  return {
    artifact: {
      kind: "artifact", schemaVersion: 1, artifactId: "artifact_design", version,
      sourceKind: "paste", sourceName: `design-v${version}.md`, blobDigest: "a".repeat(64), size: 12,
      declaredMediaType: "text/markdown", detectedMediaType: "text/markdown", locator: `blobs/${version}`,
      sensitivity: "internal", trust: "untrusted", creator: "user:local", status: "stored",
      createdAt: `2026-09-0${version}T10:00:00Z`, roles: ["design"], tags: ["review"], metadata: { phase: "design" },
      provenance: { origin: "attempt", runId: "run_1", nodeId: "design", attemptId: "attempt_1", operationId: `operation_${version}`, sourceArtifactId: "artifact_brief" },
      ...overrides,
    },
    freshness: "current",
    representations: [{
      kind: "representation", schemaVersion: 1, representationId: `representation_${version}`, artifactId: "artifact_design",
      representationKind: "text", processorName: "extractor", processorVersion: "1", mediaType: "text/plain", locator: `representations/${version}`,
      digest: "b".repeat(64), size: 10, tokenEstimate: 3, truncated: false, disclosure: "redacted", diagnostics: [], createdAt: `2026-09-0${version}T10:00:01Z`,
    }],
  };
}

test("artifact decoder preserves immutable revision and provenance evidence", () => {
  const decoded = decodeArtifactView(rawView(2));
  assert.equal(decoded.artifact.artifactId, "artifact_design");
  assert.equal(decoded.artifact.version, 2);
  assert.deepEqual(decoded.artifact.metadata, { phase: "design" });
  assert.deepEqual(decoded.artifact.provenance, {
    origin: "attempt", runId: "run_1", nodeId: "design", attemptId: "attempt_1", operationId: "operation_2", sourceArtifactId: "artifact_brief",
  });
  assert.equal(decoded.representations[0].disclosure, "redacted");
});

test("artifact decoder fails closed on drift and cross-artifact representations", () => {
  assert.throws(() => decodeArtifactView({ ...rawView(1), freshness: "probably_current" }), /freshness is unsupported/);
  const mismatched = rawView(1);
  mismatched.representations[0].artifactId = "artifact_other";
  assert.throws(() => decodeArtifactView(mismatched), /identity does not match/);
});

test("revision history filters one identity, orders newest first, and finds the recorded predecessor", () => {
  const values = decodeArtifactViews([rawView(1), rawView(3), rawView(2), { ...rawView(1), artifact: { ...rawView(1).artifact, artifactId: "artifact_other" }, representations: [] }]);
  const revisions = revisionsForArtifact(values, "artifact_design");
  assert.deepEqual(revisions.map((value) => value.artifact.version), [3, 2, 1]);
  assert.equal(previousRevision(revisions, 3).artifact.version, 2);
  assert.equal(previousRevision(revisions, 1), undefined);
});

test("revision requests encode complete UTF-8 replacement content and normalize tokens", () => {
  const request = buildArtifactRevisionRequest({
    sourceName: " design.md ", mediaType: " Text/Markdown ", content: "Hello, 世界", sensitivity: "sensitive",
    roles: " design, evidence, design ", tags: "alpha, review, alpha",
  });
  assert.deepEqual(request, {
    sourceKind: "paste", sourceName: "design.md", mediaType: "text/markdown",
    content: Buffer.from("Hello, 世界", "utf8").toString("base64"), sensitivity: "sensitive",
    roles: ["design", "evidence"], tags: ["alpha", "review"],
  });
  assert.equal("artifactId" in request, false);
  assert.equal("creator" in request, false);
});

test("artifact client and page use exact-version operations without loading stored content into revision input", async () => {
  const [client, page] = await Promise.all([
    readFile(new URL("../src/api/client.ts", import.meta.url), "utf8"),
    readFile(new URL("../src/pages/ArtifactPage.tsx", import.meta.url), "utf8"),
  ]);
  assert.match(client, /listArtifacts\(targetKind\?: Schemas\["ArtifactTargetKind"\], targetId\?: string/);
  assert.match(client, /diffArtifactVersions\(artifactId: string, from: number, to: number/);
  assert.match(client, /reviseArtifact\(artifactId: string, resourceVersion: number, body: Schemas\["ArtifactIngestRequest"\], idempotencyKey: string/);
  assert.match(client, /this\.operation\("reviseArtifact", \{ path: \{ artifactId \}, body, resourceVersion, idempotencyKey, signal \}\)/);
  assert.match(page, /Stored blob content is intentionally not loaded back into this form/);
  assert.match(page, /state\.cursor/);
  assert.doesNotMatch(page, /fetch\(.*locator/);
});
