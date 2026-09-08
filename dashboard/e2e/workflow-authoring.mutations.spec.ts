import { expect, test, type Page } from "@playwright/test";

import { installEmptyControlPlane } from "./acceptance.fixtures";

const digest = (value: string) => value.repeat(64).slice(0, 64);

test("Canvas and Structure authoring persist through the public draft API and reject incompatible ports", async ({ page }) => {
  await installEmptyControlPlane(page);
  let draft: any;
  const saves: any[] = [];
  let published = false;

  await page.route("**/api/v1/workflows/library", route => route.fulfill({ json: { versions: [], drafts: draft ? [draft] : [], archives: [] } }));
  await page.route("**/api/v1/workflows/drafts/create", async route => {
    const body = route.request().postDataJSON();
    draft = { id: "draft_browser", name: body.name, scope: body.scope, scopeReference: body.scopeReference, revision: 1, document: body.document, layout: body.layout, documentDigest: digest("a"), updatedAt: "2026-09-07T12:00:00Z" };
    await route.fulfill({ json: draft });
  });
  await page.route("**/api/v1/workflows/drafts/update", async route => {
    const body = route.request().postDataJSON(); saves.push(body);
    draft = { ...draft, revision: draft.revision + 1, document: body.document, layout: body.layout, documentDigest: digest(String((draft.revision + 1) % 10)), updatedAt: "2026-09-07T12:01:00Z" };
    await route.fulfill({ json: draft });
  });
  await page.route("**/api/v1/workflows/drafts/validate", route => route.fulfill({ json: { draftId: draft.id, revision: draft.revision, documentDigest: draft.documentDigest, digest: digest("f"), findings: [] } }));
  await page.route("**/api/v1/workflows/drafts/publish", async route => {
    const body = route.request().postDataJSON(); published = true;
    await route.fulfill({ json: { draftId: draft.id, draftRevision: draft.revision, sourceValidationDigest: digest("f"), disposition: "created", published: { name: draft.name, version: body.version, digest: digest("e"), sourceScope: "user", sourceReference: "local-user", installedAt: "2026-09-07T12:02:00Z" } } });
  });

  await page.goto("/workflows");
  await page.getByRole("button", { name: "New" }).click();
  await page.getByRole("dialog", { name: "New workflow draft" }).getByRole("button", { name: "Create draft" }).click();
  await expect(page.getByRole("tab", { name: "Structure" })).toHaveAttribute("aria-selected", "true");

  // Structure: create, configure, connect, and delete using rendered controls.
  await page.getByRole("button", { name: "Command Local command" }).click();
  await expect(page.getByRole("heading", { name: "command" })).toBeVisible();
  await page.getByLabel("Display name").fill("Structured worker");
  await expect.poll(() => saves.length).toBeGreaterThan(0);
  const structure = page.getByRole("tabpanel", { name: "Structure" });
  await structure.getByLabel("From").selectOption("start");
  await structure.getByLabel("To").selectOption("command");
  await structure.getByRole("button", { name: "Add transition" }).click();
  await expect(structure.getByRole("table", { name: "Workflow transitions" })).toContainText("start → command");
  await structure.getByRole("button", { name: "Remove command" }).click();
  await expect(structure).not.toContainText("Structured worker");

  // Canvas: add, edit and use the typed port connection surface.
  await page.getByRole("tab", { name: "Canvas" }).click();
  await page.getByRole("button", { name: "Command Local command" }).click();
  await expect(page.locator(".react-flow")).toBeVisible();
  await page.getByLabel("Display name").fill("Canvas worker");
  await page.getByText("Input bindings", { exact: true }).click();
  await page.getByRole("button", { name: /Add input binding/i }).click();
  await page.getByRole("tab", { name: "Structure" }).click();
  await page.getByRole("button", { name: /Start start Command/i }).click();
  await page.getByText("Output declarations", { exact: true }).click();
  await page.getByRole("button", { name: /Add output declaration/i }).click();
  await page.getByRole("tab", { name: "Canvas" }).click();
  const binding = page.locator("details", { hasText: "Data bindings" });
  await binding.locator("summary").click();
  const selects = binding.locator("select");
  await selects.nth(0).selectOption({ index: 1 });
  await selects.nth(1).selectOption({ index: 1 });
  await expect(binding.getByRole("alert")).toContainText('expects string from object');
  await expect(binding.getByRole("button", { name: "Connect data ports" })).toBeDisabled();

  // Make the target compatible in the inspector, then connect, validate and publish exact evidence.
  await page.getByRole("tab", { name: "Structure" }).click();
  await page.getByRole("button", { name: /Canvas worker command Command/i }).click();
  const inputDetails = page.getByRole("complementary", { name: "Contextual inspector" }).locator("details", { hasText: "Input bindings" });
  await inputDetails.evaluate((element: HTMLDetailsElement) => { element.open = true; });
  await inputDetails.locator("select").first().selectOption("object");
  await page.getByRole("tab", { name: "Canvas" }).click();
  await binding.getByRole("button", { name: "Connect data ports" }).click();
  await expect.poll(() => saves.some(value => JSON.stringify(value.document).includes("node.start.output"))).toBeTruthy();
  await expect(page.getByRole("button", { name: "Validate draft" })).toBeEnabled();
  await page.getByRole("button", { name: "Validate draft" }).click();
  await expect(page.locator(".validation-slot--valid")).toContainText("Validation passed");
  await page.getByRole("button", { name: "Publish" }).click();
  await page.getByRole("dialog", { name: "Publish immutable workflow" }).getByRole("button", { name: "Publish exact revision" }).click();
  await expect.poll(() => published).toBeTruthy();
  await expect(page.locator(".workflow-publish-result")).toContainText("Published");
});

