import { expect, test } from "@playwright/test";

import { installEmptyControlPlane } from "./acceptance.fixtures";

const primaryRoutes = [
  ["/board", "Lifecycle board"],
  ["/checkpoints", "Checkpoints"],
  ["/workflows", "Workflows"],
  ["/settings", "Settings"],
] as const;

const viewports = [
  { width: 320, height: 720 },
  { width: 768, height: 900 },
  { width: 1280, height: 900 },
] as const;

test.beforeEach(async ({ page }) => installEmptyControlPlane(page));

for (const viewport of viewports) {
  for (const [path, heading] of primaryRoutes) {
    test(`${path} has a single accessible page hierarchy at ${viewport.width}px`, async ({ page }) => {
      await page.setViewportSize(viewport);
      await page.goto(path);
      await expect(page.locator("main")).toHaveCount(1);
      await expect(page.getByRole("heading", { level: 1, name: heading })).toHaveCount(1);
      await expect(page.locator("nav[aria-label='Primary'] a")).toHaveText(["Board", "Checkpoints", "Workflows", "Settings"]);
      await expect(page.locator("body")).toHaveCSS("overflow-x", "hidden");
      expect(await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)).toBeLessThanOrEqual(1);
      const headingLevels = await page.locator("main h1, main h2, main h3").evaluateAll((nodes) => nodes.map((node) => Number(node.tagName.slice(1))));
      expect(headingLevels[0]).toBe(1);
      expect(headingLevels.every((level, index) => index === 0 || level <= headingLevels[index - 1] + 1)).toBeTruthy();
    });
  }
}

test("keyboard navigation, drawer focus return, command palette, and route focus work", async ({ page }) => {
  await page.setViewportSize({ width: 320, height: 720 });
  await page.goto("/board");
  await page.getByRole("button", { name: "Open navigation" }).focus();
  await page.keyboard.press("Enter");
  await expect(page.getByRole("button", { name: "Close navigation" }).last()).toBeFocused();
  await page.keyboard.press("Escape");
  await expect(page.getByRole("button", { name: "Open navigation" })).toBeFocused();
  await page.keyboard.press("Control+k");
  await expect(page.getByRole("dialog", { name: "Search and navigate" })).toBeVisible();
  await expect(page.getByLabel("Search dashboard")).toBeFocused();
  await page.getByLabel("Search dashboard").fill("workflow");
  await page.keyboard.press("ArrowDown");
  await expect(page.getByRole("dialog", { name: "Search and navigate" }).getByRole("link", { name: /Workflows/ })).toBeFocused();
  await page.keyboard.press("Enter");
  await expect(page).toHaveURL(/\/workflows$/);
  await expect(page.locator("main")).toBeFocused();
});

test("critical shell interaction remains usable at 200 percent zoom", async ({ page }) => {
  await page.setViewportSize({ width: 640, height: 720 });
  await page.goto("/checkpoints");
  await page.evaluate(() => { document.documentElement.style.zoom = "2"; });
  await expect(page.getByRole("heading", { level: 1, name: "Checkpoints" })).toBeVisible();
  await page.keyboard.press("Control+k");
  await expect(page.getByRole("dialog", { name: "Search and navigate" })).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)).toBeLessThanOrEqual(1);
});
