import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

import { contextLocation, latestRun, parseRunContextTab, parseWorkContextTab, runAgents, runPermissions, workNextAction } from "../src/pages/workContextModel.ts";

test("context tabs are closed URL-restored state and preserve unrelated deep-link parameters", () => {
  assert.equal(parseWorkContextTab("evidence"), "evidence");
  assert.equal(parseWorkContextTab("unknown"), "overview");
  assert.equal(parseRunContextTab("agents"), "agents");
  assert.equal(parseRunContextTab("permissions"), "overview");
  assert.equal(contextLocation("/work/work_1/run/run_1", new URLSearchParams("attemptId=attempt_1"), "agents"), "/work/work_1/run/run_1?attemptId=attempt_1&tab=agents");
  assert.equal(contextLocation("/work/work_1", new URLSearchParams("tab=runs&source=board"), "overview"), "/work/work_1?source=board");
});

test("current run and next action derive from authoritative projections", () => {
  const old = { id: "run_old", status: "completed", updatedAt: "2026-09-01T00:00:00Z", lastGlobalPosition: 4 };
  const current = { id: "run_current", status: "running", updatedAt: "2026-09-01T00:00:00Z", lastGlobalPosition: 8 };
  assert.equal(latestRun([old, current]), current);
  assert.deepEqual(workNextAction(current), { label: "Continue current run", tab: "overview" });
  assert.deepEqual(workNextAction(undefined), { label: "Prepare the first run", tab: "runs" });
});

test("run operations never mix agents or permissions from sibling runs", () => {
  const values = [{ id: "one", runId: "run_1" }, { id: "two", runId: "run_2" }];
  assert.deepEqual(runAgents(values, "run_1"), [values[0]]);
  assert.deepEqual(runPermissions(values, "run_2"), [values[1]]);
});

test("full work view keeps diagnostics routes while removing global registries from primary navigation", async () => {
  const [shell, work, run, agents] = await Promise.all([
    readFile(new URL("../src/components/AppShell.tsx", import.meta.url), "utf8"),
    readFile(new URL("../src/pages/WorkDetailPage.tsx", import.meta.url), "utf8"),
    readFile(new URL("../src/pages/RunDetailPage.tsx", import.meta.url), "utf8"),
    readFile(new URL("../src/pages/AgentsPage.tsx", import.meta.url), "utf8"),
  ]);
  assert.doesNotMatch(shell, /const contextualNavigation/);
  assert.match(shell, /contextualDestinations/);
  assert.match(work, /parseWorkContextTab/);
  assert.match(run, /RunLive/);
  assert.doesNotMatch(run, /RunAgentWorkspace/);
  assert.match(run, /Run details/);
  assert.match(run, /availableCardActions/);
  assert.match(run, /control !== "prepare"/);
  assert.match(run, /transitionPlan\.resourceVersion/);
  assert.match(run, /decision\.confirmation === "required"/);
  assert.doesNotMatch(run, /function runControls/);
  assert.doesNotMatch(run, /apiClient\.(?:pauseRun|resumeRun|retryRun|cancelRun)\(/);
  assert.match(agents, /allowedActions/);
});
