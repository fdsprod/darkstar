import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

import {
  SETTINGS_TABS,
  decodeDoctorReport,
  decodeEffectiveConfiguration,
  normalizeProjectRegistration,
  parseSettingsTab,
  sortProjects,
} from "../src/pages/settingsModel.ts";

const subsystemOrder = ["database", "daemon", "paths", "git", "codex", "github", "configuration", "provider"];

function healthyCheck(subsystem) {
  return { subsystem, status: "healthy", code: `${subsystem.toUpperCase()}_READY`, message: `${subsystem} is ready.` };
}

function doctorReport(overrides = {}) {
  return {
    schemaVersion: 1,
    status: "healthy",
    generatedAt: "2026-09-04T08:00:00Z",
    checks: subsystemOrder.map(healthyCheck),
    ...overrides,
  };
}

function providerDetails(overrides = {}) {
  return {
    name: "codex",
    version: "1.2.3",
    executableIdentity: "sha256:executable",
    platform: "windows/amd64",
    authentication: "authenticated",
    usage: "ready",
    instructionSources: ["project", "user"],
    conflictingExecutables: [],
    availableCapabilities: [{ name: "tools", version: "2" }],
    unavailableCapabilities: [{ name: "images", reason: "Not enabled" }],
    ...overrides,
  };
}

test("doctor reports derive severity and expose checks in canonical subsystem order", () => {
  const checks = subsystemOrder.map(healthyCheck).reverse();
  checks[6] = { subsystem: "daemon", status: "degraded", code: "DAEMON_SLOW", message: "Daemon is slow.", action: "Restart the daemon." };
  checks[4] = { subsystem: "git", status: "unhealthy", code: "GIT_MISSING", message: "Git is missing.", action: "Install Git." };

  const decoded = decodeDoctorReport(doctorReport({ status: "unhealthy", checks }));
  assert.deepEqual(decoded.checks.map(({ subsystem }) => subsystem), subsystemOrder);
  assert.equal(decoded.status, "unhealthy");
  assert.equal(checks[0].subsystem, "provider", "decoding must not reorder the caller's array");
});

test("doctor reports reject contradictory, incomplete, duplicate, and open-ended check states", () => {
  assert.throws(
    () => decodeDoctorReport(doctorReport({ status: "degraded" })),
    /contradicts checks with status healthy/,
  );

  const noAction = doctorReport();
  noAction.status = "degraded";
  noAction.checks[0] = { subsystem: "database", status: "degraded", code: "DATABASE_BUSY", message: "Database is busy." };
  assert.throws(() => decodeDoctorReport(noAction), /missing action/);

  const healthyAction = doctorReport();
  healthyAction.checks[0].action = "This must not be present.";
  assert.throws(() => decodeDoctorReport(healthyAction), /unexpected field action/);

  const duplicate = doctorReport();
  duplicate.checks[1] = healthyCheck("database");
  assert.throws(() => decodeDoctorReport(duplicate), /duplicate database checks/);

  const siblingStatus = doctorReport();
  siblingStatus.checks[0].status = "warning";
  assert.throws(() => decodeDoctorReport(siblingStatus), /health check status is invalid/);

  assert.throws(() => decodeDoctorReport({ ...doctorReport(), diagnostic: "extra" }), /unexpected field diagnostic/);
});

test("provider details retain only safe auth, usage, and disjoint capability states", () => {
  const input = doctorReport();
  input.checks[7] = { ...healthyCheck("provider"), providerDetails: providerDetails() };
  const decoded = decodeDoctorReport(input);
  const details = decoded.checks[7].providerDetails;
  assert.equal(details.authentication, "authenticated");
  assert.equal(details.usage, "ready");
  assert.deepEqual(details.instructionSources, ["project", "user"]);
  assert.deepEqual(details.availableCapabilities, [{ name: "tools", version: "2" }]);
  assert.deepEqual(details.unavailableCapabilities, [{ name: "images", reason: "Not enabled" }]);
  assert.equal("token" in details, false);

  const overlap = doctorReport();
  overlap.checks[7] = {
    ...healthyCheck("provider"),
    providerDetails: providerDetails({ unavailableCapabilities: [{ name: "tools", reason: "Disabled" }] }),
  };
  assert.throws(() => decodeDoctorReport(overlap), /Duplicate provider capability tools/);

  const unsafe = doctorReport();
  unsafe.checks[7] = { ...healthyCheck("provider"), providerDetails: { ...providerDetails(), token: "secret" } };
  assert.throws(() => decodeDoctorReport(unsafe), /unexpected field token/);

  const authContradiction = doctorReport();
  authContradiction.checks[7] = {
    ...healthyCheck("provider"),
    providerDetails: providerDetails({ authentication: "unauthenticated" }),
  };
  assert.throws(() => decodeDoctorReport(authContradiction), /contradicts provider readiness/);

  const wrongSubsystem = doctorReport();
  wrongSubsystem.checks[0] = { ...healthyCheck("database"), providerDetails: providerDetails() };
  assert.throws(() => decodeDoctorReport(wrongSubsystem), /only valid for codex or provider/);
});

