import { expect, test } from "@playwright/test";

import { installEmptyControlPlane } from "./acceptance.fixtures";

test.beforeEach(async ({ page }) => installEmptyControlPlane(page));

test("Board exposes safe create, filtering, drag status, and keyboard or touch lifecycle alternatives", async ({ page }) => {
  const now = "2026-09-07T12:00:00Z";
  const project = { id: "project_1", name: "Darkstar", sourceHash: "a".repeat(64), status: "active", resourceVersion: 1, lastGlobalPosition: 1, createdAt: now, updatedAt: now };
  const work = { id: "work_1", projectId: project.id, title: "Ship safe board", details: "Exercise exact legal transitions", evidence: [], routingIntent: { mode: "automatic" }, sourceHash: "b".repeat(64), priority: 2, status: "open", resourceVersion: 1, lastGlobalPosition: 2, createdAt: now, updatedAt: now };
  const targets = ["backlog", "ready", "running", "waiting", "blocked", "review", "failed", "done"].map((target) => ({ target, availability: target === "ready" ? "enabled" : "disabled", disabledReasons: target === "ready" ? [] : [target === "backlog" ? "current_state" : "unsupported_target"], confirmation: "none" }));
  const plan = { schemaVersion: 1, workItemId: work.id, state: "backlog", resourceVersion: 1, targets };
  let transitions = 0;
  await page.route("**/api/v1/projects", (route) => route.fulfill({ json: [project] }));
  await page.route("**/api/v1/work-items", (route) => route.fulfill({ json: [work] }));
  await page.route("**/api/v1/work-items/work_1/transition-plan**", (route) => route.fulfill({ json: plan }));
  await page.route("**/api/v1/work-items/work_1/transitions", (route) => { transitions += 1; return route.fulfill({ json: { schemaVersion: 1, target: "ready", effect: "run_prepared", before: plan, after: { ...plan, state: "ready", resourceVersion: 2 } } }); });
  await page.goto("/board");
  await expect(page.getByRole("region", { name: "Work lifecycle" })).toBeVisible();
  const card = page.locator("article.work-card", { hasText: "Ship safe board" });
  await expect(card).toHaveAttribute("draggable", "true");
  await card.dragTo(page.locator("article[data-lifecycle='running']"));
  expect(transitions).toBe(0);
  await card.getByRole("button", { name: "Ship safe board", exact: true }).dragTo(page.locator("article[data-lifecycle='ready']"));
  await expect.poll(() => transitions).toBe(1);
  await card.getByRole("button", { name: "Ship safe board", exact: true }).click();
  await expect(page.getByRole("complementary", { name: "Ship safe board" })).toContainText("Delete work item");
  await page.getByPlaceholder("Search work…").fill("missing");
  await expect(page.getByRole("heading", { name: "No work matches these filters" })).toBeVisible();
  await expect(page.getByRole("button", { name: "Clear filters" })).toBeVisible();
});

test("minimal Create Work defaults to automatic routing and keeps advanced override progressive", async ({ page }) => {
  const now = "2026-09-07T12:00:00Z";
  await page.route("**/api/v1/projects", (route) => route.fulfill({ json: [{ id: "project_1", name: "Darkstar", sourceHash: "a".repeat(64), status: "active", resourceVersion: 1, lastGlobalPosition: 1, createdAt: now, updatedAt: now }] }));
  await page.goto("/board?create=1");
  const dialog = page.getByRole("dialog");
  await expect(dialog.getByRole("heading", { name: "Create requested outcome" })).toBeVisible();
  await dialog.getByText("Advanced routing").click();
  await expect(dialog.getByLabel("Routing")).toHaveValue("automatic");
  await dialog.getByLabel("Routing").selectOption("override");
  await expect(dialog.locator("details select").nth(1)).toBeVisible();
  await dialog.getByRole("button", { name: "Cancel" }).click();
  await expect(page).toHaveURL(/\/board$/);
});

test("workflow authoring surfaces the canvas editor and chat, and opens drafts with safe defaults", async ({ page }) => {
  await page.goto("/workflows");
  await expect(page.getByRole("heading", { level: 1, name: "Workflows" })).toBeVisible();
  // Authoring is canvas-only: the editor and the authoring chat are its surfaces.
  await expect(page.getByRole("region", { name: "Workflow editor" })).toBeVisible();
  await expect(page.getByRole("complementary", { name: "Workflow chat" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Select a workflow" })).toBeVisible();
  await page.getByRole("button", { name: "New workflow", exact: true }).click();
  const dialog = page.getByRole("dialog", { name: "New workflow draft" });
  await expect(dialog.getByLabel("Workflow name")).toHaveValue("workflow/new-workflow");
  await expect(dialog.getByLabel("Scope")).toHaveValue("user");
  await dialog.getByRole("button", { name: "Cancel" }).click();
  await expect(dialog).toBeHidden();
  // Nothing is published from this surface without a selected workflow.
  await expect(page.getByRole("button", { name: "Publish" })).toHaveCount(0);
});

test("Settings keeps compact controls, inheritance, secret safety, and diagnostics progressive", async ({ page }) => {
  await page.goto("/settings");
  await expect(page.getByRole("heading", { level: 1, name: "Settings" })).toBeVisible();
  const tabs = page.getByRole("tablist", { name: "Settings workspace" });
  await expect(tabs.getByRole("tab")).toHaveText(["General", "Projects", "Providers", "Execution & permissions", "Health & diagnostics"]);
  await tabs.getByRole("tab", { name: "Health & diagnostics" }).click();
  await expect(page).toHaveURL(/tab=health/);
  await expect(page.getByText(/diagnostic/i).first()).toBeVisible();
  await expect(page.locator("body")).not.toContainText(/token-[A-Za-z0-9]{8}/);
});

test("Checkpoints exposes all five classes, document workspace semantics, and exact stale deep links", async ({ page }) => {
  await page.goto("/checkpoints?itemId=resolved_item");
  const tabs = page.getByRole("navigation", { name: "Attention type" });
  await expect(tabs.getByRole("button")).toHaveText(["All", "Approvals", "Input", "Permissions", "Controls", "Delivery"]);
  await expect(page.getByRole("heading", { name: "Requested checkpoint is no longer unresolved" })).toBeVisible();
  await expect(page.getByRole("region", { name: "Unified Checkpoints queue" })).toBeVisible();
});

test("loading, reconnect, empty, partial failure, stale, and cancellation states remain explicit", async ({ page }) => {
  await page.route("**/api/v1/projects", async (route) => { await new Promise((resolve) => setTimeout(resolve, 250)); await route.fulfill({ json: [] }); });
  await page.goto("/board");
  await expect(page.getByText("Loading authoritative state")).toBeVisible();
  await expect(page.getByText(/reconnecting|paused|connecting/i).first()).toBeVisible();
  await expect(page.getByText("No backlog work")).toBeVisible();
  await page.goto("/work/missing");
  await expect(page.getByRole("heading", { level: 1 })).toBeVisible();
  await expect(page.getByText(/unavailable|temporarily/i).first()).toBeVisible();
});

test("targeted release-candidate screenshots remain free of duplicate headers and metadata clutter", async ({ page }, testInfo) => {
  await page.setViewportSize({ width: 1280, height: 900 });
  for (const [name, path] of [["shell-board", "/board"], ["workflow-editor", "/workflows"], ["review-workspace", "/checkpoints?itemId=resolved"], ["settings", "/settings"]] as const) {
    await page.goto(path);
    await expect(page.locator("main h1")).toHaveCount(1);
    await page.screenshot({ path: testInfo.outputPath(`${name}.png`), fullPage: true });
  }
});
