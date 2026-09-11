import { expect, test } from "@playwright/test";
import { installEmptyControlPlane } from "./acceptance.fixtures";

test("authors add linked stage presets and submit explicit Assessment Router exits", async ({ page }) => {
  await installEmptyControlPlane(page);
  await page.setViewportSize({ width: 1600, height: 1100 });
  const makeNode = (name: string) => ({ type: "reasoning", displayName: name, entry: true, terminal: true, inputs: {}, outputs: { document: { type: "markdown", artifact: { filename: `${name}.md` } } }, checkpoint: { mode: "approve" }, reasoning: { agent: "designer" }, transitions: [] });
  const reference = { id: "plan_prompt", version: "1.0.0", digest: "a".repeat(64) };
  const preset = { id: "plan", name: "Implementation Plan", description: "Plan implementation and proof.", node: { ...makeNode("Plan"), prompt: reference }, inputs: {}, optionalInputs: { open_items: { type: "open_items" }, deferred_work: { type: "open_items" } } };
  let draft: any = { id: "draft_stages", name: "test/stages", scope: "user", scopeReference: "local-user", revision: 1, document: { apiVersion: "darkstar.local/v1alpha3", kind: "Workflow", metadata: { name: "test/stages", version: "1.0.0" }, spec: { inputs: {}, routeDefaults: { entry: "questions", terminals: ["technical_design"] }, nodes: { questions: makeNode("Questions"), research: makeNode("Research"), product_design: makeNode("Product Design"), technical_design: makeNode("Technical Design") } } }, layout: {}, documentDigest: "b".repeat(64), updatedAt: "2026-09-11T00:00:00Z" };
  let routerRequest: any;
  await page.route("**/api/v1/workflows/library", (route) => route.fulfill({ json: { versions: [], drafts: [draft], archives: [] } }));
  await page.route("**/api/v1/workflows/authoring-catalog", (route) => route.fulfill({ json: { schemaVersion: 1, nodeTypes: ["reasoning", "gate"], valueTypes: ["markdown"], checkpointModes: ["approve"], predicateOps: [], agents: { status: "known", items: ["designer"] }, policies: { status: "known", items: [] }, schemas: { status: "known", items: [] }, skills: { status: "known", items: [] }, tools: { status: "known", items: [] }, workflows: { status: "known", items: [] }, stagePresets: [preset] } }));
  await page.route("**/api/v1/workflows/drafts/update", async (route) => {
    const body = route.request().postDataJSON();
    draft = { ...draft, document: body.document, layout: body.layout, revision: draft.revision + 1, documentDigest: "c".repeat(64) };
    await route.fulfill({ json: draft });
  });
  await page.route("**/api/v1/workflows/patterns/assessment-router", async (route) => {
    routerRequest = route.request().postDataJSON();
    const document = structuredClone(routerRequest.document);
    document.spec.nodes.assessment_router = makeNode("Assessment Router");
    await route.fulfill({ json: { document, assessmentId: "assessment_router", gateIds: [] } });
  });
  await page.goto("/workflows?item=draft%3Adraft_stages");
  await page.getByRole("combobox", { name: "Stage preset" }).selectOption("plan");
  await page.getByRole("button", { name: "Add stage", exact: true }).click();
  await expect.poll(() => draft.document.spec.nodes.plan?.prompt?.id).toBe("plan_prompt");
  expect(draft.document.spec.nodes.plan.checkpoint.mode).toBe("approve");
  expect(draft.document.spec.nodes.plan.inputs.deferred_work).toBeUndefined();
  await page.getByRole("button", { name: "Assessment Router", exact: true }).click();
  await expect(page.getByRole("combobox", { name: "Plan exit", exact: true })).toHaveValue("plan");
  await page.getByRole("button", { name: "Add router", exact: true }).click();
  await expect.poll(() => routerRequest?.targets).toEqual({ questions: "questions", research: "research", design: "product_design", technical_design: "technical_design", plan: "plan" });
  await expect.poll(() => draft.document.spec.nodes.assessment_router?.displayName).toBe("Assessment Router");
});
