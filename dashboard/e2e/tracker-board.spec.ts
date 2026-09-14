import { expect, test, type Page } from "@playwright/test";
import { installEmptyControlPlane, projectRepositoryView } from "./acceptance.fixtures";

async function installTrackerBoard(page: Page) {
  await installEmptyControlPlane(page);
  const now = "2026-09-13T00:00:00Z";
  const namespace = { provider: "built_in", host: "darkstar.local", tenantId: "local", scopeId: "project_1" };
  const binding = { projectId: "project_1", revision: 1, source: { kind: "built_in", namespace }, selectedAt: now };
  const query = { text: "", predicates: [], pageSize: 50 };
  const known = (id: string, name: string) => ({ state: "known", value: { id, name } });
  const ticket = {
    ref: { namespace, id: "ticket-one" }, ticketKey: "ticket-one", observationId: "obs-one", bindingRevision: 1,
    currentSource: true, currentQueryMatch: true, revision: "1", key: "LOCAL-1", url: "", title: "Source status independent of execution", description: "Preserved business content",
    status: "fresh", freshness: "fresh", reason: "", checkedAt: now, observedAt: now, evidenceRef: "evidence-one",
    businessState: known("review-custom", "Await customer"), priority: known("0", "Normal"),
    labels: { state: "known", value: [] }, assignees: { state: "known", value: [] }, archived: { state: "known", value: false },
  };
  const tickets = [ticket, { ...ticket, ref: { namespace, id: "ticket-two" }, ticketKey: "ticket-two", observationId: "obs-two", key: "LOCAL-2", title: "Unmapped stale custom status", freshness: "stale", status: "incomplete", businessState: known("new-state", "New provider status") }];
  const columns = [{ id: "customer", name: "Customer review", statusIds: ["review-custom", "review-alt"] }, { id: "accepted", name: "Accepted", statusIds: ["accepted"] }];
  const actions = { "obs-one": [{ id: "accept", name: "Accept result", targetStateId: "accepted", availability: "available", reason: "", automation: ["Automatic repair workflow may be admitted"], requiredFields: [] }] };
  const rules = { version: "darkstar.tracker-rules/v1alpha1", id: "project-mapping", revision: 1, scope: { projectId: "project_1", bindingRevision: 1, pin: {}, source: {} }, intake: [], outbound: [], display: { groups: columns.map((column) => ({ id: column.id, name: column.name, stateIds: column.statusIds })), unknownGroup: { id: "unknown", name: "Unmapped", stateIds: [] } } };
  const history = { schemaVersion: 1, activeRevision: 1, revisions: [{ revision: 1, bindingRevision: 1, rules, createdAt: now }] };
  await page.route("**/api/v1/projects-v2", (route) => route.fulfill({ json: [projectRepositoryView({ id: "project_1", name: "Factory", status: "active", resourceVersion: 1, lastGlobalPosition: 1, createdAt: now, updatedAt: now })] }));
  await page.route("**/api/v1/projects/project_1/backlog?**", (route) => route.fulfill({ json: { schemaVersion: 1, binding, query, refresh: { phase: "failed", lastSuccessAt: now, error: { message: "Partial refresh: retained tickets remain visible" } }, tickets, nextCursor: "", includePrevious: false } }));
  await page.route("**/api/v1/work-items/source-views?**", (route) => route.fulfill({ json: { schemaVersion: 1, items: [] } }));
  await page.route("**/api/v1/projects/project_1/tracker-board", (route) => route.fulfill({ json: { schemaVersion: 1, columns, actions, activeRevision: 1 } }));
  await page.route("**/api/v1/projects/project_1/tracker-mapping", (route) => route.fulfill({ json: history }));
  await page.route("**/api/v1/projects/project_1/tracker-mapping/discovery?**", (route) => route.fulfill({ json: { schemaVersion: 1, bindingRevision: 1, template: rules, fields: [{ id: "state", values: [{ ID: "review-custom", Name: "Await customer" }, { ID: "accepted", Name: "Accepted" }] }], transitions: [{ id: "accept", name: "Accept result", targetStateId: "accepted", requiredFields: [], available: true, reason: "" }], workflows: [], readinessPolicies: [], milestones: [], capabilities: [], creation: { available: true, reason: "" } } }));
  return { actions, history };
}

test("custom board retains unmapped and stale tickets and keyboard movement requests only source transitions", async ({ page }) => {
  await installTrackerBoard(page);
  const commands: unknown[] = [];
  await page.route("**/api/v1/projects/project_1/tracker-board/transition", (route) => {
    commands.push(route.request().postDataJSON());
    return route.fulfill({ json: { schemaVersion: 1 } });
  });
  await page.goto("/tickets");
  await page.getByRole("button", { name: "Tracker board", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Customer review" })).toBeVisible();
  await expect(page.getByText("New provider status", { exact: false })).toBeVisible();
  await expect(page.getByText("Partial refresh: retained tickets remain visible")).toBeVisible();
  const card = page.getByRole("article", { name: "Source status independent of execution, tracker status Await customer" });
  await card.focus();
  await card.press("Space");
  await card.press("ArrowRight");
  page.once("dialog", async (dialog) => {
    expect(dialog.message()).toContain("Automatic repair workflow may be admitted");
    await dialog.accept();
  });
  await card.press("Enter");
  await expect.poll(() => commands.length).toBe(1);
  expect(commands[0]).toEqual({ schemaVersion: 1, observationId: "obs-one", expectedBindingRevision: 1, expectedMappingRevision: 1, transitionId: "accept" });
});

test("mapping preview shows unavailable reason and keeps activation a separate action", async ({ page }) => {
  await installTrackerBoard(page);
  const previews: unknown[] = [];
  await page.route("**/api/v1/projects/project_1/tracker-mapping/preview", (route) => {
    previews.push(route.request().postDataJSON());
    return route.fulfill({ json: { schemaVersion: 1, valid: false, issues: [{ field: "outbound", code: "missing_evidence", message: "Acceptance evidence is required" }], requestedAction: { kind: "transition", transitionId: "accept" }, requiredFields: ["resolution"], approval: "required", reason: "Acceptance evidence is required" } });
  });
  await page.goto("/tickets");
  await page.getByRole("button", { name: "Workflow mappings", exact: true }).click();
  await page.getByRole("button", { name: "Validate and preview", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Configuration blocked" })).toBeVisible();
  await expect(page.getByText("Required fields: resolution", { exact: true })).toBeVisible();
  await expect(page.getByText("Unavailable because: Acceptance evidence is required", { exact: true })).toBeVisible();
  expect(previews).toHaveLength(1);
  await expect(page.getByRole("button", { name: "Active", exact: true })).toBeDisabled();
});
