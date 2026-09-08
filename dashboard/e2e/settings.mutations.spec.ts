import { expect, test } from "@playwright/test";

import { installEmptyControlPlane } from "./acceptance.fixtures";

test("Settings previews, applies, preserves conflicts, resets inheritance, and redacts secrets", async ({ page }) => {
  await installEmptyControlPlane(page);
  const descriptor = { key: "providers.timeout", title: "Provider timeout", description: "Maximum provider wait", type: "integer", default: { type: "integer", value: 30 }, constraints: { required: true, minimum: 1, maximum: 300 }, sensitivity: "public", allowedScopes: ["user"], restart: "daemon", actions: ["preview", "apply", "restore"] };
  const state = (revision: string, configured = true, value = 45) => ({ schemaVersion: 1, scope: { type: "user" }, revision, secretRevision: "secret-r1", configured: configured ? [{ key: descriptor.key, value: { type: "integer", value } }] : [], effective: [{ key: descriptor.key, value: { type: "integer", value: configured ? value : 30 }, source: { scope: configured ? "user" : "default", reference: configured ? "user.toml" : "shipped" } }] });
  let authoritative: any = state("r1");
  let conflictNext = false;
  let secretBody: any;
  const mutations: any[] = [];
  await page.route("**/api/v1/configuration/catalog", route => route.fulfill({ json: { schemaVersion: 1, settings: [descriptor] } }));
  await page.route("**/api/v1/configuration/state**", route => route.fulfill({ json: authoritative }));
  await page.route("**/api/v1/configuration/preview", async route => {
    const body = route.request().postDataJSON(); mutations.push(body);
    const unset = body.change.operation === "unset";
    const next = unset ? state(body.expectedRevision, false) : state(body.expectedRevision, true, body.change.value.value);
    await route.fulfill({ json: { schemaVersion: 1, valid: true, before: authoritative, after: next, issues: [], restart: "daemon" } });
  });
  await page.route("**/api/v1/configuration/apply", async route => {
    const body = route.request().postDataJSON(); mutations.push(body);
    if (conflictNext) { conflictNext = false; authoritative = state("r-conflict", true, 77); await route.fulfill({ status: 409, json: { schemaVersion: 1, code: "revision_conflict", message: "changed", requestId: "req", retryable: false } }); return; }
    authoritative = body.change.operation === "unset" ? state("r-reset", false) : state("r2", true, body.change.value.value);
    await route.fulfill({ json: { schemaVersion: 1, state: authoritative, restart: "daemon", replayed: false } });
  });
  await page.route("**/api/v1/configuration/secrets", async route => { secretBody = route.request().postDataJSON(); authoritative = { ...authoritative, secretRevision: "secret-r2" }; await route.fulfill({ json: { schemaVersion: 1, name: secretBody.name, revision: "secret-r2", restart: "none", replayed: false } }); });

  await page.goto("/settings?tab=providers");
  const card = page.getByRole("article").filter({ hasText: "Provider timeout" });
  await card.getByRole("button", { name: "Edit" }).click();
  await card.getByRole("spinbutton").fill("60");
  await card.getByRole("button", { name: "Validate change" }).click();
  await expect(card.getByRole("heading", { name: "Effective change" })).toBeVisible();
  await expect(card).toContainText("Daemon restart required");
  await card.getByRole("button", { name: "Save validated change" }).click();
  await expect(page.getByText("Configuration applied", { exact: true })).toBeVisible();
  await expect(page.getByText(/Restart the daemon/)).toBeVisible();

  await card.getByRole("button", { name: "Edit" }).click();
  await card.getByRole("spinbutton").fill("90");
  await card.getByRole("button", { name: "Validate change" }).click();
  conflictNext = true;
  await card.getByRole("button", { name: "Save validated change" }).click();
  await expect(card.getByText("Authoritative state changed", { exact: true })).toBeVisible();
  await expect(card.getByRole("spinbutton")).toHaveValue("90");
  page.once("dialog", dialog => dialog.accept());
  await card.getByRole("button", { name: "Discard" }).click();

  page.once("dialog", dialog => dialog.accept());
  await card.getByRole("button", { name: "Reset to inherited" }).click();
  await expect(page.getByText(/now uses its inherited or shipped default value/)).toBeVisible();
  await expect(card).toContainText("Inherited");
  expect(mutations.some(body => body.change.operation === "unset")).toBeTruthy();

  const secret = "super-secret-browser-value";
  await page.getByRole("button", { name: "Write secret" }).click();
  await page.getByLabel("Secret name").fill("provider-key");
  await page.getByLabel("New secret value").fill(secret);
  await page.locator("#secret-write-form").getByRole("button", { name: "Write secret" }).click();
  await expect(page.getByText("Secret receipt received", { exact: true })).toBeVisible();
  expect(secretBody).toEqual({ name: "provider-key", value: secret, expectedRevision: "secret-r1" });
  await expect(page.locator("body")).not.toContainText(secret);
  await expect(page.getByLabel("New secret value")).toHaveCount(0);
});