test("effective configuration is closed, ordered, source-attributed, and display-only", () => {
  const input = {
    schemaVersion: 1,
    projectRoot: "C:\\src\\darkstar",
    files: [
      { scope: "project", path: "C:\\src\\darkstar\\.darkstar\\config.yaml" },
      { scope: "user", path: "C:\\Users\\dev\\AppData\\Local\\DARKSTAR\\config\\config.yaml" },
    ],
    entries: [
      { path: "/provider/token", value: { kind: "redacted", display: "[redacted]" }, source: { scope: "user", reference: "user config" } },
      { path: "/daemon/parallelism", value: { kind: "number", display: "4" }, source: { scope: "default", reference: "shipped defaults" } },
      { path: "/provider/options", value: { kind: "json", display: "{\"mode\":\"safe\"}" }, source: { scope: "cli", reference: "--set provider.options" } },
    ],
  };

  const decoded = decodeEffectiveConfiguration(input);
  assert.deepEqual(decoded.files.map(({ scope }) => scope), ["user", "project"]);
  assert.deepEqual(decoded.entries.map(({ path }) => path), ["/daemon/parallelism", "/provider/options", "/provider/token"]);
  assert.deepEqual(decoded.entries[2], {
    path: "/provider/token",
    value: { kind: "redacted", display: "[redacted]" },
    source: { scope: "user", reference: "user config" },
  });
  assert.equal("raw" in decoded.entries[2].value, false);
  assert.equal(input.entries[0].path, "/provider/token", "decoding must not reorder the caller's entries");
});

test("effective configuration rejects sibling enums and fields that could leak values", () => {
  const base = {
    schemaVersion: 1,
    projectRoot: "C:\\src\\darkstar",
    files: [
      { scope: "user", path: "C:\\Users\\dev\\config.yaml" },
      { scope: "project", path: "C:\\src\\darkstar\\.darkstar\\config.yaml" },
    ],
    entries: [{ path: "/provider/token", value: { kind: "redacted", display: "[redacted]" }, source: { scope: "user", reference: "user config" } }],
  };
  assert.throws(
    () => decodeEffectiveConfiguration({ ...base, entries: [{ ...base.entries[0], source: { scope: "environment", reference: "env" } }] }),
    /configuration source scope is invalid/,
  );
  assert.throws(
    () => decodeEffectiveConfiguration({ ...base, entries: [{ ...base.entries[0], value: { kind: "secret", display: "hidden" } }] }),
    /configuration value kind is invalid/,
  );
  assert.throws(
    () => decodeEffectiveConfiguration({ ...base, entries: [{ ...base.entries[0], value: { kind: "redacted", display: "[redacted]", raw: "secret" } }] }),
    /unexpected field raw/,
  );
  assert.throws(
    () => decodeEffectiveConfiguration({ ...base, entries: [{ ...base.entries[0], value: { kind: "redacted", display: "secret" } }] }),
    /safe redacted display/,
  );
  assert.throws(
    () => decodeEffectiveConfiguration({ ...base, entries: [{ ...base.entries[0], path: "provider/token" }] }),
    /valid JSON Pointer/,
  );
  assert.throws(
    () => decodeEffectiveConfiguration({ ...base, entries: [{ ...base.entries[0], path: "/provider/~2token" }] }),
    /valid JSON Pointer/,
  );
  assert.equal(
    decodeEffectiveConfiguration({ ...base, entries: [{ ...base.entries[0], path: "/provider/~0token~1name" }] }).entries[0].path,
    "/provider/~0token~1name",
  );
  assert.throws(() => decodeEffectiveConfiguration({ ...base, debug: true }), /unexpected field debug/);
	assert.throws(() => decodeEffectiveConfiguration({ ...base, files: [] }), /exactly one user and one project file/);
	assert.throws(() => decodeEffectiveConfiguration({ ...base, files: [base.files[0]] }), /exactly one user and one project file/);
	assert.throws(() => decodeEffectiveConfiguration({ ...base, files: [base.files[0], { ...base.files[0], path: "second" }] }), /duplicate user files/);
});

