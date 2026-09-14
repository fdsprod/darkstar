import { expect, test, type Page } from "@playwright/test";

import { installEmptyControlPlane } from "./acceptance.fixtures";

const now = "2026-09-14T04:30:00Z";

async function repositoryControlPlane(page: Page) {
  await installEmptyControlPlane(page);
  const state: { view?: any; writes: any[]; conflictNext: boolean; unavailable: boolean } = {
    writes: [], conflictNext: false, unavailable: false,
  };
  await page.route(/\/api\/v1\/projects-v2(?:\/.*)?$/, async (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    if (state.unavailable) {
      await route.fulfill({ status: 503, json: { code: "WORK_SERVICE_UNAVAILABLE", message: "Repository service is unavailable.", retryable: true } });
      return;
    }
    if (request.method() === "GET") {
      await route.fulfill({ json: path === "/api/v1/projects-v2" ? (state.view ? [state.view] : []) : state.view });
      return;
    }
    const body = request.postDataJSON();
    expect(request.headers()["idempotency-key"]).toBeTruthy();
    state.writes.push({ path, method: request.method(), body, key: request.headers()["idempotency-key"] });
    if (path === "/api/v1/projects-v2") {
      state.view = {
        schemaVersion: 2,
        project: { id: "project_01K00000000000000000000001", name: body.name, sourceHash: "a".repeat(64), status: "active", resourceVersion: 1, lastGlobalPosition: 1, createdAt: now, updatedAt: now },
        repositories: [], defaults: body.defaults ?? {}, migration: { state: "ready" },
      };
      await route.fulfill({ status: 201, json: state.view });
      return;
    }
    expect(request.headers()["if-match"]).toBe(`"${state.view.project.resourceVersion}"`);
    if (state.conflictNext) {
      state.conflictNext = false;
      state.view.project.resourceVersion += 1;
      await route.fulfill({ status: 409, json: { code: "REPOSITORY_REVISION_CONFLICT", message: "Repository settings changed in another session.", retryable: false } });
      return;
    }
    if (path.endsWith("/defaults")) {
      state.view.defaults = body.defaults;
    } else if (request.method() === "POST") {
      expect(body.expectedMembershipRevision).toBe(0);
      const id = `repository_${state.view.repositories.length + 1}`;
      state.view.repositories.push({
        repository: { id, root: body.repositoryPath, commonGitDir: `${body.repositoryPath}/.git`, identityKey: body.repositoryPath, createdAt: now },
        membership: { projectId: "project_01K00000000000000000000001", repositoryId: id, label: body.label, role: body.role, settings: body.settings, revision: 1, status: "active", updatedAt: now },
      });
    } else {
      const entry = state.view.repositories.find((value: any) => path.endsWith(`/${value.repository.id}`));
      expect(body.expectedMembershipRevision).toBe(entry.membership.revision);
      entry.membership.revision += 1;
      if (request.method() === "DELETE") {
        entry.membership.status = "removed";
        entry.membership.removal = { actor: { type: "user", id: "browser-test" }, removedAt: now };
      } else {
        Object.assign(entry.membership, { label: body.label, role: body.role, settings: body.settings, status: "active" });
        delete entry.membership.removal;
      }
    }
    state.view.project.resourceVersion += 1;
    await route.fulfill({ json: state.view });
  });
  return state;
}

async function createProject(page: Page) {
  await page.goto("/settings?tab=projects");
  await page.getByRole("button", { name: "Create a project without code" }).click();
  const dialog = page.getByRole("dialog", { name: "Create project", exact: true });
  await expect(dialog.getByLabel("Project name")).toBeFocused();
  await dialog.getByLabel("Project name").fill("Product without code");
  await dialog.getByLabel("Project name").press("Enter");
  await expect(dialog).toBeHidden();
  await expect(page.getByRole("heading", { name: "No code repositories yet" })).toBeVisible();
}

async function attachRepository(page: Page, label: string, role: "read_only" | "implementation") {
  await page.getByRole("button", { name: "Attach repository", exact: true }).click();
  const dialog = page.getByRole("dialog", { name: "Attach repository" });
  await expect(dialog.getByLabel("Local repository path")).toBeFocused();
  await dialog.getByLabel("Local repository path").fill(`C:\\Projects\\${label}`);
  await dialog.getByLabel("Local repository path").press("Tab");
  await expect(dialog.getByLabel("Repository label")).toBeFocused();
  await dialog.getByLabel("Repository label").fill(label);
  await dialog.getByLabel("Repository role").selectOption(role);
  await dialog.getByRole("button", { name: "Attach repository", exact: true }).click();
  await expect(dialog).toBeHidden();
  await expect(page.getByRole("heading", { name: label, exact: true })).toBeVisible();
}

