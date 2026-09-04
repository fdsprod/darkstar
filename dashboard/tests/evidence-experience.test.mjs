import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

import { buildArtifactIngestRequest, buildArtifactTarget, decodeArtifactImpact, decodeArtifactView } from "../src/pages/artifactModel.ts";

test("paste ingestion preserves UTF-8 bytes and canonical classification tokens", async () => {
  const request = await buildArtifactIngestRequest({ source: { kind: "paste", sourceName: " note.md ", mediaType: " Text/Markdown ", content: "Evidence 世界" }, sensitivity: "internal", roles: " log, evidence, log ", tags: "late, late" });
  assert.equal(request.sourceKind, "paste");
  assert.equal(request.sourceName, "note.md");
  assert.equal(request.mediaType, "text/markdown");
  assert.equal(Buffer.from(request.content, "base64").toString("utf8"), "Evidence 世界");
  assert.deepEqual(request.roles, ["evidence", "log"]);
  assert.deepEqual(request.tags, ["late"]);
});

test("file ingestion rejects over 25 MiB before reading browser bytes", async () => {
  let read = false;
  const file = { name: "large.bin", type: "", size: 25 * 1024 * 1024 + 1, async arrayBuffer() { read = true; return new ArrayBuffer(0); } };
  await assert.rejects(buildArtifactIngestRequest({ source: { kind: "file", file }, sensitivity: "internal", roles: "", tags: "" }), /25 MiB/);
  assert.equal(read, false);
});

test("the target model exposes exactly the six dashboard evidence scopes", () => {
  for (const kind of ["work", "run", "node", "checkpoint", "story", "implementation_point"]) assert.deepEqual(buildArtifactTarget(kind, "target_1"), { kind, id: "target_1" });
  assert.throws(() => buildArtifactTarget("project", "project_1"), /supported evidence target/);
  assert.throws(() => buildArtifactTarget("work", " "), /identifier is required/);
});

test("daemon-native nested artifact JSON decodes without inventing provenance", () => {
  const view = decodeArtifactView({ artifact: { artifactId: "artifact_one", version: 1, sourceKind: "file", sourceName: "evidence.txt", blobDigest: "a".repeat(64), size: 8, declaredMediaType: "text/plain", detectedMediaType: "text/plain", locator: "sha256:one", sensitivity: "internal", trust: "untrusted", creator: "user:local", status: "stored", producer: { name: "ingest", version: "1" }, roles: [], tags: [], metadata: {}, provenance: { origin: "operation", operationId: "operation_one" }, createdAt: "2026-09-04T00:00:00Z" }, freshness: "potentially_stale", representations: [{ representationId: "representation_one", artifact: { artifactId: "artifact_one", version: 1 }, representationKind: "text", processor: { name: "text", version: "1", mediaTypes: ["text/plain"] }, mediaType: "text/plain", locator: "sha256:two", digest: "b".repeat(64), size: 8, tokenEstimate: 2, truncated: false, disclosure: "raw", diagnostics: [], metadata: {}, createdAt: "2026-09-04T00:00:01Z" }] });
  assert.equal(view.freshness, "potentially_stale");
  assert.equal(view.artifact.provenance.origin, "operation");
  assert.equal(view.representations[0].artifactId, "artifact_one");
  assert.equal(view.representations[0].processorName, "text");
});

test("impact decoding is a closed proposal union and preserves coverage truth", () => {
  const assessment = decodeArtifactImpact({ kind: "impact_assessment", schemaVersion: 1, evidence: { artifactId: "artifact_one", version: 1 }, target: { kind: "run", id: "run_one" }, runId: "run_one", roles: ["evidence"], coverage: [{ attemptId: "attempt_one", nodeId: "design", manifestId: "manifest_one", state: "not_supplied" }], proposals: [{ action: "refresh", attemptId: "attempt_one", reason: "active_attempt_missing_exact_evidence" }] });
  assert.equal(assessment.proposals[0].action, "refresh");
  assert.equal(assessment.coverage[0].state, "not_supplied");
  assert.throws(() => decodeArtifactImpact({ ...assessment, proposals: [{ action: "apply", reason: "unsafe" }] }), /impact action is unsupported/);
});

test("all six contextual target affordances use the shared evidence workflow", async () => {
  const sources = await Promise.all(["WorkDetailPage.tsx", "RunDetailPage.tsx", "CheckpointsPage.tsx", "ArtifactsPage.tsx"].map((name) => readFile(new URL(`../src/pages/${name}`, import.meta.url), "utf8")));
  const joined = sources.join("\n");
  for (const kind of ["work", "run", "node", "checkpoint", "story", "implementation_point"]) assert.match(joined, new RegExp(`targetKind=${kind}`));
  assert.match(joined, /type="file"/);
  assert.match(joined, /role="tablist"/);
  assert.doesNotMatch(joined, /dangerouslySetInnerHTML/);
});

test("deep-linked ingestion waits for content and ambiguous attachment retries keep their exact target", async () => {
  const source = await readFile(new URL("../src/pages/ArtifactsPage.tsx", import.meta.url), "utf8");
  assert.match(source, /if \(items && params\.has\("ingest"\)/);
  assert.match(source, /\}, \[items, search\]\);/);
  assert.match(source, /const target = stored\?\.target \?\? buildArtifactTarget/);
  assert.match(source, /artifact: \{ artifactId: stored\.artifactId, version: stored\.version \}, target/);
  assert.match(source, /disabled=\{Boolean\(partial\)\}/);
  assert.match(source, /Attachment target locked/);
});

test("original downloads and availability copy respect inspection status", async () => {
  const source = await readFile(new URL("../src/pages/ArtifactPage.tsx", import.meta.url), "utf8");
  assert.match(source, /selected\.artifact\.status === "stored" \? <DownloadOriginal/);
  assert.match(source, /download unavailable by inspection policy/);
  assert.doesNotMatch(source, /immutable original remains available/);
});
