import { expect, test, type Page } from "@playwright/test";
import { installEmptyControlPlane } from "./acceptance.fixtures";

async function installBacklog(page: Page) {
  await installEmptyControlPlane(page);
  const now = "2026-09-13T00:00:00Z";
  const namespace = { provider: "github_issues", host: "github.com", tenantId: "owner-node", scopeId: "repo-node" };
  const source = { kind: "external", connectionId: "issues", connectionRevision: "2", scope: { namespace, containerId: "repo-node" } };
  const query = { text: "", predicates: [], pageSize: 50 };
  const ticket = {
    ref: { namespace, id: "issue-node" }, ticketKey: "stable-ticket-key", observationId: "observation-1", bindingRevision: 3,
    revision: "content-1", key: "acme/bugs#42", url: "https://github.com/acme/bugs/issues/42", title: "External business ticket", description: "Original source description",
    status: "cached", reason: "", observedAt: now, checkedAt: now, evidenceRef: "evidence:original",
    businessState: { state: "known", value: { id: "open", name: "Open" } }, priority: { state: "unsupported", reason: "No native priority" },
    assignees: { state: "known", value: [] }, labels: { state: "known", value: [{ id: "label-node", name: "bug" }] }, archived: { state: "unsupported", reason: "No issue archive" }, freshness: "fresh", currentSource: true, currentQueryMatch: true,
  };
  const view = {
    schemaVersion: 1, binding: { projectId: "project_1", revision: 3, source, selectedAt: now }, query,
    refresh: { bindingRevision: 3, phase: "failed", generation: 1, query, startedAt: now, updatedAt: now, lastSuccessAt: now, nextAttemptAt: "", failures: 1, error: { code: "permission_denied", message: "Source access needs attention", retryable: false, details: {} } },
    tickets: [ticket], nextCursor: "", includePrevious: false,
  };
  const effects: string[] = [];
  page.on("request", (request) => {
    if (request.method() !== "GET" && request.method() !== "HEAD") {
      effects.push(new URL(request.url()).pathname);
    }
  });
  await page.route("**/api/v1/projects", (route) => route.fulfill({ json: [{ id: "project_1", name: "Factory", status: "active", resourceVersion: 1, lastGlobalPosition: 1, createdAt: now, updatedAt: now }] }));
  await page.route("**/api/v1/projects/project_1/backlog?**", (route) => route.fulfill({ json: view }));
  await page.route("**/api/v1/projects/project_1/tickets?**", (route) => route.fulfill({ json: { schemaVersion: 1, tickets: [], nextCursor: "" } }));
  await page.route("**/api/v1/tracker/connections", (route) => route.fulfill({ json: { schemaVersion: 1, connections: [] } }));
  await page.route("**/api/v1/projects/project_1/backlog/executions?**", (route) => {
    expect(new URL(route.request().url()).searchParams.get("observationId")).toBe("observation-1");
    return route.fulfill({ json: { schemaVersion: 1, items: [] } });
  });
  return { effects, view };
}

test("provider failure retains the external source and content without execution effects", async ({ page }) => {
  const state = await installBacklog(page);
  await page.goto("/tickets");
  await expect(page.getByText("GitHub Issues · issues (2)", { exact: true })).toBeVisible();
  await expect(page.getByText("Source access needs attention", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: /External business ticket/ }).click();
  await expect(page.getByText("Original source description", { exact: true })).toBeVisible();
  await expect(page.getByRole("link", { name: "Open source ticket" })).toHaveAttribute("href", "https://github.com/acme/bugs/issues/42");
  expect(state.effects).toEqual([]);
});

test("refresh submits the viewed binding and filter without creating work", async ({ page }) => {
  const state = await installBacklog(page);
  const refreshes: unknown[] = [];
  await page.route("**/api/v1/projects/project_1/backlog/refresh", (route) => {
    refreshes.push(route.request().postDataJSON());
    return route.fulfill({ json: { schemaVersion: 1, refresh: state.view.refresh } });
  });
  await page.goto("/tickets");
  await page.getByLabel("Refresh filter").fill("triage");
  await page.getByRole("button", { name: "Refresh source", exact: true }).click();
  await expect.poll(() => refreshes.length).toBe(1);
  expect(refreshes).toEqual([{ schemaVersion: 1, expectedBindingRevision: 3, query: { text: "triage", predicates: [], pageSize: 50 } }]);
  expect(state.effects).toEqual(["/api/v1/projects/project_1/backlog/refresh"]);
});

