import { expect, test } from "@playwright/test";
import { installEmptyControlPlane } from "./acceptance.fixtures";

test("implementation draft shows actual file editing and task configuration", async ({ page }) => {
  await installEmptyControlPlane(page);
  const document = { apiVersion: "darkstar.local/v1alpha3", kind: "Workflow", metadata: { name: "implementation-example", version: "1.0.0" }, spec: { inputs: { task: { type: "task", resource: { kind: "task" } } }, routeDefaults: { entry: "implement", terminals: ["implement"] }, nodes: { implement: { type: "implementation", displayName: "Update README", entry: true, terminal: true, inputs: { task: { type: "task", from: "run.input.task" } }, outputs: { changeset: { type: "object" } }, implementation: { taskInput: "task", instructions: "Update README.md on disk." }, permissions: ["process.run", "workspace.write"], transitions: [] } } } };
  const draft = { id: "implementation-draft", name: "implementation-example", scope: "user", scopeReference: "local-user", revision: 1, document, layout: {}, documentDigest: "b".repeat(64), updatedAt: "2026-09-08T00:00:00Z" };
  await page.route("**/api/v1/workflows/library", route => route.fulfill({ json: { versions: [], drafts: [draft], archives: [] } }));
  await page.route("**/api/v1/workflows/drafts/implementation-draft", route => route.fulfill({ json: draft }));
  await page.goto("/workflows?item=draft%3Aimplementation-draft&view=canvas&selection=node%3Aimplement");
  await expect(page.getByText("Implementation · edits files", { exact: true })).toBeVisible();
  await expect(page.getByText(/Changes files in/)).toBeVisible();
  await expect(page.getByRole("textbox", { name: "Task input", exact: true })).toHaveValue("task");
  await expect(page.getByRole("button", { name: "Done, done node", exact: true })).toBeAttached();
  await expect(page.locator(".workflow-flow-node--start")).toHaveCSS("border-top-color", "rgb(74, 222, 128)");
});

test("chat forks a read-only workflow, updates the canvas, asks questions, and leaves publishing to the human", async ({ page }, testInfo) => {
  await page.setViewportSize({ width: 1600, height: 1000 });
  await installEmptyControlPlane(page);
  await page.route("**/api/v1/workflows/chat/models", route => route.fulfill({ json: [{ id: "test-model", name: "Test Model", isDefault: true, defaultEffort: "low", efforts: ["low", "high"] }, { id: "other-model", name: "Other Model", isDefault: false, defaultEffort: "medium", efforts: ["medium"] }] }));
  const original = { apiVersion: "darkstar.local/v1alpha2", kind: "Workflow", metadata: { name: "chat-example", version: "1.0.0" }, spec: { routeDefaults: { entry: "review", terminals: ["review"] }, nodes: { review: { type: "approval", displayName: "Initial review", entry: true, terminal: true, inputs: {}, outputs: {}, approval: { actor: "workflow-owner" }, transitions: [] } } } };
  const version = { name: "chat-example", version: "1.0.0", digest: "a".repeat(64), sourceScope: "default", sourceReference: "shipped", installedAt: "2026-09-08T00:00:00Z" };
  const edited = structuredClone(original); edited.spec.nodes.review.displayName = "Human deployment review";
  const draft = { id: "chat-draft", name: "chat-example", scope: "user", scopeReference: "local-user", baseVersion: "1.0.0", revision: 2, document: edited, layout: {}, documentDigest: "b".repeat(64), updatedAt: "2026-09-08T00:00:00Z" };
  await page.route("**/api/v1/workflows/library", route => route.fulfill({ json: { versions: [version], drafts: [], archives: [] } }));
  await page.route("**/api/v1/workflows/show?**", route => route.fulfill({ json: { version, document: original } }));
  const requests: any[] = []; let publishes = 0;
  await page.route("**/api/v1/workflows/drafts/publish", route => { publishes++; return route.abort(); });
  await page.route("**/api/v1/workflows/chat", route => {
    requests.push(route.request().postDataJSON());
    const events = requests.length === 1 ? [
      { kind: "draft", payload: draft },
      { kind: "validation", payload: { draftId: draft.id, revision: 2, documentDigest: draft.documentDigest, digest: "c".repeat(64), findings: [] } },
      { kind: "question", payload: { question: "Should deployment require a second reviewer?", options: ["One reviewer", "Two reviewers"] } },
      { kind: "done", payload: { message: "Complete" } },
    ] : [{ kind: "text", payload: { text: "Kept one reviewer." } }, { kind: "done", payload: { message: "Complete" } }];
    return route.fulfill({ contentType: "application/x-ndjson", body: events.map(event => JSON.stringify(event)).join("\n") + "\n" });
  });
  await page.goto("/workflows");
  await expect(page.getByText("Read only · create a new version to edit")).toBeVisible();
  await expect(page.getByRole("checkbox", { name: "Create a new workflow from scratch" })).toHaveCount(0);
  await expect(page.getByRole("combobox", { name: "Chat model" })).toHaveValue("test-model");
  await page.getByRole("combobox", { name: "Chat model" }).selectOption("other-model");
  await expect(page.getByRole("combobox", { name: "Chat effort" })).toHaveValue("medium");
  await expect(page.getByRole("combobox", { name: "Chat effort" }).locator("option")).toHaveCount(1);
  await page.getByRole("combobox", { name: "Chat model" }).selectOption("test-model");
  await page.getByRole("combobox", { name: "Chat effort" }).selectOption("high");
  await page.reload();
  await expect(page.getByRole("combobox", { name: "Chat model" })).toHaveValue("test-model");
  await expect(page.getByRole("combobox", { name: "Chat effort" })).toHaveValue("high");
  await page.getByRole("textbox", { name: "Message", exact: true }).fill("Make this the human deployment review");
  await page.getByRole("button", { name: "Send", exact: true }).click();
  await expect(page.getByText("Human deployment review", { exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "One reviewer", exact: true })).toBeEnabled();
  await expect(page.getByRole("button", { name: "Publish version", exact: true })).toBeEnabled();
  expect(requests[0].target).toEqual({ kind: "version", name: "chat-example", version: "1.0.0" });
  expect(requests[0].generation).toEqual({ model: "test-model", effort: "high" });
  await expect(page.locator(".workflow-chat-composer").getByRole("button", { name: "Send", exact: true })).toBeVisible();
  await page.getByRole("button", { name: "One reviewer", exact: true }).click();
  await page.getByRole("textbox", { name: "Message", exact: true }).press("Enter");
  await expect(page.getByText("Kept one reviewer.", { exact: true })).toBeVisible();
  await expect(page.getByText("Waiting for your reply", { exact: true })).toHaveCount(0);
  expect(requests[1].target).toEqual({ kind: "draft", id: "chat-draft", revision: 2 });
  expect(publishes).toBe(0);
  expect(original.spec.nodes.review.displayName).toBe("Initial review");
  await page.screenshot({ path: testInfo.outputPath("workflow-chat.png") });
});
