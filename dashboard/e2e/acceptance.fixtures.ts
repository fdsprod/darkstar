import type { Page } from "@playwright/test";

const apiError = { code: "fixture_unavailable", message: "Acceptance fixture has no domain record for this request." };

export async function installEmptyControlPlane(page: Page) {
  await page.route("**/api/v1/events**", (route) => route.abort());
  await page.route("**/api/v1/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    const body = emptyResponse(path);
    if (body !== undefined) {
      await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(body) });
      return;
    }
    await route.fulfill({ status: 503, contentType: "application/json", body: JSON.stringify(apiError) });
  });
}

function emptyResponse(path: string): unknown {
  if (path === "/api/v1/projects" || path === "/api/v1/work-items" || path === "/api/v1/workflows" || path === "/api/v1/artifacts") return [];
  if (path === "/api/v1/runs") return { items: [], pageInfo: { nextCursor: null } };
  if (path === "/api/v1/workflows/library") return { versions: [], drafts: [], archives: [] };
  if (path === "/api/v1/content-library") return { items: [] };
  if (path === "/api/v1/workflows/node-definitions") return [];
  if (path === "/api/v1/workflows/authoring-catalog") return {
    schemaVersion: 1,
    nodeTypes: ["reasoning", "gate", "command", "approval", "subworkflow", "point_execution"],
    valueTypes: ["null", "boolean", "integer", "number", "string", "array", "object"],
    checkpointModes: ["none", "acknowledge", "approve", "approve_on_change", "external"],
    predicateOps: ["const", "eq", "ne", "lt", "lte", "gt", "gte", "present", "all", "any", "not"],
    agents: { status: "unavailable", reason: "not_configured" }, policies: { status: "unavailable", reason: "not_configured" },
    schemas: { status: "unavailable", reason: "not_configured" }, skills: { status: "unavailable", reason: "not_configured" },
    tools: { status: "unavailable", reason: "not_configured" }, workflows: { status: "known", items: [] },
  };
  if (path === "/api/v1/attention/v2") return { schemaVersion: 2, generatedAt: "2026-09-07T12:00:00Z", updatedAt: "2026-09-07T12:00:00Z", totalCount: 0, items: [] };
  if (path === "/api/v1/agents" || path === "/api/v1/agents/permissions") return { items: [], totalCount: 0 };
  return undefined;
}