test("React Flow uses the available editor height and keeps node content inside measured bounds", async ({ page }, testInfo) => {
  await installEmptyControlPlane(page);
  const document = { apiVersion: "darkstar.local/v1alpha2", kind: "Workflow", metadata: { name: "software-delivery-copy", version: "0.1.0", displayName: "Software delivery copy" }, spec: { inputs: Object.fromEntries(Array.from({ length: 8 }, (_, index) => [`input_${index + 1}_with_a_long_name`, { type: "string" }])), routeDefaults: { entry: "assess", terminals: ["publish"] }, nodes: { assess: { type: "command", displayName: "Assess the software delivery request and constraints", entry: true, command: { argv: ["assess"] }, inputs: Object.fromEntries(Array.from({ length: 8 }, (_, index) => [`input_${index + 1}_with_a_long_name`, { from: `run.input.input_${index + 1}_with_a_long_name`, type: "string", required: true }])), outputs: { "assessment_result_with_a_long_name": { type: "object", required: true } }, transitions: [{ id: "to_publish", to: "publish" }] }, publish: { type: "command", displayName: "Publish verified software delivery artifacts", terminal: true, command: { argv: ["publish"] }, inputs: { "assessment_result_with_a_long_name": { from: "node.assess.output.assessment_result_with_a_long_name", type: "object", required: true } }, outputs: {}, transitions: [] } } } };
  const draft = { id: "draft_layout", name: "software-delivery-copy", scope: "user", scopeReference: "local-user", revision: 1, document, layout: { version: 1, nodes: { assess: { x: 40, y: 40 }, publish: { x: 900, y: 40 } } }, documentDigest: digest("9"), updatedAt: "2026-09-07T12:00:00Z" };
  await page.route("**/api/v1/workflows/library", route => route.fulfill({ json: { versions: [], drafts: [draft], archives: [] } }));
  await page.setViewportSize({ width: 1690, height: 1275 });
  await page.goto("/workflows?item=draft:draft_layout&view=canvas");
  const canvas = page.locator(".react-flow");
  await expect(canvas).toBeVisible();
  await expect.poll(() => canvas.evaluate(element => element.getBoundingClientRect().height)).toBeGreaterThan(800);
  const nodes = page.locator(".workflow-flow-node");
  await expect(nodes).toHaveCount(3);
  await expect(page.locator(".canvas-edge--execution")).toHaveCount(1);
  await expect(page.locator(".canvas-edge--data")).toHaveCount(0);
  const dataToggle = page.getByRole("button", { name: /Show data bindings/ });
  await expect(dataToggle).toHaveAttribute("aria-pressed", "false");
  await dataToggle.click();
  await expect(page.locator(".canvas-edge--data")).toHaveCount(9);
  await page.getByRole("button", { name: "Hide data bindings" }).click();
  await expect(page.locator(".canvas-edge--data")).toHaveCount(0);
  for (const node of await nodes.all()) {
    const dimensions = await node.evaluate(element => ({ clientWidth: element.clientWidth, scrollWidth: element.scrollWidth, clientHeight: element.clientHeight, scrollHeight: element.scrollHeight }));
    expect(dimensions.scrollWidth).toBeLessThanOrEqual(dimensions.clientWidth + 8); // React Flow handles intentionally protrude by half their width.
    expect(dimensions.scrollHeight).toBeLessThanOrEqual(dimensions.clientHeight + 8);
  }
  await expect(page.locator(".workflow-flow-node--command")).toHaveCount(2);
  const cardBodyStyle = await page.locator(".workflow-flow-node__body").first().evaluate(element => ({ padding: getComputedStyle(element).padding, whiteSpace: getComputedStyle(element.querySelector("strong")!).whiteSpace }));
  expect(cardBodyStyle).toEqual({ padding: "12px 20px", whiteSpace: "nowrap" });
  const longestPort = page.getByRole("button", { name: /assessment_result_with_a_long_name/ }).first();
  await expect(longestPort).toContainText("assessment_result_with_a_long_name");
  await expect(longestPort).toHaveAttribute("title", /assessment_result_with_a_long_name/);
  const flowLeft = await canvas.evaluate(element => element.getBoundingClientRect().left);
  const runInputsLeft = await page.locator(".workflow-flow-run-inputs").evaluate(element => element.getBoundingClientRect().left);
  expect(runInputsLeft).toBeGreaterThanOrEqual(flowLeft + 60);
  await page.getByRole("button", { name: "Auto-layout" }).click();
  await expect.poll(() => page.locator(".react-flow__viewport").evaluate(element => getComputedStyle(element).transform)).toContain("0.85");
  await page.screenshot({ path: testInfo.outputPath("software-delivery-copy-desktop.png"), fullPage: true });

  await page.setViewportSize({ width: 900, height: 900 });
  await expect(canvas).toBeVisible();
  await expect.poll(() => canvas.evaluate(element => element.getBoundingClientRect().height)).toBeGreaterThan(500);
  await expect(page.getByRole("tab", { name: "Structure" })).toBeVisible();
  await page.screenshot({ path: testInfo.outputPath("software-delivery-copy-narrow.png"), fullPage: true });
});
