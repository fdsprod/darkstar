import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

import { operationDefinitions } from "../src/api/schema.generated.ts";
import {
  buildCreateWorkItemRequest,
  deriveBoardCards,
  filterBoardCards,
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
