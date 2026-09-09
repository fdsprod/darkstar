import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

import { operationDefinitions } from "../src/api/schema.generated.ts";
import {
  availableCardActions,
  applyTransitionPlans,
  buildWorkTransitionRequest,
  buildPrepareRunRequest,
  buildCreateWorkItemRequest,
  deriveBoardCards,
  disabledTransitionReason,
  filterBoardCards,
  legalTransitionTargets,
  workflowProfiles,
} from "../src/pages/boardModel.ts";

const timestamp = "2026-09-03T12:00:00Z";

function project(id, name) {
  return {
    id,
    name,
    sourceHash: `source-${id}`,
    status: "active",
    resourceVersion: 1,
    lastGlobalPosition: 1,
    createdAt: timestamp,
    updatedAt: timestamp,
  };
}

function transitionPlan(workItemId, state, decisions) {
  const enabled = new Set(decisions);
  return {
    schemaVersion: 1,
    workItemId,
    state,
    resourceVersion: 1,
    targets: ["backlog", "ready", "running", "waiting", "blocked", "review", "failed", "done"].map((target) => ({
      target,
      availability: enabled.has(target) ? "enabled" : "disabled",
      disabledReasons: enabled.has(target) ? [] : [target === "ready" ? "preparation_required" : "unsupported_target"],
      confirmation: target === "done" && enabled.has(target) ? "required" : "none",
    })),
  };
}

function work(id, projectId, title, overrides = {}) {
  return {
    id,
    projectId,
    title,
    sourceHash: `source-${id}`,
    priority: 0,
    status: "active",
    resourceVersion: 1,
    lastGlobalPosition: 1,
    createdAt: timestamp,
    updatedAt: timestamp,
    ...overrides,
  };
}

function run(id, workItemId, status, globalPosition, overrides = {}) {
  return {
    id,
    workItemId,
    workflowId: "default",
    workflowVersion: "1.0.0",
    status,
    resourceVersion: 1,
    lastGlobalPosition: globalPosition,
    createdAt: timestamp,
    updatedAt: timestamp,
    ...overrides,
  };
}

test("board columns derive from the newest authoritative work and run projections", () => {
  const alpha = project("project_alpha", "Alpha");
  const backlog = work("work_backlog", alpha.id, "Unrouted request", { status: "open" });
  const active = work("work_active", alpha.id, "Running request", { priority: 10 });
  const finished = work("work_done", alpha.id, "Delivered request", { status: "completed" });
  const snapshot = {
    projects: [alpha],
    workItems: [backlog, active, finished],
    runs: [
      run("run_old", active.id, "failed", 5, { updatedAt: "2026-09-03T14:00:00Z" }),
      run("run_current", active.id, "running", 6, { updatedAt: "2026-09-03T13:00:00Z" }),
    ],
  };

  const cards = deriveBoardCards(snapshot);

  assert.deepEqual(cards.map((card) => card.work.id), [active.id, backlog.id, finished.id]);
  assert.deepEqual(
    Object.fromEntries(cards.map((card) => [card.work.id, card.lifecycle])),
    {
      [active.id]: "running",
      [finished.id]: "done",
      [backlog.id]: "backlog",
    },
  );
  assert.equal(cards.find((card) => card.work.id === active.id)?.run?.id, "run_current");
  assert.equal(cards.find((card) => card.work.id === active.id)?.project, alpha);
});

test("the lifecycle plan supplies states such as review that collection projections cannot see", () => {
  const alpha = project("project_alpha", "Alpha");
  const item = work("work_review", alpha.id, "Approve release");
  const fallback = deriveBoardCards({ projects: [alpha], workItems: [item], runs: [run("run_review", item.id, "waiting", 3)] });
  const reviewPlan = { ...transitionPlan(item.id, "review", ["running", "done"]), state: "review", resourceVersion: 4 };

  assert.equal(fallback[0].lifecycle, "waiting");
  assert.equal(applyTransitionPlans(fallback, { [item.id]: reviewPlan })[0].lifecycle, "review");
  assert.equal(fallback[0].lifecycle, "waiting", "reconciliation must not mutate the collection projection");
});

test("a stale lifecycle plan cannot move a concurrently refreshed card backward", () => {
  const alpha = project("project_alpha", "Alpha");
  const item = work("work_concurrent", alpha.id, "Concurrent update", { lastGlobalPosition: 9 });
  const current = deriveBoardCards({ projects: [alpha], workItems: [item], runs: [run("run_current", item.id, "running", 12)] });
  const stale = { ...transitionPlan(item.id, "ready", ["running"]), state: "ready", resourceVersion: 10 };

  assert.equal(applyTransitionPlans(current, { [item.id]: stale })[0].lifecycle, "running");
});

