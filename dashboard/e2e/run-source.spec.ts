import { expect, test } from "@playwright/test";
import { installEmptyControlPlane } from "./acceptance.fixtures";

test("historical runs display their frozen source even when local work has changed", async ({ page }) => {
  await installEmptyControlPlane(page);
  const at = "2026-09-08T17:00:00Z";
  const run = { id: "run_source_history", workItemId: "work_source_history", workflowId: "test", workflowVersion: "1", status: "completed", resourceVersion: 2, createdAt: at, updatedAt: at };
  const ref = { Namespace: { Provider: "github_issues", Host: "github.com", TenantID: "owner", ScopeID: "repo" }, ID: "issue-1" };
  const sourceSnapshot = {
    runId: run.id, workItemId: run.workItemId, admissionId: "admission-original", observationId: "observation-original", lineageRevision: 1, bindingRevision: 2,
    ref, pin: { AdapterID: "github_issues", AdapterVersion: "1", ConfigRevision: "connection-2" }, approvedAt: at, capturedAt: at,
    ticket: { title: "Original approved title", description: "Original approved instructions", key: "acme/repo#42", revision: "revision-original", url: "https://github.com/acme/repo/issues/42", evidenceRef: "evidence-original", businessState: { state: "known", value: { ID: "open", Name: "Open" } } },
  };
  const effects: string[] = [];
  page.on("request", (request) => {
    if (!["GET", "HEAD"].includes(request.method())) {
      effects.push(request.url());
    }
  });
  await page.route(`**/api/v1/runs/${run.id}`, (route) => route.fulfill({ json: { run, sourceSnapshot, nodes: [], attempts: [], timeline: [], commands: [], timelinePageInfo: { hasEarlier: false }, commandsPageInfo: { hasEarlier: false } } }));
  await page.route(`**/api/v1/work-items/${run.workItemId}`, (route) => route.fulfill({ json: { work: { id: run.workItemId, title: "Later local work title", status: "completed", resourceVersion: 3, createdAt: at, updatedAt: at }, runs: [run], stories: [] } }));
  await page.route("**/api/v1/work-items/*/transition-plan**", (route) => route.fulfill({ json: { resourceVersion: 3, state: "done", targets: [] } }));
  await page.goto(`/work/${run.workItemId}/run/${run.id}`);
  await page.getByText("Ticket version used by this run", { exact: true }).click();
  const panel = page.locator(".run-source-panel");
  await expect(panel.getByRole("heading", { name: "Original approved title", exact: true })).toBeVisible();
  await expect(panel.getByText("Original approved instructions", { exact: true })).toBeVisible();
  await expect(panel.getByText("Open", { exact: true })).toBeVisible();
  await expect(panel).not.toContainText("Later local work title");
  await panel.getByText("Input provenance", { exact: true }).click();
  await expect(panel.getByText("observation-original", { exact: true })).toBeVisible();
  await expect(panel.getByText("evidence-original", { exact: true })).toBeVisible();
  expect(effects).toEqual([]);
});