test("source selection is revision checked and an unconfirmed change keeps the prior view", async ({ page }) => {
  const state = await installBacklog(page);
  const selections: unknown[] = [];
  await page.route("**/api/v1/projects/project_1/backlog/source", (route) => {
    selections.push(route.request().postDataJSON());
    return route.fulfill({ status: 409, json: { code: "TICKET_CONFLICT", message: "Selected source changed" } });
  });
  await page.goto("/tickets");
  await page.getByRole("button", { name: "Change source" }).click();
  await page.getByRole("combobox", { name: "Source connection", exact: true }).selectOption("built_in");
  await page.getByRole("button", { name: "Use selected source" }).click();
  await expect(page.getByText("The source change was not confirmed.", { exact: false })).toBeVisible();
  await expect(page.getByText("GitHub Issues · issues (2)", { exact: true })).toBeVisible();
  expect(selections).toEqual([{ schemaVersion: 1, expectedRevision: 3, source: { kind: "built_in" } }]);
  expect(state.effects).toEqual(["/api/v1/projects/project_1/backlog/source"]);
});

test("a source switch preserves old ticket identity in previous-source history", async ({ page }) => {
  const state = await installBacklog(page);
  let switched = false;
  const binding = { projectId: "project_1", revision: 4, source: { kind: "built_in", namespace: { provider: "built_in", host: "darkstar.local", tenantId: "local", scopeId: "project_1" } }, selectedAt: "2026-09-13T01:00:00Z" };
  await page.route("**/api/v1/projects/project_1/backlog?**", (route) => {
    const previous = new URL(route.request().url()).searchParams.get("includePrevious") === "true";
    return route.fulfill({ json: switched ? {
      ...state.view, binding, refresh: null, includePrevious: previous,
      tickets: previous ? [{ ...state.view.tickets[0], currentSource: false, currentQueryMatch: false, status: "out_of_scope" }] : [],
    } : state.view });
  });
  await page.route("**/api/v1/projects/project_1/backlog/source", (route) => {
    switched = true;
    return route.fulfill({ json: { schemaVersion: 1, binding, history: [state.view.binding, binding] } });
  });
  await page.goto("/tickets");
  await page.getByRole("button", { name: "Change source" }).click();
  await page.getByRole("combobox", { name: "Source connection", exact: true }).selectOption("built_in");
  await page.getByRole("button", { name: "Use selected source" }).click();
  await expect(page.getByText("Built-in tickets", { exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: /External business ticket/ })).toHaveCount(0);
  await page.getByRole("checkbox", { name: "Include previous sources" }).check();
  await page.getByRole("button", { name: /External business ticket/ }).click();
  await expect(page.getByText("Previous source · revision 3", { exact: true })).toBeVisible();
  await expect(page.getByRole("link", { name: "Open source ticket" })).toHaveAttribute("href", state.view.tickets[0].url);
  await expect(page.getByRole("button", { name: "Check this ticket" })).toHaveCount(0);
  expect(state.effects).toEqual(["/api/v1/projects/project_1/backlog/source"]);
});

