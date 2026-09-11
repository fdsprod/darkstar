import { expect, test } from "@playwright/test";
import { installEmptyControlPlane } from "./acceptance.fixtures";

test("library drafts publish immutable versions, compare history, and restore archives", async ({ page }, testInfo) => {
  await installEmptyControlPlane(page);
  await page.setViewportSize({ width: 1500, height: 1000 });
  let item: any;
  const saved: any[] = [];
  await page.route(/\/api\/v1\/content-library(?:\/.*)?$/, async (route) => {
    const path = new URL(route.request().url()).pathname;
    if (route.request().method() === "GET") {
      await route.fulfill({ json: { items: item ? [item] : [] } });
      return;
    }
    const body = route.request().postDataJSON();
    if (path.endsWith("/draft")) {
      saved.push(body);
      item = { ...item, name: body.name, description: body.description, draft: { revision: item.draft.revision + 1, document: body.document } };
    } else if (path.endsWith("/publish")) {
      item = { ...item, versions: [...item.versions, { reference: { id: item.id, version: body.version, digest: "a".repeat(64) }, document: structuredClone(item.draft.document), createdAt: "2026-09-11T00:00:00Z" }] };
    } else if (path.endsWith("/archive")) {
      item = { ...item, archivedAt: "2026-09-11T00:00:00Z" };
    } else if (path.endsWith("/restore")) {
      delete item.archivedAt;
      item = { ...item };
    } else {
      item = { id: "content_design", name: body.name, description: body.description, kind: body.document.kind, draft: { revision: 1, document: body.document }, versions: [] };
    }
    await route.fulfill({ json: item });
  });
  await page.route("**/api/v1/workflows/content-usage?**", (route) => route.fulfill({ json: { usages: [] } }));
  await page.goto("/templates");
  await page.getByLabel("New template name").fill("Product Design");
  await page.getByRole("button", { name: "Create draft" }).click();
  await page.getByRole("textbox", { name: "Markdown template", exact: true }).fill("# Product Design\n\n## Overview\n\nExplain expected behavior.");
  await page.getByLabel("New version", { exact: true }).fill("1.0.0");
  await expect(page.getByRole("button", { name: "Publish saved draft" })).toBeDisabled();
  await page.getByRole("button", { name: "Save draft", exact: true }).click();
  await expect.poll(() => saved.length).toBe(1);
  await page.getByLabel("New version", { exact: true }).fill("1.0.0");
  await page.getByRole("button", { name: "Publish saved draft" }).click();
  await page.getByLabel("Viewing version").selectOption("1.0.0");
  await expect(page.getByRole("textbox", { name: "Markdown template", exact: true })).toBeDisabled();
  await page.getByRole("button", { name: "Use this version in draft" }).click();
  await page.getByRole("textbox", { name: "Markdown template", exact: true }).fill("# Product Design\n\n## Overview\n\nRevised behavior.");
  await page.getByLabel("Compare current view with").selectOption("1.0.0");
  await expect(page.locator(".content-version-comparison")).toContainText("Explain expected behavior.");
  await expect(page.locator(".content-version-comparison")).toContainText("Revised behavior.");
  await page.screenshot({ path: testInfo.outputPath("template-version-editor.png"), fullPage: true });
  await page.getByRole("button", { name: "Save draft", exact: true }).click();
  await page.getByRole("button", { name: "Archive", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Product Design (archived)" })).toBeVisible();
  await page.getByRole("button", { name: "Restore to library" }).click();
  await expect(page.getByRole("heading", { name: "Product Design", exact: true })).toBeVisible();
  expect(item.versions[0].document.content).toContain("Explain expected behavior.");
});

test("prompt builder requests actual connection conditions and renders why sections change", async ({ page }) => {
  await installEmptyControlPlane(page);
  const document = { kind: "prompt", instructions: "Assess the work item.", sections: [{ id: "existing", when: { kind: "input_linked", input: "open_items" }, instructions: "Reconcile linked open items." }] };
  const item = { id: "assessment", name: "Assessment", description: "", kind: "prompt", draft: { revision: 1, document }, versions: [] };
  const previews: any[] = [];
  await page.route("**/api/v1/content-library", (route) => route.fulfill({ json: { items: [item] } }));
  await page.route("**/api/v1/content-library/preview", async (route) => {
    const body = route.request().postDataJSON();
    previews.push(body);
    const linked = body.linkedInputs.includes("open_items");
    await route.fulfill({ json: { instructions: linked ? "Assess the work item.\n\nReconcile linked open items." : "Assess the work item.", sections: [{ id: "existing", included: linked, reason: linked ? "open_items is linked" : "open_items is absent" }], estimatedTokens: 15, tokenEstimateMethod: "characters_divided_by_four" } });
  });
  await page.route("**/api/v1/workflows/content-usage?**", (route) => route.fulfill({ json: { usages: [] } }));
  await page.goto("/templates?id=assessment");
  await expect(page.locator(".prompt-preview")).toContainText("existing: omitted");
  await page.getByLabel("open items linked").check();
  await expect(page.locator(".prompt-preview")).toContainText("existing: included");
  await expect(page.locator(".prompt-preview")).toContainText("Reconcile linked open items.");
  expect(previews.at(-1).linkedInputs).toEqual(["open_items"]);
  await page.getByLabel("Revising an artifact", { exact: true }).check();
  await expect.poll(() => previews.at(-1).revision).toBe(true);
});

test("workflow node links published prompt and output template versions without replacing human review", async ({ page }) => {
  await installEmptyControlPlane(page);
  await page.setViewportSize({ width: 1600, height: 1100 });
  const reference = { id: "product_template", version: "1.0.0", digest: "a".repeat(64) };
  const promptReference = { id: "product_prompt", version: "1.0.0", digest: "b".repeat(64) };
  const template = { kind: "template", content: "# Design\n\n## Behavior", requiredHeadings: ["Behavior"] };
  const prompt = { kind: "prompt", instructions: "Describe product behavior.", sections: [] };
  const items = [
    { id: reference.id, name: "Product template", kind: "template", description: "", draft: { revision: 1, document: template }, versions: [{ reference, document: template, createdAt: "2026-09-11T00:00:00Z" }] },
    { id: promptReference.id, name: "Product prompt", kind: "prompt", description: "", draft: { revision: 1, document: prompt }, versions: [{ reference: promptReference, document: prompt, createdAt: "2026-09-11T00:00:00Z" }] },
  ];
  const document = { apiVersion: "darkstar.local/v1alpha3", kind: "Workflow", metadata: { name: "test/linked-design", version: "1.0.0" }, spec: { inputs: { task: { type: "task", resource: { kind: "task" } }, backlog: { type: "open_items", resource: { kind: "open_items" } } }, routeDefaults: { entry: "design", terminals: ["design"] }, nodes: { design: { type: "reasoning", entry: true, terminal: true, displayName: "Product Design", checkpoint: { mode: "approve" }, inputs: { task: { type: "task", from: "run.input.task" } }, outputs: { design: { type: "markdown", artifact: { filename: "design.md" } } }, reasoning: { agent: "designer", instructions: "Consider the requested product change." }, transitions: [] } } } };
  let draft: any = { id: "draft_linked", name: "test/linked-design", scope: "user", scopeReference: "local-user", revision: 1, document, layout: {}, documentDigest: "c".repeat(64), updatedAt: "2026-09-11T00:00:00Z" };
  await page.route("**/api/v1/content-library", (route) => route.fulfill({ json: { items } }));
  await page.route("**/api/v1/workflows/library", (route) => route.fulfill({ json: { versions: [], drafts: [draft], archives: [] } }));
  await page.route("**/api/v1/workflows/drafts/update", async (route) => {
    const body = route.request().postDataJSON();
    draft = { ...draft, document: body.document, layout: body.layout, revision: draft.revision + 1, documentDigest: "d".repeat(64) };
    await route.fulfill({ json: draft });
  });
  await page.goto("/workflows?item=draft%3Adraft_linked&selection=node%3Adesign");
  await page.getByRole("combobox", { name: "Prompt definition", exact: true }).selectOption(JSON.stringify(promptReference));
  await expect.poll(() => draft.document.spec.nodes.design.prompt?.id).toBe(promptReference.id);
  await expect(page.getByRole("textbox", { name: "Additional node instructions", exact: true })).toBeVisible();
  await page.getByRole("combobox", { name: "Template for design", exact: true }).selectOption(JSON.stringify(reference));
  await expect.poll(() => draft.document.spec.nodes.design.outputs.design.artifact.templateInput).toBeTruthy();
  await page.getByRole("combobox", { name: "Deferred work", exact: true }).selectOption("run.input.backlog");
  await expect.poll(() => draft.document.spec.nodes.design.inputs.deferred_work?.from).toBe("run.input.backlog");
  expect(draft.document.spec.nodes.design.checkpoint.mode).toBe("approve");
  const binding = draft.document.spec.nodes.design.inputs[draft.document.spec.nodes.design.outputs.design.artifact.templateInput];
  expect(draft.document.spec.inputs[binding.from.slice(10)].resource).toEqual({ kind: "template_reference", reference });
});
