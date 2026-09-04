import assert from "node:assert/strict";
import { readdir, readFile } from "node:fs/promises";
import test from "node:test";

import { tabKeyTarget } from "../src/accessibility/keyboard.ts";

const read = (path) => readFile(new URL(path, import.meta.url), "utf8");

test("horizontal tabs implement cyclic arrows and boundary keys", () => {
  assert.equal(tabKeyTarget(0, "ArrowRight", 4), 1);
  assert.equal(tabKeyTarget(3, "ArrowRight", 4), 0);
  assert.equal(tabKeyTarget(0, "ArrowLeft", 4), 3);
  assert.equal(tabKeyTarget(2, "Home", 4), 0);
  assert.equal(tabKeyTarget(1, "End", 4), 3);
  assert.equal(tabKeyTarget(1, "Enter", 4), undefined);
  assert.equal(tabKeyTarget(-1, "ArrowRight", 4), undefined);
  assert.equal(tabKeyTarget(0, "ArrowRight", 0), undefined);
});

test("dashboard pages keep one shell-owned main landmark", async () => {
  const pageDirectory = new URL("../src/pages/", import.meta.url);
  const pageFiles = (await readdir(pageDirectory)).filter((name) => name.endsWith(".tsx"));
  const pages = await Promise.all(pageFiles.map((name) => read(`../src/pages/${name}`)));
  for (const [index, source] of pages.entries()) {
    assert.doesNotMatch(source, /<\/?main\b/, `${pageFiles[index]} must not nest a main landmark`);
  }
  const shell = await read("../src/components/AppShell.tsx");
  assert.equal(shell.match(/<main\b/g)?.length, 1);
  assert.equal(shell.match(/<\/main>/g)?.length, 1);
});

test("every dashboard tabset has roving focus and persistent controlled panels", async () => {
  const [agents, checkpoints, workflows, artifacts, settings] = await Promise.all([
    read("../src/pages/AgentsPage.tsx"),
    read("../src/pages/CheckpointsPage.tsx"),
    read("../src/pages/WorkflowsPage.tsx"),
    read("../src/pages/ArtifactsPage.tsx"),
    read("../src/pages/SettingsPage.tsx"),
  ]);
  for (const source of [agents, checkpoints, workflows, artifacts, settings]) {
    assert.match(source, /tabKeyTarget/);
    assert.match(source, /role="tab"[^>]*tabIndex=/);
    assert.match(source, /onKeyDown=/);
  }
  for (const [source, ids] of [
    [agents, ["agent-panel-executions", "agent-panel-permissions"]],
    [checkpoints, ["checkpoint-panel-reviews", "checkpoint-panel-inputs"]],
    [workflows, ["workflow-panel-preview", "workflow-panel-graph", "workflow-panel-readiness", "workflow-panel-definition"]],
    [artifacts, ["evidence-source-panel-file", "evidence-source-panel-paste"]],
    [settings, ["settings-panel-health", "settings-panel-provider", "settings-panel-projects", "settings-panel-configuration"]],
  ]) {
    for (const id of ids) {
      const dynamicControl = /aria-controls=\{`(?:settings-panel-\$\{item\.id\}|workflow-panel-\$\{id\})`\}/.test(source);
      const dynamicPanel = /id=\{`settings-panel-\$\{item\.id\}`\}/.test(source);
      assert.ok(source.includes(`aria-controls="${id}"`) || dynamicControl, `tab must control ${id}`);
      assert.ok(source.includes(`id="${id}"`) || dynamicPanel, `panel ${id} must remain in the DOM`);
    }
    assert.match(source, /role="tabpanel"/);
    assert.match(source, /hidden=/);
  }
});

