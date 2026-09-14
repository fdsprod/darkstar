import assert from "node:assert/strict";
import test from "node:test";

import { repositorySettingsDraft, repositorySettingsFromDraft } from "../src/pages/projectRepositoriesModel.ts";

test("inherited collections and explicit empty overrides stay distinct", () => {
  const inherited = repositorySettingsDraft();
  assert.equal(inherited.scopeMode, "inherit");
  assert.equal(inherited.validationMode, "inherit");
  assert.deepEqual(repositorySettingsFromDraft(inherited), { pathScope: null, validationProfiles: null });

  const empty = repositorySettingsDraft({ pathScope: [], validationProfiles: {} });
  assert.equal(empty.scopeMode, "custom");
  assert.equal(empty.validationMode, "custom");
  assert.deepEqual(repositorySettingsFromDraft(empty), { pathScope: [], validationProfiles: {} });
});

test("all repository settings round trip with explicit configuration origin", () => {
  const settings = {
    configurationRoot: "C:\\Projects\\Application",
    baseRef: "develop",
    worktreeBase: "../worktrees",
    delivery: { remote: "upstream", targetBranch: "release" },
    pathScope: ["src", "docs/design"],
    validationProfiles: { test: ["npm test", "go test ./..."], manual: [] },
  };
  const draft = repositorySettingsDraft(settings);
  assert.deepEqual(repositorySettingsFromDraft(draft), settings);
  assert.equal(draft.scopeLines, "src\ndocs/design");
  assert.notEqual(repositorySettingsFromDraft(draft).validationProfiles, settings.validationProfiles);
});

test("switching to inheritance ignores retained custom text", () => {
  const draft = { ...repositorySettingsDraft(), scopeMode: "inherit", scopeLines: "../../outside", validationMode: "inherit", validationText: "unfinished JSON" };
  assert.deepEqual(repositorySettingsFromDraft(draft), { pathScope: null, validationProfiles: null });
});

test("relative worktrees require a configuration root and roots must be absolute", () => {
  assert.throws(() => repositorySettingsFromDraft({ ...repositorySettingsDraft(), worktreeBase: "trees" }), /configuration root/i);
  assert.throws(() => repositorySettingsFromDraft({ ...repositorySettingsDraft(), configurationRoot: "relative" }), /absolute/i);
  for (const root of ["C:\\Projects\\Application", "/srv/application", "\\\\server\\share\\application"]) {
    assert.equal(repositorySettingsFromDraft({ ...repositorySettingsDraft(), configurationRoot: root, worktreeBase: "trees" }).configurationRoot, root);
  }
  assert.equal(repositorySettingsFromDraft({ ...repositorySettingsDraft(), worktreeBase: "/tmp/worktrees" }).worktreeBase, "/tmp/worktrees");
});

test("path scope rejects escapes and preserves an intentional empty list", () => {
  for (const scopeLines of ["../outside", "src/../../outside", "/absolute", "C:\\outside", "C:relative", "src\\folder", "src\u0000bad"]) {
    assert.throws(() => repositorySettingsFromDraft({ ...repositorySettingsDraft(), scopeMode: "custom", scopeLines }), /scope/i);
  }
  assert.deepEqual(repositorySettingsFromDraft({ ...repositorySettingsDraft(), scopeMode: "custom", scopeLines: "\n src \r\n\ndocs/design\n" }).pathScope, ["src", "docs/design"]);
  assert.deepEqual(repositorySettingsFromDraft({ ...repositorySettingsDraft(), scopeMode: "custom", scopeLines: "" }).pathScope, []);
});

test("validation profiles reject malformed JSON, wrong shapes, and unsafe keys", () => {
  for (const validationText of ["", "{", "null", "[]", '{"test":"npm test"}', '{"test":[" "]}', '{" test":["npm test"]}', '{"__proto__":{"polluted":true}}', '{"constructor":[]}', '{"prototype":[]}']) {
    assert.throws(() => repositorySettingsFromDraft({ ...repositorySettingsDraft(), validationMode: "custom", validationText }), /validation/i);
  }
  assert.equal({}.polluted, undefined);
  assert.deepEqual(repositorySettingsFromDraft({ ...repositorySettingsDraft(), validationMode: "custom", validationText: '{"test":[]}' }).validationProfiles, { test: [] });
});

test("unknown settings and invalid prototype objects fail instead of dropping data", () => {
  for (const settings of [{ unknown: "hidden" }, { delivery: { unknown: "hidden" } }, { pathScope: "src" }, { validationProfiles: [] }, JSON.parse('{"__proto__":{}}'), Object.create({ baseRef: "hidden" })]) {
    assert.throws(() => repositorySettingsDraft(settings));
  }
  assert.throws(() => repositorySettingsFromDraft({ ...repositorySettingsDraft(), unknown: "hidden" }), /unsupported/i);
  assert.throws(() => repositorySettingsFromDraft({ ...repositorySettingsDraft(), scopeMode: "sometimes" }), /scope/i);
  assert.throws(() => repositorySettingsFromDraft({ ...repositorySettingsDraft(), validationMode: "sometimes" }), /validation/i);
});