test("board filters combine project and case-insensitive search against projection fields", () => {
  const alpha = project("project_alpha", "Alpha Platform");
  const beta = project("project_beta", "Beta Tools");
  const snapshot = {
    projects: [alpha, beta],
    workItems: [
      work("work_auth", alpha.id, "Rotate bearer credentials"),
      work("work_docs", beta.id, "Publish operator guide"),
      work("work_worker", alpha.id, "Repair worker"),
    ],
    runs: [run("run_worker", "work_worker", "blocked", 8, { workflowId: "incident-repair" })],
  };
  const cards = deriveBoardCards(snapshot);

  assert.deepEqual(
    filterBoardCards(cards, { projectId: alpha.id }).map((card) => card.work.id).sort(),
    ["work_auth", "work_worker"],
  );
  assert.deepEqual(
    filterBoardCards(cards, { query: "BETA TOOLS" }).map((card) => card.work.id),
    ["work_docs"],
  );
  assert.deepEqual(
    filterBoardCards(cards, { projectId: alpha.id, query: "INCIDENT-REPAIR" }).map((card) => card.work.id),
    ["work_worker"],
  );
  assert.deepEqual(
    filterBoardCards(cards, { view: "attention" }).map((card) => card.work.id),
    ["work_worker"],
  );
});

test("create-work normalizes the body and the API client supplies required request headers", async () => {
  const body = buildCreateWorkItemRequest({
    projectId: "  project_alpha ",
    title: "  Ship dashboard  ",
    priority: 4,
  });
  assert.deepEqual(body, {
    projectId: "project_alpha",
    title: "Ship dashboard",
    routingIntent: { mode: "automatic" },
    priority: 4,
  });
  assert.deepEqual(operationDefinitions.createWorkItem, {
    method: "POST",
    path: "/api/v1/work-items",
  });

  const client = await readFile(new URL("../src/api/client.ts", import.meta.url), "utf8");
  const createMethod = client.slice(
    client.indexOf("createWorkItem("),
    client.indexOf("pauseRun("),
  );
  assert.match(createMethod, /this\.operation\("createWorkItem", \{ body, idempotencyKey, signal \}\)/);
  assert.match(client, /headers\.set\("Authorization", authorization\)/);
  assert.match(client, /headers\.set\("Idempotency-Key", options\.idempotencyKey\)/);
  assert.match(client, /headers\.set\("Content-Type", "application\/json"\)/);
  assert.match(client, /body: options\.body === undefined \? undefined : JSON\.stringify\(options\.body\)/);
  assert.match(client, /credentials: "same-origin"/);
});

test("board events cannot locally invent a projected status transition", async () => {
  const reducer = await readFile(new URL("../src/state/dashboardState.ts", import.meta.url), "utf8");
  const eventBranch = reducer.slice(reducer.indexOf('case "event"'), reducer.indexOf("\n  }\n}"));

  assert.match(eventBranch, /advanceEventCursor\(state\.cursor, action\.event\)/);
  assert.doesNotMatch(eventBranch, /snapshot\s*:/);
  assert.doesNotMatch(eventBranch, /status\s*:/);
});

test("create-work validation rejects incomplete or invalid local form input before the API", () => {
  assert.throws(() => buildCreateWorkItemRequest({ projectId: "", title: "Something" }), /Choose a project/);
  assert.throws(() => buildCreateWorkItemRequest({ projectId: "project_alpha", title: "   " }), /Describe the requested outcome/);
  assert.throws(
    () => buildCreateWorkItemRequest({ projectId: "project_alpha", title: "Something", priority: -1 }),
    /Priority must be a whole number/,
  );
});

test("run preparation is distinct from launching a ready run", () => {
  const alpha = project("project_alpha", "Alpha");
  const item = work("work_alpha", alpha.id, "Prepare the release", { status: "open" });
  const backlog = deriveBoardCards({ projects: [alpha], workItems: [item], runs: [] })[0];
  const ready = deriveBoardCards({ projects: [alpha], workItems: [item], runs: [run("run_ready", item.id, "ready", 2)] })[0];

  assert.deepEqual(availableCardActions(backlog, transitionPlan(item.id, "backlog", ["done"])), ["prepare", "cancel"]);
  assert.deepEqual(availableCardActions(ready, transitionPlan(item.id, "ready", ["running", "done"])), ["launch", "cancel"]);
});

test("drag keyboard and menu movement share one typed transition command", () => {
  const preparation = { workflowId: "delivery", workflowVersion: "1.0.0", profile: "fast" };
  assert.deepEqual(buildWorkTransitionRequest("drag", "ready", preparation), buildWorkTransitionRequest("keyboard", "ready", preparation));
  assert.deepEqual(buildWorkTransitionRequest("keyboard", "ready", preparation), buildWorkTransitionRequest("menu", "ready", preparation));
  assert.deepEqual(buildWorkTransitionRequest("drag", "running"), { target: "running" });
  assert.deepEqual(buildWorkTransitionRequest("menu", "done"), { target: "done", confirmation: "confirmed" });
  assert.deepEqual(buildWorkTransitionRequest("menu", "ready"), { target: "ready" });
});

test("board movement exposes only server-enabled targets and preserves precise disabled reasons", () => {
  const plan = transitionPlan("work_alpha", "ready", ["running", "done"]);
  assert.deepEqual(legalTransitionTargets(plan), ["running", "done"]);
  assert.equal(disabledTransitionReason(plan, "ready"), "Choose a workflow before moving to Ready");
  assert.equal(disabledTransitionReason(plan, "blocked"), "No lifecycle command supports this move");
  assert.equal(disabledTransitionReason(undefined, "running"), "Checking current lifecycle rules");
});

