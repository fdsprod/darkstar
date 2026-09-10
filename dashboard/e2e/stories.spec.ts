import { expect, test, type Page } from "@playwright/test";

interface StoryEntry { id: string; title: string; name: string; type: string }

/** Every published story, read from the running Storybook index. */
async function catalog(page: Page): Promise<StoryEntry[]> {
  const response = await page.request.get("/index.json");
  expect(response.ok()).toBeTruthy();
  const index = (await response.json()) as { entries: Record<string, StoryEntry> };
  return Object.values(index.entries).filter((entry) => entry.type === "story");
}

test("the catalog publishes every pure component group", async ({ page }) => {
  const entries = await catalog(page);
  const titles = new Set(entries.map((entry) => entry.title));
  for (const group of ["UI/Button", "UI/Field", "UI/ModalDialog", "UI/Link", "UI/DetailSection", "Patterns/AsyncPanel", "Patterns/EmptyState", "Patterns/Chrome", "Document/Annotations", "Document/ReviewLayout", "Document/Markdown"]) {
    expect(titles, `missing story group ${group}`).toContain(group);
  }
  expect(entries.length).toBeGreaterThanOrEqual(50);
});

test("every story renders in isolation without a daemon, router, or console error", async ({ page }) => {
  const entries = await catalog(page);
  const broken: string[] = [];

  for (const entry of entries) {
    const problems: string[] = [];
    const onConsole = (message: { type(): string; text(): string }) => {
      if (message.type() === "error") problems.push(message.text());
    };
    const onError = (error: Error) => problems.push(error.message);
    page.on("console", onConsole);
    page.on("pageerror", onError);

    await page.goto(`/iframe.html?id=${encodeURIComponent(entry.id)}&viewMode=story`);
    // A story that threw leaves Storybook's error frame behind instead of markup.
    await expect(page.locator("#storybook-root")).toBeAttached();
    await page.waitForFunction(() => {
      const root = document.querySelector("#storybook-root");
      return Boolean(root && root.childElementCount > 0);
    }, undefined, { timeout: 5_000 }).catch(() => problems.push("rendered nothing"));
    // Storybook always ships an empty #error-message node; only text means a throw.
    const errorFrame = (await page.locator("#error-message").allInnerTexts()).join("").trim();
    if (errorFrame) problems.push(errorFrame);

    page.off("console", onConsole);
    page.off("pageerror", onError);
    if (problems.length) broken.push(`${entry.title} / ${entry.name}: ${problems.join(" | ")}`);
  }

  expect(broken, `stories failed to render:\n${broken.join("\n")}`).toEqual([]);
});
