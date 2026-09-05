import assert from "node:assert/strict";
import { readFile, readdir } from "node:fs/promises";
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
  assert.match(settings, /next\.set\("tab", nextTab\)/);
  assert.match(settings, /next\.set\("setting", key\)/);
  assert.match(settings, /next\.set\("projectId", nextScope\.projectId\)/);
});

test("shared interaction patterns distinguish actions, async states, and empty states", async () => {
  const [patterns, structure, styles, pageNames] = await Promise.all([
    read("../src/components/InteractionPatterns.tsx"),
    read("../src/components/PageStructure.tsx"),
    read("../src/styles.css"),
    readdir(new URL("../src/pages/", import.meta.url)),
  ]);
  const pages = (await Promise.all(pageNames.filter((name) => name.endsWith(".tsx")).map((name) => read(`../src/pages/${name}`)))).join("\n");

  for (const component of ["ActionBar", "SectionHeader", "StatusBadge", "AsyncPanel", "EmptyState", "ActionGuidance"]) {
    assert.match(patterns, new RegExp(`export function ${component}`));
  }
  for (const state of ["loading", "success", "error", "stale", "cancelled", "validation"]) assert.match(patterns, new RegExp(`"${state}"`));
  for (const kind of ["empty", "filtered", "awaiting", "unavailable"]) assert.match(patterns, new RegExp(`"${kind}"`));
  assert.match(patterns, /aria-busy=\{busy\}/);
  assert.match(patterns, /role=\{failure \? "alert" : "status"\}/);
  assert.match(structure, /<ActionBar/);
  assert.match(structure, /<StatusBadge tone="readonly"/);
  assert.match(styles, /\.dialog-draft-note/);
  assert.match(pages, /Closing discards unsaved changes/);
  assert.doesNotMatch(pages, /<AppLink[^>]*className="button/);
  assert.doesNotMatch(pages, />Back<\/button>/);
  assert.doesNotMatch(pages, /This view is ready for live data/);
  assert.match(pages, /kind="filtered"/);
  assert.match(pages, /kind="awaiting"/);
  assert.match(pages, /kind="unavailable"/);
  assert.match(pages, /aria-describedby=\{!canCreate \? "create-work-guidance"/);
});