test("keyboard creation, zero/one/multiple repositories, defaults, and retained detachment use revisioned commands", async ({ page }) => {
  const state = await repositoryControlPlane(page);
  await createProject(page);
  await attachRepository(page, "Frontend", "implementation");
  await attachRepository(page, "Services", "read_only");
  await expect(page.getByText("2 active repositories", { exact: true })).toBeVisible();
  expect(state.view.repositories.map((entry: any) => entry.membership.role)).toEqual(["implementation", "read_only"]);

  await page.getByRole("button", { name: "Repository defaults", exact: true }).click();
  const defaults = page.getByRole("dialog", { name: "Repository defaults" });
  await defaults.getByLabel("Base ref", { exact: true }).fill("release/stable");
  await page.screenshot({ path: "out/dar155-repository-defaults.png" });
  await page.setViewportSize({ width: 390, height: 844 });
  await expect(defaults).toBeVisible();
  await defaults.evaluate((element) => {
    element.scrollTop = 0;
  });
  await page.screenshot({ path: "out/dar155-repository-defaults-mobile.png" });
  await defaults.getByRole("button", { name: "Save changes" }).scrollIntoViewIfNeeded();
  await expect(defaults.getByRole("button", { name: "Save changes" })).toBeInViewport();
  await page.setViewportSize({ width: 1280, height: 720 });
  await defaults.getByRole("combobox", { name: "Path scope", exact: true }).selectOption("custom");
  await defaults.getByLabel("Allowed paths, one per line").fill("src\ndocs");
  await defaults.getByRole("button", { name: "Save changes" }).click();
  await expect(defaults).toBeHidden();
  expect(state.view.defaults.baseRef).toBe("release/stable");
  expect(state.view.defaults.pathScope).toEqual(["src", "docs"]);

  await page.getByRole("button", { name: "Detach Frontend", exact: true }).click();
  const detach = page.getByRole("dialog", { name: "Detach repository" });
  await expect(detach.getByRole("button", { name: "Cancel", exact: true })).toBeFocused();
  await expect(detach).toContainText("Existing runs, repository files, and history are preserved");
  await detach.getByRole("button", { name: "Detach repository", exact: true }).click();
  await expect(detach).toBeHidden();
  await expect(page.getByText("1 active repository", { exact: true })).toBeVisible();
  await page.getByText("Detached repositories (1)", { exact: true }).click();
  await expect(page.getByRole("button", { name: "Reattach Frontend" })).toBeVisible();
  expect(state.view.repositories[0].membership.status).toBe("removed");
  await page.evaluate(() => {
    window.scrollTo(0, 0);
  });
  await page.screenshot({ path: "out/dar155-repository-management.png" });
  await page.getByRole("button", { name: "Reattach Frontend" }).click();
  const reattach = page.getByRole("dialog", { name: "Reattach repository" });
  await reattach.getByRole("button", { name: "Reattach repository", exact: true }).click();
  await expect(reattach).toBeHidden();
  await expect(page.getByText("2 active repositories", { exact: true })).toBeVisible();
  expect(state.view.repositories).toHaveLength(2);
  expect(state.view.repositories[0].membership.revision).toBe(3);
});

test("invalid and stale changes retain the draft and require an explicit reload", async ({ page }) => {
  const state = await repositoryControlPlane(page);
  await createProject(page);
  await attachRepository(page, "Frontend", "read_only");
  await page.getByRole("button", { name: "Edit Frontend", exact: true }).click();
  const dialog = page.getByRole("dialog", { name: "Edit repository" });
  await dialog.getByLabel("Configuration root", { exact: true }).fill("relative/path");
  const beforeInvalid = state.writes.length;
  await dialog.getByRole("button", { name: "Save changes" }).click();
  await expect(dialog.getByText("Changes were not saved", { exact: true })).toBeVisible();
  expect(state.writes).toHaveLength(beforeInvalid);
  await expect(dialog.getByLabel("Configuration root", { exact: true })).toHaveValue("relative/path");
  await dialog.getByLabel("Configuration root", { exact: true }).fill("");
  await dialog.getByLabel("Repository label").fill("Renamed frontend");
  state.conflictNext = true;
  await dialog.getByRole("button", { name: "Save changes" }).click();
  await expect(dialog.getByText("Project changed while you were editing", { exact: true })).toBeVisible();
  await expect(dialog.getByLabel("Repository label")).toHaveValue("Renamed frontend");
  await expect(dialog.getByRole("button", { name: "Save changes" })).toBeDisabled();
  await dialog.getByRole("button", { name: "Reload and discard this draft" }).click();
  await expect(dialog.getByLabel("Repository label")).toHaveValue("Frontend");
  await expect(dialog.getByRole("button", { name: "Save changes" })).toBeEnabled();
  await dialog.getByLabel("Repository label").fill("Refreshed frontend");
  await dialog.getByRole("button", { name: "Save changes" }).click();
  await expect(dialog).toBeHidden();
  await expect(page.getByRole("heading", { name: "Refreshed frontend", exact: true })).toBeVisible();
  expect(state.view.repositories[0].membership.label).toBe("Refreshed frontend");
});

test("unavailable repository state offers retry without falling back to legacy data", async ({ page }) => {
  const state = await repositoryControlPlane(page);
  state.unavailable = true;
  await page.goto("/settings?tab=projects");
  await expect(page.getByText("Projects unavailable", { exact: true })).toBeVisible();
  state.unavailable = false;
  await page.getByRole("button", { name: "Retry projects", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Create your first project", exact: true })).toBeVisible();
  expect(state.writes).toHaveLength(0);
});

test("loading project membership has an explicit accessible state", async ({ page }) => {
  await installEmptyControlPlane(page);
  let release: () => void = () => {};
  const pending = new Promise<void>((resolve) => {
    release = resolve;
  });
  await page.route("**/api/v1/projects-v2", async (route) => {
    await pending;
    await route.fulfill({ json: [] });
  });
  await page.goto("/settings?tab=projects");
  await expect(page.getByText("Loading projects", { exact: true })).toBeVisible();
  release();
  await expect(page.getByRole("heading", { name: "Create your first project", exact: true })).toBeVisible();
});
