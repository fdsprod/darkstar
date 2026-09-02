import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { buildManifest, encodedManifest, REQUIRED_CAPABILITIES } from "../scripts/builtin-skills.mjs";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");

test("the MVP built-in skill package is complete and versioned", () => {
  const manifest = buildManifest();
  assert.deepEqual(manifest.skills.map((skill) => skill.name), REQUIRED_CAPABILITIES);
  assert.equal(manifest.version, "1.0.0");
  for (const skill of manifest.skills) {
    assert.match(skill.version, /^1\.\d+\.\d+$/);
    assert.match(skill.fingerprint, /^[0-9a-f]{64}$/);
    assert.equal(skill.kind, "skill");
    assert.equal(skill.class, "guaranteed");
    assert.equal(skill.source.type, "builtin_skill");
    assert.equal(skill.source.locator, skill.path);
    assert.deepEqual(skill.dependencies, []);
    assert.deepEqual(skill.permissions, []);
  }
});

test("the checked-in manifest matches the closed skill packages", () => {
  assert.equal(readFileSync(resolve(root, "skills/builtin/manifest.json"), "utf8").replace(/\r\n/g, "\n"), encodedManifest());
});

test("default MVP workflow capability references are packaged", () => {
  const packaged = new Set(buildManifest().skills.map((skill) => skill.name));
  const expectedReferences = ["darkstar:route-assessment", "darkstar:readiness", "darkstar:evidence-research", "darkstar:technical-design", "darkstar:story-decomposition", "darkstar:tracer-bullets"];
  for (const capability of expectedReferences) assert.ok(packaged.has(capability), capability);
});
