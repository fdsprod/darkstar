import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { pathToFileURL } from "node:url";

function bytesFor(fixture, base) {
  if (fixture.inlineBase64 !== undefined) return Buffer.from(fixture.inlineBase64, "base64");
  return fixture.inline === undefined ? readFileSync(resolve(base, fixture.path)) : Buffer.from(fixture.inline, "utf8");
}

function isSupportedImage(fixture, bytes) {
  if (fixture.declaredType === "image/png") return bytes.subarray(0, 8).equals(Buffer.from([137, 80, 78, 71, 13, 10, 26, 10]));
  if (fixture.declaredType === "image/jpeg") return bytes.length >= 3 && bytes[0] === 0xff && bytes[1] === 0xd8 && bytes[2] === 0xff;
  if (fixture.declaredType === "image/webp") return bytes.subarray(0, 4).toString("ascii") === "RIFF" && bytes.subarray(8, 12).toString("ascii") === "WEBP";
  return false;
}

function classify(fixture, bytes, sourceByteLimit) {
  if (bytes.length > (fixture.sourceByteLimit ?? sourceByteLimit)) return { state: "rejected_too_large", representation: null };
  if (fixture.detectedType === "application/x-msdownload") return { state: "quarantined", representation: "descriptor" };
  if (fixture.declaredType === "application/json") {
    try { JSON.parse(bytes.toString("utf8")); }
    catch { return { state: "stored_uninspectable", representation: "descriptor" }; }
    return { state: "ready", representation: "structured" };
  }
  if (["application/yaml"].includes(fixture.declaredType)) return { state: "ready", representation: "structured" };
  if (fixture.declaredType === "text/csv") return { state: "ready", representation: "table" };
  if (fixture.declaredType === "application/pdf") return bytes.subarray(0, 5).toString() === "%PDF-"
    ? { state: "ready", representation: "document" }
    : { state: "stored_uninspectable", representation: "descriptor" };
  if (fixture.declaredType.startsWith("image/")) return isSupportedImage(fixture, bytes)
    ? { state: "ready", representation: "image" }
    : { state: "stored_uninspectable", representation: "descriptor" };
  return { state: "ready", representation: "text" };
}

export function evaluateCorpus(corpus, corpusPath) {
  const base = dirname(corpusPath);
  const seen = new Map();
  return corpus.fixtures.map((fixture) => {
    const bytes = bytesFor(fixture, base);
    const digest = createHash("sha256").update(bytes).digest("hex");
    const firstArtifactId = seen.get(digest) ?? null;
    if (!firstArtifactId) seen.set(digest, fixture.id);
    const actual = { ...classify(fixture, bytes, corpus.sourceByteLimit) };
    if (fixture.expected.trusted !== undefined) actual.trusted = false;
    return { id: fixture.id, digest, sharesBlobWith: firstArtifactId, actual, pass: JSON.stringify(actual) === JSON.stringify(fixture.expected) };
  });
}

export function selectContext(selection, budget) {
  const ordered = [...selection.candidates].sort((a, b) =>
    Number(b.required) - Number(a.required)
    || a.rank - b.rank
    || a.arrival - b.arrival
    || a.artifactId.localeCompare(b.artifactId)
    || a.representationId.localeCompare(b.representationId));
  const selected = [];
  const omitted = [];
  let used = 0;
  for (const item of ordered) {
    if (used + item.estimatedTokens <= budget) {
      selected.push(item.representationId);
      used += item.estimatedTokens;
    } else if (item.required) {
      return { code: "CONTEXT_REQUIRED_EXCEEDS_BUDGET", selected: [], omitted: [], used: 0 };
    } else omitted.push({ representationId: item.representationId, reason: "budget" });
  }
  return { code: "OK", selected, omitted, used };
}

export function loadCorpus(path) { return JSON.parse(readFileSync(path, "utf8")); }

export function main(argv = process.argv.slice(2)) {
  if (argv.length !== 1) {
    process.stderr.write("Usage: node scripts/artifact-reference.mjs <golden-corpus.json>\n");
    return 2;
  }
  const corpusPath = resolve(argv[0]);
  const corpus = loadCorpus(corpusPath);
  const fixtures = evaluateCorpus(corpus, corpusPath);
  const selection = selectContext(corpus.selection, corpus.contextBudget);
  process.stdout.write(`${JSON.stringify({ schemaVersion: corpus.schemaVersion, fixtures, selection }, null, 2)}\n`);
  return fixtures.every((entry) => entry.pass)
    && JSON.stringify(selection.selected) === JSON.stringify(corpus.selection.expectedSelected)
    && JSON.stringify(selection.omitted) === JSON.stringify(corpus.selection.expectedOmitted) ? 0 : 1;
}

if (import.meta.url === pathToFileURL(process.argv[1]).href) process.exitCode = main();
