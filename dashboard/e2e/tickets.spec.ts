import { expect, test, type Page } from "@playwright/test";
import { installEmptyControlPlane } from "./acceptance.fixtures";

async function installTickets(page: Page, editable = true) {
  await installEmptyControlPlane(page);
  const now = "2026-09-13T00:00:00Z";
  const project = { id: "project_1", name: "Factory", status: "active", resourceVersion: 1, lastGlobalPosition: 1, createdAt: now, updatedAt: now };
  let ticket = { id: "ticket_1", projectId: project.id, revision: "1", key: "T-1", url: "", title: "Observe the backlog", description: "Retain the original", businessState: { id: "open", name: "Open" }, priority: 2, assignees: [], labels: [], evidenceRef: "history:1", observedAt: now };
  const history = [{ revision: "1", kind: "created", recordedAt: now, evidenceRef: "history:1", title: ticket.title, description: ticket.description, businessState: "open", priority: 2 }];
  const edits: unknown[] = [];
  const effects: string[] = [];
  const detail = () => ({ schemaVersion: 1, ticket, capabilities: { edit: editable, transitions: editable }, fields: editable ? ["title", "description", "priority"].map((id) => ({ id, name: id, kind: id === "priority" ? "number" : "text", required: id === "title" })) : [], transitions: editable ? [{ id: "complete", name: "Complete ticket", toState: { id: "completed", name: "Completed" } }] : [], history });
  page.on("request", (request) => {
    if (request.method() !== "GET" && request.method() !== "HEAD") {
      effects.push(new URL(request.url()).pathname);
    }
  });
  await page.route("**/api/v1/projects", (route) => route.fulfill({ json: [project] }));
  await page.route("**/api/v1/projects/project_1/tickets?**", (route) => route.fulfill({ json: { schemaVersion: 1, tickets: [ticket], nextCursor: "" } }));
  await page.route("**/api/v1/projects/project_1/tickets/ticket_1", (route) => route.fulfill({ json: detail() }));
  await page.route("**/api/v1/projects/project_1/tickets/ticket_1/edit", (route) => {
    const body = route.request().postDataJSON();
    edits.push(body);
    ticket = { ...ticket, title: body.title, revision: "2" };
    return route.fulfill({ json: detail() });
  });
  await page.goto("/tickets");
  await page.getByText("Built-in ticket history and editing", { exact: true }).click();
  await page.getByRole("button", { name: /Observe the backlog/ }).click();
  await expect(page.getByLabel("Title", { exact: true })).toHaveValue(ticket.title);
  return { edits, effects };
}

test("ticket browsing has no execution effects and edits carry the viewed revision", async ({ page }) => {
  const state = await installTickets(page);
  expect(state.effects).toEqual([]);
  await page.getByLabel("Title", { exact: true }).fill("Updated backlog title");
  await page.getByRole("button", { name: "Save ticket" }).click();
  await expect(page.getByRole("heading", { name: "Updated backlog title" })).toBeVisible();
  expect(state.edits).toEqual([{ schemaVersion: 1, revision: "1", title: "Updated backlog title" }]);
  expect(state.effects).toEqual(["/api/v1/projects/project_1/tickets/ticket_1/edit"]);
  await page.getByText("Ticket history (1 revisions)").click();
  await expect(page.locator(".ticket-history pre")).toHaveText("Retain the original");
});

test("read-only capability keeps ticket actions disabled", async ({ page }) => {
  const state = await installTickets(page, false);
  await expect(page.getByRole("button", { name: "Save ticket" })).toBeDisabled();
  await expect(page.getByRole("button", { name: "Complete ticket" })).toHaveCount(0);
  expect(state.effects).toEqual([]);
});

test("an unconfirmed edit blocks further mutations until the ticket is refreshed", async ({ page }) => {
  const state = await installTickets(page);
  await page.route("**/api/v1/projects/project_1/tickets/ticket_1/edit", (route) => route.fulfill({ status: 409, json: { schemaVersion: 1, code: "TICKET_CONFLICT", message: "Ticket changed", retryable: false } }));
  await page.getByLabel("Title", { exact: true }).fill("Local edit");
  await page.getByRole("button", { name: "Save ticket" }).click();
  await expect(page.getByText("The change was not confirmed.", { exact: false })).toBeVisible();
  await expect(page.getByRole("button", { name: "Save ticket" })).toBeDisabled();
  await expect(page.getByRole("button", { name: "Complete ticket" })).toBeDisabled();
  expect(state.effects).toEqual(["/api/v1/projects/project_1/tickets/ticket_1/edit"]);
  await page.getByRole("button", { name: "Refresh", exact: true }).click();
  await expect(page.getByRole("button", { name: "Complete ticket" })).toBeEnabled();
});
