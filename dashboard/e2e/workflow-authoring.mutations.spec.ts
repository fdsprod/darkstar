import { expect, test, type Page } from "@playwright/test";
import { readFileSync } from "node:fs";

import { installEmptyControlPlane } from "./acceptance.fixtures";
import { deriveEditorGraph } from "../src/pages/workflowEditorModel";
import { derivePortGraph, portLabel } from "../src/pages/workflowPortModel";

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
  const document = JSON.parse(readFileSync(new URL("../../examples/workflows/software-delivery.json", import.meta.url), "utf8"));
  document.metadata.name = "software-delivery-copy"; document.metadata.displayName = "Software delivery copy";
  let draft = { id: "draft_layout", name: "software-delivery-copy", scope: "user", scopeReference: "local-user", revision: 1, document, layout: { version: 1, nodes: {} }, documentDigest: digest("9"), updatedAt: "2026-09-07T12:00:00Z" };
  const saves: any[] = [];
  await page.route("**/api/v1/workflows/library", route => route.fulfill({ json: { versions: [], drafts: [draft], archives: [] } }));
  await page.route("**/api/v1/workflows/drafts/update", async route => {
    const body = route.request().postDataJSON(); saves.push(body);
    draft = { ...draft, revision: draft.revision + 1, document: body.document, layout: body.layout, updatedAt: "2026-09-07T12:01:00Z" };
    await route.fulfill({ json: draft });
  });
  await page.setViewportSize({ width: 1690, height: 1275 });
  await page.goto("/workflows?item=draft:draft_layout&view=canvas");
  const canvas = page.locator(".react-flow");
  await expect(canvas).toBeVisible();
  await expect.poll(() => canvas.evaluate(element => element.getBoundingClientRect().height)).toBeGreaterThan(800);
  const graph = deriveEditorGraph(document, {}), projection = derivePortGraph(document, graph);
  const workflowNodes = page.locator(".react-flow__node-workflow"), nodes = page.locator(".workflow-flow-node");
  await expect(workflowNodes).toHaveCount(graph.nodes.length);
  await expect(page.locator(".workflow-flow-run-inputs")).toHaveCount(1);
  await expect(page.locator(".canvas-edge--execution")).toHaveCount(24);
  await expect(page.locator(".canvas-edge--data")).toHaveCount(43);
  await expect(page.locator(".react-flow__edge")).toHaveCount(67);
  await expect(page.locator(".workflow-flow-handle--execution.react-flow__handle-top, .workflow-flow-handle--execution.react-flow__handle-bottom")).toHaveCount(0);
  for (const endpoint of [
    { node: "p0_intake", handle: ".workflow-flow-handle--execution.react-flow__handle-right", port: /output p0_intake\.complete/ },
    { node: "p1_route_assessment", handle: ".workflow-flow-handle--execution.react-flow__handle-left", port: /input p1_route_assessment\.execute/ },
  ]) {
    const card = page.locator(`.react-flow__node[data-id="${endpoint.node}"]`), handle = card.locator(endpoint.handle), port = card.getByRole("button", { name: endpoint.port });
    const [handleBox, portBox] = await Promise.all([handle.boundingBox(), port.boundingBox()]);
    expect(handleBox && portBox && Math.abs(handleBox.y + handleBox.height / 2 - (portBox.y + portBox.height / 2)) <= 1, `${endpoint.node} execution handle must align with its displayed port row`).toBeTruthy();
  }
  const expectedLabels = projection.edges.map(edge => `${edge.kind}: ${portLabel(edge.source)} to ${portLabel(edge.target)}`).toSorted();
  await expect.poll(() => page.locator(".react-flow__edge").evaluateAll(elements => elements.map(element => element.getAttribute("aria-label")).toSorted())).toEqual(expectedLabels);
  const extent = await workflowNodes.evaluateAll(elements => { const boxes = elements.map(element => element.getBoundingClientRect()); const left = Math.min(...boxes.map(box => box.left)), right = Math.max(...boxes.map(box => box.right)), top = Math.min(...boxes.map(box => box.top)), bottom = Math.max(...boxes.map(box => box.bottom)); return { width: right - left, height: bottom - top, ranks: new Set(boxes.map(box => Math.round(box.top))).size }; });
  expect(extent.height).toBeGreaterThan(extent.width * 4);
  expect(extent.width).toBeLessThan(1600);
  expect(extent.ranks).toBeGreaterThan(10);
  const canvasBox = await canvas.boundingBox();
  for (const id of ["p0_intake", "p1_route_assessment"]) {
    const box = await page.locator(`.react-flow__node[data-id="${id}"]`).boundingBox();
    expect(box && canvasBox && box.y >= canvasBox.y && box.y + box.height <= canvasBox.y + canvasBox.height && box.x >= canvasBox.x && box.x + box.width <= canvasBox.x + canvasBox.width, `${id}: ${JSON.stringify({ box, canvasBox })}`).toBeTruthy();
  }
  await expect(page.locator(".canvas-edge--execution").first()).toBeVisible();
  await expect(page.locator(".editor-save-state")).toHaveText("Saved");
  await page.getByRole("button", { name: "Zoom in" }).click(); await page.getByRole("button", { name: "Zoom out" }).click();
  await page.getByRole("group", { name: "Visible connections" }).getByRole("button", { name: /Execution/ }).click();
  await expect(page.locator(".canvas-edge--execution")).toHaveCount(24); await expect(page.locator(".canvas-edge--data")).toHaveCount(0); await expect(page.locator(".workflow-flow-run-inputs")).toHaveCount(0);
  await page.getByRole("group", { name: "Visible connections" }).getByRole("button", { name: /Data/ }).click();
  await expect(page.locator(".canvas-edge--execution")).toHaveCount(0); await expect(page.locator(".canvas-edge--data")).toHaveCount(43); await expect(page.locator(".workflow-flow-run-inputs")).toHaveCount(1);
  await page.getByRole("group", { name: "Visible connections" }).getByRole("button", { name: /All/ }).click();
  await expect(page.locator(".react-flow__edge")).toHaveCount(67);
  await page.getByRole("button", { name: "Zoom to fit" }).click();
  await expect.poll(async () => {
    const outer = await canvas.boundingBox(); const boxes = await nodes.evaluateAll(elements => elements.map(element => { const box = element.getBoundingClientRect(); return { left: box.left, right: box.right, top: box.top, bottom: box.bottom }; }));
    return Boolean(outer && boxes.every(box => box.left >= outer.x - 2 && box.right <= outer.x + outer.width + 2 && box.top >= outer.y - 2 && box.bottom <= outer.y + outer.height + 2));
  }).toBeTruthy();
  await page.waitForTimeout(650);
  expect(saves).toHaveLength(0); expect(draft.revision).toBe(1); expect(draft.document).toEqual(document);
  await expect(page.locator(".editor-save-state")).toHaveText("Saved");
  for (const node of await nodes.all()) {
    const dimensions = await node.evaluate(element => ({ clientWidth: element.clientWidth, scrollWidth: element.scrollWidth, clientHeight: element.clientHeight, scrollHeight: element.scrollHeight }));
    expect(dimensions.scrollWidth).toBeLessThanOrEqual(dimensions.clientWidth + 8); // React Flow handles intentionally protrude by half their width.
    expect(dimensions.scrollHeight).toBeLessThanOrEqual(dimensions.clientHeight + 8);
  }
  await expect(page.locator(".workflow-flow-node--command")).toHaveCount(2);
  const cardBodyStyle = await page.locator(".workflow-flow-node__body").first().evaluate(element => ({ padding: getComputedStyle(element).padding, whiteSpace: getComputedStyle(element.querySelector("strong")!).whiteSpace }));
  expect(cardBodyStyle).toEqual({ padding: "12px 20px", whiteSpace: "nowrap" });
  const longestPort = page.getByRole("button", { name: /route_readiness_threshold/ }).first();
  await expect(longestPort).toContainText("route_readiness_threshold");
  await expect(longestPort).toHaveAttribute("title", /route_readiness_threshold/);
  const flowLeft = await canvas.evaluate(element => element.getBoundingClientRect().left);
  const runInputsLeft = await page.locator(".workflow-flow-run-inputs").evaluate(element => element.getBoundingClientRect().left);
  expect(runInputsLeft).toBeGreaterThanOrEqual(flowLeft + 60);
  await page.screenshot({ path: testInfo.outputPath("software-delivery-copy-desktop-overview.png"), fullPage: true });
  await page.getByRole("button", { name: "Auto-layout" }).click();
  await expect.poll(() => page.locator(".react-flow__viewport").evaluate(element => getComputedStyle(element).transform)).toContain("0.85");
  await expect.poll(() => saves.length).toBeGreaterThan(0);
  await page.screenshot({ path: testInfo.outputPath("software-delivery-copy-desktop-normal.png"), fullPage: true });

  await page.setViewportSize({ width: 900, height: 900 });
  await expect(canvas).toBeVisible();
  await expect.poll(() => canvas.evaluate(element => element.getBoundingClientRect().height)).toBeGreaterThan(500);
  await expect(page.getByRole("tab", { name: "Structure" })).toBeVisible();
  await page.screenshot({ path: testInfo.outputPath("software-delivery-copy-narrow.png"), fullPage: true });
});