test("create-work emits the closed automatic or override routing intent", () => {
  assert.deepEqual(buildCreateWorkItemRequest({ projectId: "project_alpha", title: "Automatic" }), {
    projectId: "project_alpha", title: "Automatic", routingIntent: { mode: "automatic" },
  });
  assert.deepEqual(buildCreateWorkItemRequest({
    projectId: "project_alpha", title: "Bounded", details: "Keep review", evidence: [" plan.md ", "plan.md", "ticket:DAR-144"],
    routingIntent: { mode: "override", workflowId: " delivery ", workflowVersion: " 2.0.0 ", entryNodeId: " design ", terminalNodeIds: [" review ", "review", "publish"] },
  }), {
    projectId: "project_alpha", title: "Bounded", details: "Keep review", evidence: ["plan.md", "ticket:DAR-144"],
    routingIntent: { mode: "override", workflowId: "delivery", workflowVersion: "2.0.0", entryNodeId: "design", terminalNodeIds: ["review", "publish"] },
  });
  assert.throws(() => buildCreateWorkItemRequest({ projectId: "project_alpha", title: "Invalid", routingIntent: { mode: "override", workflowId: " " } }), /Choose a workflow/);
});

test("run preparation normalizes the optional profile without inventing a default", () => {
  assert.deepEqual(buildPrepareRunRequest({
    workItemId: "  work_alpha  ",
    workflowId: "  darkstar/story-execution ",
    workflowVersion: " 1.4.0 ",
    profile: " release ",
  }), {
    workItemId: "work_alpha",
    workflowId: "darkstar/story-execution",
    workflowVersion: "1.4.0",
    profile: "release",
  });
  assert.deepEqual(buildPrepareRunRequest({
    workItemId: "work_alpha",
    workflowId: "darkstar/story-execution",
    workflowVersion: "1.4.0",
    profile: "   ",
  }), {
    workItemId: "work_alpha",
    workflowId: "darkstar/story-execution",
    workflowVersion: "1.4.0",
  });
});

test("workflow profile choices come from the exact selected definition", () => {
  const definition = {
    version: { name: "darkstar/story-execution", version: "1.4.0", digest: "a".repeat(64), sourceScope: "default", sourceReference: "test", installedAt: timestamp },
    document: {
      apiVersion: "darkstar.local/v1alpha2",
      kind: "Workflow",
      metadata: { name: "darkstar/story-execution", version: "1.4.0" },
      spec: {
        routeDefaults: { entry: "design", terminals: ["publish"] },
        profiles: {
          release: { description: "Full release route", entry: "design", terminals: ["publish"], inputDefaults: {} },
          fast: { description: "Fast validation route", entry: "design", terminals: ["validate"], inputDefaults: {} },
        },
        nodes: {},
      },
    },
  };
  assert.deepEqual(workflowProfiles(definition), [
    { id: "fast", description: "Fast validation route" },
    { id: "release", description: "Full release route" },
  ]);
});

test("board movement uses only the work lifecycle plan and apply operations", async () => {
  assert.deepEqual(operationDefinitions.planWorkItemTransition, { method: "GET", path: "/api/v1/work-items/{workItemId}/transition-plan" });
  assert.deepEqual(operationDefinitions.applyWorkItemTransition, { method: "POST", path: "/api/v1/work-items/{workItemId}/transitions" });

  const [client, page] = await Promise.all([
    readFile(new URL("../src/api/client.ts", import.meta.url), "utf8"),
    readFile(new URL("../src/pages/BoardPage.tsx", import.meta.url), "utf8"),
  ]);
  assert.match(client, /planWorkItemTransition\([^\n]*this\.operation\("planWorkItemTransition"/);
  assert.match(client, /applyWorkItemTransition\([^\n]*this\.operation\("applyWorkItemTransition"/);
  assert.doesNotMatch(page, /PrepareRunDialog/);
  assert.match(page, /buildWorkTransitionRequest\(source, target\)/);
  assert.match(page, /apiClient\.applyWorkItemTransition\(card\.work\.id, plan\.resourceVersion,/);
  assert.doesNotMatch(page, /apiClient\.(?:prepareRun|startRun|pauseRun|resumeRun|retryRun|cancelRun)\(/);
  assert.match(page, /Advanced routing/);
  assert.match(page, /draggable=/);
  assert.match(page, /data-drop-available=/);
  assert.doesNotMatch(page, /<details className="move-menu">/);
  assert.doesNotMatch(page, /role="(?:menu|menuitem|dialog)"/);
  assert.match(page, /<aside className="work-quick-panel" aria-labelledby=/);
  assert.match(page, /draggedCard=\{boardCards\.find/);
  assert.doesNotMatch(page, /let draggedCardReference/);
  assert.match(page, /result\.after/);
  assert.match(page, /error\.workTransitionPlan/);
});
