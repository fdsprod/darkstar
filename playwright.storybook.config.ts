import { defineConfig } from "@playwright/test";

// Stories run under their own configuration so the dashboard acceptance suite
// does not pay for a Storybook boot on every run.
export default defineConfig({
  testDir: "./dashboard/e2e",
  testMatch: "**/stories.spec.ts",
  outputDir: "./test-results/stories",
  fullyParallel: false,
  forbidOnly: Boolean(process.env.CI),
  retries: 0,
  workers: 1,
  reporter: "line",
  // One test walks the whole catalog serially, so it needs more than the default.
  timeout: 300_000,
  use: {
    baseURL: "http://127.0.0.1:6006",
    browserName: "chromium",
    colorScheme: "dark",
    trace: "retain-on-failure",
  },
  webServer: {
    command: "npm run storybook --workspace @darkstar/dashboard -- --port 6006 --ci",
    url: "http://127.0.0.1:6006/index.json",
    // Always start fresh: a reused dev server can serve a stale module graph
    // after a component is edited, which surfaces as a bogus missing export.
    reuseExistingServer: false,
    timeout: 180_000,
  },
});
