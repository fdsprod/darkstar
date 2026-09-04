import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const read = (path) => readFile(new URL(path, import.meta.url), "utf8");

test("dashboard typography uses a readable semantic scale", async () => {
  const styles = await read("../src/styles.css");
  for (const token of ["page-title", "section-title", "body", "label", "caption", "control", "mono"]) {
    assert.match(styles, new RegExp(`--font-${token}:`), `missing ${token} typography token`);
  }
  const pixelSizes = [...styles.matchAll(/font-size:\s*([\d.]+)px/g)].map((match) => Number(match[1]));
  assert.ok(pixelSizes.every((size) => size >= 12), `found unreadable font size: ${Math.min(...pixelSizes)}px`);
  assert.match(styles, /--font-body:\s*\.875rem/);
  assert.match(styles, /--font-caption:\s*\.75rem/);
  assert.match(styles, /--control-height:\s*2\.5rem/);
  assert.match(styles, /overflow-x:\s*hidden/);
  assert.match(styles, /code, pre \{[^}]*--font-mono[^}]*overflow-wrap: anywhere/s);
});

test("dashboard navigation exposes stable groups and one shared page heading", async () => {
  const [shell, router, pageStructure, workDetail] = await Promise.all([
    read("../src/components/AppShell.tsx"),
    read("../src/app/router.tsx"),
    read("../src/components/PageStructure.tsx"),
    read("../src/pages/WorkDetailPage.tsx"),
  ]);
  for (const group of ["Work", "Operations", "Library", "System"]) assert.match(shell, new RegExp(`label: "${group}"`));
  assert.doesNotMatch(shell, /\/board\?create=1/);
  assert.match(shell, /switching unavailable/);
  assert.equal(pageStructure.match(/<h1>/g)?.length, 1);
  assert.match(pageStructure, /aria-label="Breadcrumb"/);
  assert.doesNotMatch(workDetail, /<h1>/);
  for (const section of ["Work", "Operations", "Library", "System"]) assert.match(router, new RegExp(`section: "${section}"`));
});

test("meaningful dashboard selections are encoded in shareable URLs", async () => {
  const [workflows, checkpoints, agents, artifact, settings] = await Promise.all([
    read("../src/pages/WorkflowsPage.tsx"),
    read("../src/pages/CheckpointsPage.tsx"),
    read("../src/pages/AgentsPage.tsx"),
    read("../src/pages/ArtifactPage.tsx"),
    read("../src/pages/SettingsPage.tsx"),
  ]);
  assert.match(workflows, /value\.set\("workflow", key\)/);
  assert.match(workflows, /value\.set\("tab", next\)/);
  assert.match(checkpoints, /next\.set\("approvalId", item\.approvalId\)/);
  assert.match(checkpoints, /next\.set\("inputId", id\)/);
  assert.match(agents, /next\.set\("attemptId", agent\.attemptId\)/);
  assert.match(agents, /next\.set\("permissionId", value\.id\)/);
  assert.match(artifact, /next\.set\("revision", String\(version\)\)/);
  assert.match(settings, /nextParams\.set\("tab", next\)/);
});
