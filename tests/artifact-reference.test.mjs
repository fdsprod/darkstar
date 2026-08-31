import test from "node:test";
import assert from "node:assert/strict";
import { fileURLToPath } from "node:url";
import { dirname, resolve } from "node:path";
import { evaluateCorpus, loadCorpus, selectContext } from "../scripts/artifact-reference.mjs";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const corpusPath = resolve(root, "examples/artifacts/golden-corpus.json");
const corpus = loadCorpus(corpusPath);
const results = evaluateCorpus(corpus, corpusPath);

test("golden corpus covers the required artifact risks", () => {
  const coverage = new Set(corpus.fixtures.flatMap((fixture) => fixture.covers));
  for (const item of ["text","markdown","json","yaml","csv","pdf","image","malformed","duplicate","prompt_injection","size_limit","unsupported","type_mismatch","safe_degradation"])
    assert.ok(coverage.has(item), `missing coverage: ${item}`);
});

test("every fixture has its documented safe outcome", () => {
  assert.deepEqual(results.filter((entry) => !entry.pass), []);
  assert.ok(results.filter((entry) => ["malformed-json","unknown-binary"].includes(entry.id)).every((entry) => entry.actual.representation === "descriptor"));
});

test("equal bytes share a blob but retain artifact identities", () => {
  const duplicate = results.find((entry) => entry.id === "duplicate-b");
  assert.equal(duplicate.sharesBlobWith, "duplicate-a");
  assert.notEqual(duplicate.id, duplicate.sharesBlobWith);
});

test("context selection is deterministic and accounts for omissions", () => {
  const actual = selectContext(corpus.selection, corpus.contextBudget);
  assert.equal(actual.code, "OK");
  assert.deepEqual(actual.selected, corpus.selection.expectedSelected);
  assert.deepEqual(actual.omitted, corpus.selection.expectedOmitted);
});

test("required context fails closed when it cannot fit", () => {
  assert.equal(selectContext(corpus.selection, 17).code, "CONTEXT_REQUIRED_EXCEEDS_BUDGET");
});

test("late evidence cannot mutate a frozen manifest", () => {
  assert.equal(corpus.selection.lateEvidence.expected, "next_manifest_only");
  assert.ok(!corpus.selection.expectedSelected.includes(corpus.selection.lateEvidence.representationId));
});