test("focus, motion, and non-color status styling remain explicit", async () => {
  const [styles, detail] = await Promise.all([read("../src/styles.css"), read("../src/pages/WorkDetailPage.tsx")]);
  assert.match(styles, /prefers-reduced-motion: reduce/);
  assert.match(styles, /button:focus-visible/);
  assert.match(styles, /\.artifact-dropzone:focus-within/);
  const textColors = [...styles.matchAll(/(?:^|[;{])\s*color:\s*(#[0-9a-f]{6})/gim)].map((match) => match[1].toLowerCase());
  for (const color of textColors) {
    if (color === "#0a1020" || color === "#3e4757") continue; // dark text on the primary button; decorative aria-hidden separators
    assert.ok(contrast(color, "#0d111a") >= 4.5, `${color} must retain normal-text contrast against the dashboard surface`);
  }
  assert.match(detail, /<span aria-hidden="true" \/>\{humanize\(status\)\}/);
  assert.match(detail, /aria-hidden="true"/);
});

function contrast(left, right) {
  const values = [relativeLuminance(left), relativeLuminance(right)].sort((a, b) => b - a);
  return (values[0] + 0.05) / (values[1] + 0.05);
}

function relativeLuminance(hex) {
  const channels = [1, 3, 5].map((start) => Number.parseInt(hex.slice(start, start + 2), 16) / 255)
    .map((value) => value <= 0.04045 ? value / 12.92 : ((value + 0.055) / 1.055) ** 2.4);
  return 0.2126 * channels[0] + 0.7152 * channels[1] + 0.0722 * channels[2];
}

test("every dashboard write has an OpenAPI, CLI, and durable event mapping", async () => {
  const [client, docs, openapiText, workService, runService, artifactService, runControl, readinessControl, checkpointService, inputService, permissionService] = await Promise.all([
    read("../src/api/client.ts"),
    read("../../runtime/docs/cli.md"),
    read("../../schemas/openapi-v1alpha1.json"),
    read("../../runtime/src/core/workmanagement/service.go"),
    read("../../runtime/src/core/runexecution/service.go"),
    read("../../runtime/src/core/artifactops/service.go"),
    read("../../runtime/src/core/runexecution/control.go"),
    read("../../runtime/src/core/readinesscontrol/service.go"),
    read("../../runtime/src/core/artifactcheckpoint/service.go"),
    read("../../runtime/src/core/runexecution/inputs.go"),
    read("../../runtime/src/core/runexecution/permissions.go"),
  ]);
  const eventSources = [workService, runService, artifactService, runControl, readinessControl, checkpointService, inputService, permissionService].join("\n");
  const pageDirectory = new URL("../src/pages/", import.meta.url);
  const pageFiles = (await readdir(pageDirectory)).filter((name) => name.endsWith(".tsx"));
  const pageSource = (await Promise.all(pageFiles.map((name) => read(`../src/pages/${name}`)))).join("\n");
  const usedMethods = new Set([...pageSource.matchAll(/apiClient\.([A-Za-z0-9_]+)\s*\(/g)].map((match) => match[1]));
  const openapi = JSON.parse(openapiText);
  const operations = new Map();
  for (const pathItem of Object.values(openapi.paths)) {
    for (const [verb, operation] of Object.entries(pathItem)) {
      if (operation?.operationId) operations.set(operation.operationId, verb);
    }
  }
  const mappings = [
    ["registerProject", "registerProject", "darkstar project register", "project.created"],
    ["createWorkItem", "createWorkItem", "darkstar work create", "work.created"],
    ["createRun", "createOrStartRun", "darkstar run start", "run.created"],
    ["pauseRun", "pauseRun", "darkstar run pause", "run.paused"],
    ["resumeRun", "resumeRun", "darkstar run resume", "run.resumed"],
    ["retryRun", "retryRun", "darkstar run retry", "run.retried"],
    ["cancelRun", "cancelRun", "darkstar run cancel", "run.cancelled"],
    ["decideRunReadiness", "decideRunReadiness", "darkstar run readiness decide", "readiness.decision_recorded"],
    ["decideApproval", "decideApproval", "darkstar approval decide", "approval.decided"],
    ["answerInputRequest", "answerInputRequest", "darkstar input answer", "input.answer_recorded"],
    ["retryInputDelivery", "retryInputRequestDelivery", "darkstar input retry", "input.answer_delivered"],
    ["ingestArtifact", "ingestArtifact", "darkstar artifact ingest", "artifact.ingested"],
    ["reviseArtifact", "reviseArtifact", "darkstar artifact revise", "artifact.revised"],
    ["extractArtifact", "extractArtifact", "darkstar artifact extract", "artifact.extracted"],
    ["attachArtifact", "attachArtifact", "darkstar artifact attach", "artifact.attached"],
    ["cancelAgent", "cancelAgent", "darkstar agent cancel", "run.cancelled"],
    ["decideProviderPermission", "decideProviderPermission", "darkstar agent permissions decide", "permission.decision_recorded"],
    ["retryProviderPermissionDelivery", "retryProviderPermissionDelivery", "darkstar agent permissions retry", "permission.response_delivered"],
  ];
  const expectedWrites = new Set(mappings.map(([method]) => method));
  const readOnlyPostOperations = new Set(["previewWorkflowRoute", "assessArtifactImpact"]);
  const usedWrites = new Set();
  for (const method of usedMethods) {
    const escaped = method.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
    const operation = new RegExp(`${escaped}\\([^\\n]*?this\\.operation\\(\"([^\"]+)\"`).exec(client)?.[1];
    if (operation && operations.get(operation) !== "get" && !readOnlyPostOperations.has(operation)) usedWrites.add(method);
  }
  assert.deepEqual([...usedWrites].sort(), [...expectedWrites].sort(), "dashboard writes changed without updating the parity inventory");
  for (const [method, operation, command, event] of mappings) {
    assert.ok(usedMethods.has(method), `${method} must be exercised by the dashboard`);
    assert.match(client, new RegExp(`${method}\\([^\\n]*this\\.operation\\(\"${operation}\"`));
    assert.notEqual(operations.get(operation), "get", `${operation} must be a write operation`);
    assert.ok(docs.includes(command), `CLI docs must include ${command}`);
    assert.ok(eventSources.includes(event), `runtime must append ${event}`);
  }
  assert.doesNotMatch(workService, /Actor: statestore\.Actor\{Type: statestore\.ActorUser, ID: "cli"/);
  assert.doesNotMatch(runService, /statestore\.ActorUser, "cli"/);
  assert.match(workService, /ID: "local-user"/);
  assert.match(runService, /statestore\.ActorUser, "local-user"/);
});
