import { defineConfig } from "@playwright/test";

export default defineConfig({
  testDir: "./dashboard/e2e",
  outputDir: "./test-results/browser",
  fullyParallel: false,
  forbidOnly: Boolean(process.env.CI),
  retries: process.env.CI ? 1 : 0,
  workers: process.env.CI ? 1 : 2,
  reporter: process.env.CI ? [["line"], ["html", { open: "never" }]] : "line",
  use: {
    baseURL: "http://127.0.0.1:4173",
    browserName: "chromium",
    colorScheme: "dark",
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
  },
  webServer: {
    command: "npx vite dashboard --host 127.0.0.1 --port 4173",
    url: "http://127.0.0.1:4173/board",
    reuseExistingServer: !process.env.CI,
    timeout: 120_000,
  },
});
