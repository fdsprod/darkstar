import assert from "node:assert/strict";
import { mkdtempSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

import {
  RepositoryFixtureError,
  loadRepositoryFixtureManifest,
  materializeRepositoryFixtures,
  validateManifest,
} from "../scripts/repository-fixtures.mjs";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const manifestPath = join(root, "examples", "repositories", "golden-repositories.json");

function withTempDirectory(run) {
  const directory = mkdtempSync(join(tmpdir(), "darkstar-golden-repositories-"));
  try { return run(directory); }
  finally { rmSync(directory, { recursive: true, force: true }); }
}

test("golden repository manifest covers clean, dirty, branch, and worktree states", () => {
  const manifest = loadRepositoryFixtureManifest(manifestPath);
  assert.deepEqual(validateManifest(manifest), []);
  const coverage = new Set(manifest.fixtures.flatMap((fixture) => fixture.covers));
  for (const item of ["clean", "dirty", "branches", "worktree", "tracked_change", "untracked_file", "owned_branch", "unowned_branch_collision"])
    assert.ok(coverage.has(item), `missing repository coverage: ${item}`);
});

test("materialized repositories match their declared observations", () => withTempDirectory((directory) => {
  const manifest = loadRepositoryFixtureManifest(manifestPath);
  const result = materializeRepositoryFixtures(manifest, join(directory, "materialized"));
  assert.equal(result.status, "passed");
  assert.deepEqual(result.fixtures.filter((fixture) => !fixture.pass), []);

  const dirty = result.fixtures.find((fixture) => fixture.id === "dirty_checkout");
  assert.deepEqual(dirty.snapshot.status, [" M src/value.txt", "?? notes/untracked.txt"]);
  assert.equal(readFileSync(join(directory, "materialized", "dirty_checkout", "repository", "src", "value.txt"), "utf8"), "user-owned modification\n");

  const linked = result.fixtures.find((fixture) => fixture.id === "linked_worktree");
  assert.equal(linked.snapshot.worktrees.length, 2);
  assert.ok(linked.snapshot.worktrees.every((worktree) => worktree.status.length === 0));
}));

test("materialization is deterministic across destination paths", () => withTempDirectory((directory) => {
  const manifest = loadRepositoryFixtureManifest(manifestPath);
  const first = materializeRepositoryFixtures(manifest, join(directory, "first"));
  const second = materializeRepositoryFixtures(manifest, join(directory, "second"));
  assert.deepEqual(first, second);
  assert.equal(new Set(first.fixtures.map((fixture) => fixture.snapshot.head)).size, 1);
}));

test("materializer refuses to reuse a destination", () => withTempDirectory((directory) => {
  const manifest = loadRepositoryFixtureManifest(manifestPath);
  assert.throws(
    () => materializeRepositoryFixtures(manifest, directory),
    (cause) => cause instanceof RepositoryFixtureError && cause.code === "REPOSITORY_FIXTURE_DESTINATION_EXISTS",
  );
}));

test("manifest rejects path traversal before invoking Git", () => {
  const manifest = loadRepositoryFixtureManifest(manifestPath);
  manifest.fixtures[0].changes = [{ path: "../escape.txt", content: "unsafe\n" }];
  assert.ok(validateManifest(manifest).some((cause) => cause.code === "REPOSITORY_FIXTURE_PATH_INVALID"));
});