test("project registration normalization and sorting are deterministic and non-mutating", () => {
  assert.deepEqual(normalizeProjectRegistration({ name: "  DARKSTAR  ", source: "  C:\\src\\darkstar  " }), {
    name: "DARKSTAR",
    source: "C:\\src\\darkstar",
  });
  assert.deepEqual(normalizeProjectRegistration("  Another  ", "  D:\\src\\another  "), {
    name: "Another",
    source: "D:\\src\\another",
  });
  assert.throws(() => normalizeProjectRegistration({ name: " ", source: "C:\\src" }), /name is required/);
  assert.throws(() => normalizeProjectRegistration({ name: "Name", source: " " }), /source is required/);

  const common = { sourceHash: "sha256:x", resourceVersion: 1, lastGlobalPosition: 1, createdAt: "now", updatedAt: "now" };
  const projects = [
    { ...common, id: "project-3", name: "Zulu", status: "active" },
    { ...common, id: "project-2", name: "alpha", status: "active" },
    { ...common, id: "project-4", name: "Able", status: "archived" },
    { ...common, id: "project-1", name: "Alpha", status: "active" },
  ];
  const sorted = sortProjects(projects);
  assert.deepEqual(sorted.map(({ id }) => id), ["project-1", "project-2", "project-3", "project-4"]);
  assert.deepEqual(projects.map(({ id }) => id), ["project-3", "project-2", "project-4", "project-1"]);
});

test("settings tabs accept only the closed route vocabulary", () => {
  assert.deepEqual(SETTINGS_TABS, ["health", "provider", "projects", "configuration"]);
  for (const tab of SETTINGS_TABS) assert.equal(parseSettingsTab(tab), tab);
  for (const invalid of [undefined, null, "", "providers", "Health", "debug"]) assert.equal(parseSettingsTab(invalid), "health");
});

test("settings route uses independent API-backed resources and exposes only project registration as a mutation", async () => {
  const [page, router, client] = await Promise.all([
    readFile(new URL("../src/pages/SettingsPage.tsx", import.meta.url), "utf8"),
    readFile(new URL("../src/app/router.tsx", import.meta.url), "utf8"),
    readFile(new URL("../src/api/client.ts", import.meta.url), "utf8"),
  ]);
  assert.match(router, /case "settings": return <SettingsPage \/>/);
  for (const method of ["getHealth", "getDoctorReport", "getEffectiveConfiguration", "listProjects"]) assert.match(page, new RegExp(`apiClient\\.${method}`));
  assert.match(page, /Promise\.allSettled/);
  assert.match(page, /apiClient\.registerProject/);
  assert.doesNotMatch(page, /apiClient\.(?:stopDaemon|restartDaemon|enableProvider|disableProvider|setConfiguration)/);
  assert.match(page, /Recommended action/);
  assert.doesNotMatch(page, /onClick=\{check\.action/);
  assert.match(client, /getDoctorReport\(projectRoot\?: string/);
  assert.match(client, /registerProject\(body: Schemas\["ProjectRegistration"\], idempotencyKey: string/);
});

test("generated settings contracts keep doctor and configuration projections closed", async () => {
  const generated = await readFile(new URL("../src/api/schema.generated.ts", import.meta.url), "utf8");
  assert.doesNotMatch(generated, /"DoctorReport": unknown/);
  assert.match(generated, /"DoctorReport": \{ "schemaVersion": 1; "status": "healthy" \| "degraded" \| "unhealthy"/);
  assert.match(generated, /"EffectiveConfiguration": \{ "schemaVersion": 1;/);
  assert.match(generated, /"kind": "string" \| "number" \| "boolean" \| "null" \| "json" \| "redacted"/);
  assert.match(generated, /"getEffectiveConfiguration"/);
});