test("ticket admission and route preparation carry the explicitly approved observation", async ({ page }) => {
  const state = await installBacklog(page);
  const ticket = state.view.tickets[0];
  const sourceTicket = { observationId: ticket.observationId, ref: ticket.ref, revision: ticket.revision, title: ticket.title, description: ticket.description, key: ticket.key, url: ticket.url, businessState: ticket.businessState, observedAt: ticket.observedAt, evidenceRef: ticket.evidenceRef };
  const lineage = { workItemId: "work_1", projectId: "project_1", ticketKey: ticket.ticketKey, revision: 1, bindingRevision: 3, ref: ticket.ref, origin: "admitted", createdAt: ticket.observedAt };
  const source = {
    schemaVersion: 1, workItemId: "work_1", projectId: "project_1", localActivity: "open", runOutcome: "unobserved",
    approvedTicket: sourceTicket, currentTicket: { ...sourceTicket, observationId: "observation-2", title: "Changed after approval", revision: "content-2" }, currentObservationId: "observation-2",
    lineage, lineages: [lineage], approval: { id: "approval-1", workItemId: "work_1", projectId: "project_1", observationId: ticket.observationId, lineageRevision: 1, bindingRevision: 3, approvedAt: ticket.observedAt, actor: "local-user" },
    assessment: { state: "action_required", reasons: ["Source content changed after approval"] }, runs: [], externalAcceptance: { state: "unknown", reason: "No external acceptance observed" },
  };
  const admissions: unknown[] = [];
  const preparations: unknown[] = [];
  await page.route("**/api/v1/projects/project_1/backlog/admit", (route) => {
    admissions.push(route.request().postDataJSON());
    return route.fulfill({ json: { schemaVersion: 1, workItemId: "work_1", sourceObservationId: ticket.observationId, source } });
  });
  await page.route("**/api/v1/work-items/work_1/source", (route) => route.fulfill({ json: source }));
  await page.route("**/api/v1/work-items/work_1", (route) => route.fulfill({ json: {
    schemaVersion: 1, work: { id: "work_1", projectId: "project_1", title: ticket.title, details: ticket.description, evidence: [], routingIntent: { mode: "automatic" }, priority: 0, status: "open", resourceVersion: 1, lastGlobalPosition: 1, createdAt: ticket.observedAt, updatedAt: ticket.observedAt }, runs: [], stories: [], points: [],
  } }));
  await page.route("**/api/v1/runs/prepare", (route) => {
    preparations.push(route.request().postDataJSON());
    return route.fulfill({ status: 503, json: { code: "UNAVAILABLE", message: "Fixture stops before execution" } });
  });
  await page.goto("/tickets");
  await page.getByRole("button", { name: /External business ticket/ }).click();
  await page.getByRole("button", { name: "Approve this version for work" }).click();
  await page.getByRole("link", { name: "Open work and prepare a run" }).click();
  await expect(page.getByRole("button", { name: "Approve current version for next run" })).toBeVisible();
  await page.getByRole("button", { name: "Assess route", exact: true }).click();
  await expect.poll(() => preparations.length).toBe(1);
  expect(admissions).toEqual([{ schemaVersion: 1, expectedBindingRevision: 3, observationId: "observation-1" }]);
  expect(preparations).toEqual([{ workItemId: "work_1", sourceObservationId: "observation-1" }]);
  expect(state.effects).toEqual(["/api/v1/projects/project_1/backlog/admit", "/api/v1/runs/prepare"]);
});

test("board Ready action carries the approved observation without approving newer content", async ({ page }) => {
  const state = await installBacklog(page);
  const ticket = state.view.tickets[0];
  const at = ticket.observedAt;
  const approvedTicket = { observationId: ticket.observationId, ref: ticket.ref, revision: ticket.revision, title: ticket.title, description: ticket.description, key: ticket.key, url: ticket.url, businessState: ticket.businessState, observedAt: at, evidenceRef: ticket.evidenceRef };
  const source = { schemaVersion: 1, workItemId: "work_1", projectId: "project_1", localActivity: "idle", runOutcome: "unobserved", approvedTicket, currentTicket: { ...approvedTicket, observationId: "observation-newer" }, currentObservationId: "observation-newer", lineages: [], lineage: null, approval: { observationId: ticket.observationId }, assessment: { state: "action_required", reasons: [] }, runs: [], externalAcceptance: { state: "unknown", reason: "Unobserved" } };
  const work = { id: "work_1", projectId: "project_1", title: ticket.title, status: "open", resourceVersion: 1, createdAt: at, updatedAt: at };
  const targets = ["backlog", "ready", "running", "waiting", "blocked", "review", "failed", "done"];
  const plan = { schemaVersion: 1, workItemId: work.id, resourceVersion: 1, state: "backlog", targets: targets.map((target) => ({ target, availability: target === "ready" ? "enabled" : "disabled", disabledReasons: [], confirmation: "none" })) };
  let preparedPlan = false;
  const commands: unknown[] = [];
  await page.route("**/api/v1/work-items", (route) => route.fulfill({ json: [work] }));
  await page.route("**/api/v1/work-items/source-views", (route) => route.fulfill({ json: { schemaVersion: 1, items: [source] } }));
  await page.route("**/api/v1/work-items/work_1/transition-plan**", (route) => {
    const query = new URL(route.request().url()).searchParams;
    if (query.has("sourceObservationId")) {
      expect(query.get("target")).toBe("ready");
      expect(query.get("sourceObservationId")).toBe("observation-1");
      preparedPlan = true;
    }
    return route.fulfill({ json: plan });
  });
  await page.route("**/api/v1/work-items/work_1/transitions", (route) => {
    commands.push(route.request().postDataJSON());
    return route.fulfill({ status: 409, json: { code: "WORK_TRANSITION_FAILED", message: "Stop after inspecting the approved request" } });
  });
  await page.goto("/board");
  await expect.poll(() => preparedPlan).toBe(true);
  await page.locator(".work-card").filter({ hasText: ticket.title }).dragTo(page.locator('[data-lifecycle="ready"]'));
  await expect.poll(() => commands.length).toBe(1);
  expect(commands[0]).toMatchObject({ target: "ready", preparation: { sourceObservationId: "observation-1" } });
  expect(state.effects).toEqual(["/api/v1/work-items/work_1/transitions"]);
});
