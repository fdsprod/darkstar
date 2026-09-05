import type { components } from "../api/schema.generated";

type ConfigurationSchemas = components["schemas"];

export type ConfigurationCatalog = ConfigurationSchemas["ConfigurationCatalog"];
export type ConfigurationDescriptor = ConfigurationCatalog["settings"][number];
export type ConfigurationState = ConfigurationSchemas["ConfigurationState"];
export type ConfigurationScope = ConfigurationState["scope"];
export type ConfigurationTypedValue = ConfigurationState["configured"][number]["value"];
export type ConfigurationMutationRequest = ConfigurationSchemas["ConfigurationMutationRequest"];
export type ConfigurationPreview = ConfigurationSchemas["ConfigurationPreview"];

export type Loadable<T> =
  | { state: "loading" }
  | { state: "ready"; value: T }
  | { state: "unsupported"; message: string }
  | { state: "failed"; message: string };

export type ConfigurationEditorState =
  | { state: "closed" }
  | { state: "editing"; key: string; draft: ConfigurationDraft }
  | { state: "previewing"; key: string; draft: ConfigurationDraft }
  | { state: "previewed"; key: string; draft: ConfigurationDraft; preview: ConfigurationPreview }
  | { state: "applying"; key: string; draft: ConfigurationDraft; preview: ConfigurationPreview }
  | { state: "stale"; key: string; draft: ConfigurationDraft; message: string }
  | { state: "failed"; key: string; draft: ConfigurationDraft; message: string };

export type ConfigurationNotice =
  | { state: "none" }
  | { state: "applied"; message: string; restart: "none" | "daemon"; replayed: boolean }
  | { state: "restored"; message: string; restart: "none" | "daemon"; replayed: boolean }
  | { state: "secret-written"; message: string; restart: "none" | "daemon"; replayed: boolean }
  | { state: "failed"; message: string };

export type ConfigurationDraft =
  | { operation: "unset" }
  | { operation: "set"; type: "boolean"; value: boolean }
  | { operation: "set"; type: "integer"; value: string }
  | { operation: "set"; type: "string" | "enum" | "path" | "secret_reference"; value: string };

export const CONFIGURATION_GROUPS = [
  "General",
  "Project",
  "Workflow defaults",
  "Providers",
  "Permissions",
  "Delivery",
  "Storage",
  "Advanced",
] as const;
export type ConfigurationGroup = (typeof CONFIGURATION_GROUPS)[number];

const PROJECT_ID_PATTERN = /^project_[0-9A-HJKMNP-TV-Z]{26}$/;

export const HEALTH_SUBSYSTEMS = [
  "database",
  "daemon",
  "paths",
  "git",
  "codex",
  "github",
  "configuration",
  "provider",
] as const;

export type HealthSubsystem = (typeof HEALTH_SUBSYSTEMS)[number];
export type HealthStatus = "healthy" | "degraded" | "unhealthy";
export type ProviderAuthentication = "authenticated" | "unauthenticated" | "unknown";
export type ProviderUsage = "ready" | "exhausted" | "unknown";

export interface AvailableProviderCapability {
  name: string;
  version: string;
}

export interface UnavailableProviderCapability {
  name: string;
  reason: string;
}

export interface ProviderDetails {
  name: string;
  version: string;
  executableIdentity: string;
  platform: string;
  authentication: ProviderAuthentication;
  usage: ProviderUsage;
  instructionSources: string[];
  conflictingExecutables: string[];
  availableCapabilities: AvailableProviderCapability[];
  unavailableCapabilities: UnavailableProviderCapability[];
}

interface HealthCheckBase {
  subsystem: HealthSubsystem;
  code: string;
  message: string;
  providerDetails?: ProviderDetails;
}

export type HealthCheck =
  | (HealthCheckBase & { status: "healthy"; action?: never })
  | (HealthCheckBase & { status: "degraded" | "unhealthy"; action: string });

export interface DoctorReport {
  schemaVersion: 1;
  status: HealthStatus;
  generatedAt: string;
  checks: HealthCheck[];
}

export type ConfigurationFileScope = "user" | "project";
export type ConfigurationSourceScope = "default" | "user" | "project" | "run" | "cli";
export type ConfigurationValueKind = "string" | "number" | "boolean" | "null" | "json" | "redacted";

export interface EffectiveConfigurationFile {
  scope: ConfigurationFileScope;
  path: string;
}

export interface EffectiveConfigurationEntry {
  path: string;
  value: {
    kind: ConfigurationValueKind;
    display: string;
  };
  source: {
    scope: ConfigurationSourceScope;
    reference: string;
  };
}

export interface EffectiveConfiguration {
  schemaVersion: 1;
  projectRoot: string;
  files: EffectiveConfigurationFile[];
  entries: EffectiveConfigurationEntry[];
}

export interface Project {
  id: string;
  name: string;
  sourceHash: string;
  status: "active" | "archived";
  resourceVersion: number;
  lastGlobalPosition: number;
  createdAt: string;
  updatedAt: string;
}

export interface ProjectRegistration {
  name: string;
  source: string;
}

export const SETTINGS_TABS = ["health", "provider", "projects", "configuration"] as const;
export type SettingsTab = (typeof SETTINGS_TABS)[number];

const HEALTH_STATUS_SEVERITY: Record<HealthStatus, number> = {
  healthy: 0,
  degraded: 1,
  unhealthy: 2,
};

const HEALTH_CODE_PATTERN = /^[A-Z][A-Z0-9_]*$/;

/** Decodes and canonically orders the complete, closed doctor response. */
export function decodeDoctorReport(value: unknown): DoctorReport {
  const report = exactRecord(value, ["schemaVersion", "status", "generatedAt", "checks"], "doctor report");
  if (report.schemaVersion !== 1) throw new Error("Unsupported doctor report schemaVersion.");
  const status = oneOf(report.status, ["healthy", "degraded", "unhealthy"] as const, "doctor report status");
  const generatedAt = dateTime(report.generatedAt, "doctor report generatedAt");
  const rawChecks = array(report.checks, "doctor report checks");
  if (rawChecks.length !== HEALTH_SUBSYSTEMS.length) {
    throw new Error(`Doctor report requires exactly ${HEALTH_SUBSYSTEMS.length} subsystem checks.`);
  }

  const rank = new Map<HealthSubsystem, number>(HEALTH_SUBSYSTEMS.map((subsystem, index) => [subsystem, index]));
  const seen = new Set<HealthSubsystem>();
  const checks = rawChecks.map(decodeHealthCheck);
  for (const check of checks) {
    if (seen.has(check.subsystem)) throw new Error(`Doctor report contains duplicate ${check.subsystem} checks.`);
    seen.add(check.subsystem);
  }
  for (const subsystem of HEALTH_SUBSYSTEMS) {
    if (!seen.has(subsystem)) throw new Error(`Doctor report is missing the ${subsystem} check.`);
  }
  checks.sort((left, right) => rank.get(left.subsystem)! - rank.get(right.subsystem)!);

  const derivedStatus = checks.reduce<HealthStatus>(
    (worst, check) => HEALTH_STATUS_SEVERITY[check.status] > HEALTH_STATUS_SEVERITY[worst] ? check.status : worst,
    "healthy",
  );
  if (status !== derivedStatus) {
    throw new Error(`Doctor report status ${status} contradicts checks with status ${derivedStatus}.`);
  }
  return { schemaVersion: 1, status, generatedAt, checks };
}

/** Decodes the display-only effective configuration response and orders its rows. */
export function decodeEffectiveConfiguration(value: unknown): EffectiveConfiguration {
  const configuration = exactRecord(value, ["schemaVersion", "projectRoot", "files", "entries"], "effective configuration");
  if (configuration.schemaVersion !== 1) throw new Error("Unsupported effective configuration schemaVersion.");
  const projectRoot = string(configuration.projectRoot, "effective configuration projectRoot");
  const files = array(configuration.files, "effective configuration files").map((candidate) => {
    const file = exactRecord(candidate, ["scope", "path"], "effective configuration file");
    return {
      scope: oneOf(file.scope, ["user", "project"] as const, "configuration file scope"),
      path: nonEmptyString(file.path, "configuration file path"),
    };
  });
  const fileScopes = new Set<ConfigurationFileScope>();
  for (const file of files) {
    if (fileScopes.has(file.scope)) throw new Error(`Effective configuration contains duplicate ${file.scope} files.`);
    fileScopes.add(file.scope);
  }
	if (files.length !== 2 || !fileScopes.has("user") || !fileScopes.has("project")) {
		throw new Error("Effective configuration must identify exactly one user and one project file.");
	}
  const fileRank: Record<ConfigurationFileScope, number> = { user: 0, project: 1 };
  files.sort((left, right) => fileRank[left.scope] - fileRank[right.scope] || compareText(left.path, right.path));

  const entries = array(configuration.entries, "effective configuration entries").map((candidate) => {
    const entry = exactRecord(candidate, ["path", "value", "source"], "effective configuration entry");
    const rendered = exactRecord(entry.value, ["kind", "display"], "effective configuration value");
    const source = exactRecord(entry.source, ["scope", "reference"], "effective configuration source");
    const kind = oneOf(rendered.kind, ["string", "number", "boolean", "null", "json", "redacted"] as const, "configuration value kind");
    const display = string(rendered.display, "configuration value display");
    if (kind === "redacted" && display !== "[redacted]") {
      throw new Error("Redacted configuration values must use the safe redacted display.");
    }
    return {
      path: jsonPointer(entry.path, "configuration entry path"),
      value: { kind, display },
      source: {
        scope: oneOf(source.scope, ["default", "user", "project", "run", "cli"] as const, "configuration source scope"),
        reference: nonEmptyString(source.reference, "configuration source reference"),
      },
    };
  });
  const entryPaths = new Set<string>();
  for (const entry of entries) {
    if (entryPaths.has(entry.path)) throw new Error(`Effective configuration contains duplicate entry path ${entry.path}.`);
    entryPaths.add(entry.path);
  }
  entries.sort((left, right) => compareText(left.path, right.path));
  return { schemaVersion: 1, projectRoot, files, entries };
}

/** Returns a detached project list with active projects first and stable textual tie-breakers. */
export function sortProjects<T extends Project>(projects: readonly T[]): T[] {
  return [...projects].sort((left, right) => {
    if (left.status !== right.status) return left.status === "active" ? -1 : 1;
    return compareFoldedText(left.name, right.name)
      || compareText(left.name, right.name)
      || compareText(left.id, right.id)
      || compareText(left.sourceHash, right.sourceHash);
  });
}

export function normalizeProjectRegistration(value: ProjectRegistration): ProjectRegistration;
export function normalizeProjectRegistration(name: string, source: string): ProjectRegistration;
export function normalizeProjectRegistration(value: ProjectRegistration | string, source?: string): ProjectRegistration {
  const candidate = typeof value === "string" ? { name: value, source } : value;
  const name = string(candidate.name, "project name").trim();
  const normalizedSource = string(candidate.source, "project source").trim();
  if (!name) throw new Error("Project name is required.");
  if (!normalizedSource) throw new Error("Project source is required.");
  return { name, source: normalizedSource };
}

export function parseSettingsTab(value: string | null | undefined): SettingsTab {
  return SETTINGS_TABS.includes(value as SettingsTab) ? value as SettingsTab : "health";
}

export function configurationScope(kind: "user", projectId?: string): ConfigurationScope;
export function configurationScope(kind: "project", projectId: string): ConfigurationScope;
export function configurationScope(kind: "user" | "project", projectId?: string): ConfigurationScope {
  if (kind === "user") return { type: "user" };
  if (!projectId || !PROJECT_ID_PATTERN.test(projectId)) throw new Error("Project scope requires a registered project identity.");
  return { type: "project", projectId };
}

export function parseConfigurationScope(kind: string | null | undefined, projectId: string | null | undefined): ConfigurationScope {
  return kind === "project" && projectId && PROJECT_ID_PATTERN.test(projectId)
    ? { type: "project", projectId }
    : { type: "user" };
}

export function scopeProjectId(scope: ConfigurationScope): string | undefined {
  return scope.type === "project" ? scope.projectId : undefined;
}

export function configurationScopesEqual(left: ConfigurationScope, right: ConfigurationScope): boolean {
  return left.type === right.type && (left.type === "user" || (right.type === "project" && left.projectId === right.projectId));
}

export function settingGroup(key: string): ConfigurationGroup {
  const root = key.split(".", 1)[0].toLocaleLowerCase("en-US");
  if (root === "project") return "Project";
  if (root === "workflow" || root === "workflows") return "Workflow defaults";
  if (root === "provider" || root === "providers") return "Providers";
  if (root === "permission" || root === "permissions" || root === "approval" || root === "approvals") return "Permissions";
  if (root === "delivery" || root === "github" || root === "gitlab") return "Delivery";
  if (root === "storage" || root === "artifact" || root === "artifacts" || root === "database") return "Storage";
  if (root === "advanced" || root === "experimental" || root === "debug") return "Advanced";
  return "General";
}

export function groupConfigurationSettings(settings: readonly ConfigurationDescriptor[]): Map<ConfigurationGroup, ConfigurationDescriptor[]> {
  const groups = new Map<ConfigurationGroup, ConfigurationDescriptor[]>(CONFIGURATION_GROUPS.map((group) => [group, []]));
  for (const setting of settings) groups.get(settingGroup(setting.key))!.push(setting);
  for (const values of groups.values()) values.sort((left, right) => compareFoldedText(left.title, right.title) || compareText(left.key, right.key));
  return groups;
}

export function settingMatchesSearch(setting: ConfigurationDescriptor, query: string): boolean {
  const normalized = query.trim().toLocaleLowerCase("en-US");
  if (!normalized) return true;
  return `${setting.title}\n${setting.description}\n${setting.key}\n${settingGroup(setting.key)}`.toLocaleLowerCase("en-US").includes(normalized);
}

export function configuredValue(state: ConfigurationState, key: string): ConfigurationTypedValue | undefined {
  return state.configured.find((entry) => entry.key === key)?.value;
}

export function effectiveSetting(state: ConfigurationState, key: string) {
  return state.effective.find((entry) => entry.key === key);
}

export function draftForSetting(descriptor: ConfigurationDescriptor, state: ConfigurationState): ConfigurationDraft {
  const current = configuredValue(state, descriptor.key) ?? effectiveSetting(state, descriptor.key)?.value ?? descriptor.default;
  if (current.type !== descriptor.type) throw new Error(`Configuration state type for ${descriptor.key} does not match its catalog descriptor.`);
  switch (current.type) {
    case "boolean": return { operation: "set", type: "boolean", value: current.value };
    case "integer": return { operation: "set", type: "integer", value: String(current.value) };
    case "string":
    case "enum":
    case "path":
    case "secret_reference": return { operation: "set", type: current.type, value: current.value };
  }
}

export function mutationFromDraft(
  scope: ConfigurationScope,
  descriptor: ConfigurationDescriptor,
  draft: ConfigurationDraft,
  expectedRevision: string,
): ConfigurationMutationRequest {
  if (!descriptor.allowedScopes.includes(scope.type)) throw new Error(`${descriptor.title} is read-only at ${scope.type} scope.`);
  if (!descriptor.actions.includes("preview") || !descriptor.actions.includes("apply")) throw new Error(`${descriptor.title} does not support editing.`);
  if (draft.operation === "unset") return { scope, key: descriptor.key, change: { operation: "unset" }, expectedRevision };
  if (draft.type !== descriptor.type) throw new Error(`${descriptor.title} requires a ${descriptor.type} value.`);
  let value: ConfigurationTypedValue;
  switch (draft.type) {
    case "boolean": value = { type: "boolean", value: draft.value }; break;
    case "integer": {
      if (!/^-?\d+$/.test(draft.value.trim())) throw new Error(`${descriptor.title} requires a whole number.`);
      const integer = Number(draft.value);
      if (!Number.isSafeInteger(integer)) throw new Error(`${descriptor.title} is outside the supported integer range.`);
      if (descriptor.constraints.minimum !== undefined && integer < descriptor.constraints.minimum) throw new Error(`${descriptor.title} must be at least ${descriptor.constraints.minimum}.`);
      if (descriptor.constraints.maximum !== undefined && integer > descriptor.constraints.maximum) throw new Error(`${descriptor.title} must be at most ${descriptor.constraints.maximum}.`);
      value = { type: "integer", value: integer };
      break;
    }
    case "string": value = { type: "string", value: draft.value }; break;
    case "enum":
      if (!descriptor.constraints.allowedValues?.includes(draft.value)) throw new Error(`${descriptor.title} must use a catalog option.`);
      value = { type: "enum", value: draft.value };
      break;
    case "path": value = { type: "path", value: draft.value }; break;
    case "secret_reference": value = { type: "secret_reference", value: draft.value }; break;
  }
  if (descriptor.constraints.required && typeof value.value === "string" && !value.value.trim()) throw new Error(`${descriptor.title} is required.`);
  return { scope, key: descriptor.key, change: { operation: "set", value }, expectedRevision };
}

export function typedValueText(value: ConfigurationTypedValue): string {
  return value.type === "boolean" ? (value.value ? "Enabled" : "Disabled") : String(value.value);
}

export function settingAnchor(key: string): string {
  return `setting-${key.replace(/[^a-zA-Z0-9_-]/g, "-")}`;
}

export function parseSettingDeepLink(value: string | null | undefined): string {
  return value?.trim() ?? "";
}

function decodeHealthCheck(value: unknown): HealthCheck {
  const candidate = record(value, "health check");
  const status = oneOf(candidate.status, ["healthy", "degraded", "unhealthy"] as const, "health check status");
  const keys = status === "healthy"
    ? ["subsystem", "status", "code", "message", "providerDetails"]
    : ["subsystem", "status", "code", "message", "action", "providerDetails"];
  exactKeys(candidate, keys, "health check", new Set(["providerDetails"]));
  const subsystem = oneOf(candidate.subsystem, HEALTH_SUBSYSTEMS, "health check subsystem");
  const code = nonEmptyString(candidate.code, "health check code");
  if (!HEALTH_CODE_PATTERN.test(code)) throw new Error(`Health check code ${code} is invalid.`);
  const message = nonEmptyString(candidate.message, "health check message");
  const providerDetails = candidate.providerDetails === undefined ? undefined : decodeProviderDetails(candidate.providerDetails);
  if (providerDetails && subsystem !== "codex" && subsystem !== "provider") {
    throw new Error("Provider details are only valid for codex or provider checks.");
  }
  if (providerDetails && status === "healthy"
    && (providerDetails.authentication === "unauthenticated" || providerDetails.usage === "exhausted")) {
    throw new Error("Healthy check contradicts provider readiness.");
  }
  if (status === "healthy") return { subsystem, status, code, message, ...(providerDetails ? { providerDetails } : {}) };
  const action = nonEmptyString(candidate.action, "non-healthy check action");
  return { subsystem, status, code, message, action, ...(providerDetails ? { providerDetails } : {}) };
}

function decodeProviderDetails(value: unknown): ProviderDetails {
  const details = exactRecord(value, [
    "name",
    "version",
    "executableIdentity",
    "platform",
    "authentication",
    "usage",
    "instructionSources",
    "conflictingExecutables",
    "availableCapabilities",
    "unavailableCapabilities",
  ], "provider details");
  const authentication = oneOf(details.authentication, ["authenticated", "unauthenticated", "unknown"] as const, "provider authentication");
  const usage = oneOf(details.usage, ["ready", "exhausted", "unknown"] as const, "provider usage");
  const instructionSources = uniqueStrings(details.instructionSources, "provider instruction sources");
  const conflictingExecutables = uniqueStrings(details.conflictingExecutables, "provider conflicting executables");
  const availableCapabilities = array(details.availableCapabilities, "available provider capabilities").map((candidate) => {
    const capability = exactRecord(candidate, ["name", "version"], "available provider capability");
    return {
      name: nonEmptyString(capability.name, "available capability name"),
      version: nonEmptyString(capability.version, "available capability version"),
    };
  });
  const unavailableCapabilities = array(details.unavailableCapabilities, "unavailable provider capabilities").map((candidate) => {
    const capability = exactRecord(candidate, ["name", "reason"], "unavailable provider capability");
    return {
      name: nonEmptyString(capability.name, "unavailable capability name"),
      reason: nonEmptyString(capability.reason, "unavailable capability reason"),
    };
  });
  const capabilityNames = new Set<string>();
  for (const capability of [...availableCapabilities, ...unavailableCapabilities]) {
    if (capabilityNames.has(capability.name)) throw new Error(`Duplicate provider capability ${capability.name}.`);
    capabilityNames.add(capability.name);
  }
  instructionSources.sort(compareText);
  conflictingExecutables.sort(compareText);
  availableCapabilities.sort((left, right) => compareText(left.name, right.name));
  unavailableCapabilities.sort((left, right) => compareText(left.name, right.name));
  return {
    name: nonEmptyString(details.name, "provider name"),
    version: string(details.version, "provider version"),
    executableIdentity: string(details.executableIdentity, "provider executable identity"),
    platform: string(details.platform, "provider platform"),
    authentication,
    usage,
    instructionSources,
    conflictingExecutables,
    availableCapabilities,
    unavailableCapabilities,
  };
}

function exactRecord(value: unknown, keys: readonly string[], label: string): Record<string, unknown> {
  const candidate = record(value, label);
  exactKeys(candidate, keys, label);
  return candidate;
}

function exactKeys(value: Record<string, unknown>, keys: readonly string[], label: string, optional = new Set<string>()): void {
  const allowed = new Set(keys);
  const unexpected = Object.keys(value).find((key) => !allowed.has(key));
  if (unexpected) throw new Error(`${label} contains unexpected field ${unexpected}.`);
  const missing = keys.find((key) => !optional.has(key) && !Object.prototype.hasOwnProperty.call(value, key));
  if (missing) throw new Error(`${label} is missing ${missing}.`);
}

function record(value: unknown, label: string): Record<string, unknown> {
  if (typeof value !== "object" || value === null || Array.isArray(value)) throw new Error(`${label} must be an object.`);
  return value as Record<string, unknown>;
}

function array(value: unknown, label: string): unknown[] {
  if (!Array.isArray(value)) throw new Error(`${label} must be an array.`);
  return value;
}

function string(value: unknown, label: string): string {
  if (typeof value !== "string") throw new Error(`${label} must be a string.`);
  return value;
}

function nonEmptyString(value: unknown, label: string): string {
  const decoded = string(value, label);
  if (!decoded) throw new Error(`${label} is required.`);
  return decoded;
}

function oneOf<const T extends readonly string[]>(value: unknown, choices: T, label: string): T[number] {
  if (typeof value !== "string" || !choices.includes(value)) throw new Error(`${label} is invalid.`);
  return value as T[number];
}

function dateTime(value: unknown, label: string): string {
  const decoded = string(value, label);
  if (!/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/.test(decoded)
    || Number.isNaN(Date.parse(decoded))) throw new Error(`${label} must be an RFC 3339 timestamp.`);
  return decoded;
}

function jsonPointer(value: unknown, label: string): string {
  const decoded = string(value, label);
  if (!decoded.startsWith("/") || /~(?:[^01]|$)/.test(decoded)) {
    throw new Error(`${label} must be a valid JSON Pointer.`);
  }
  return decoded;
}

function uniqueStrings(value: unknown, label: string): string[] {
  const values = array(value, label).map((entry) => nonEmptyString(entry, label));
  if (new Set(values).size !== values.length) throw new Error(`${label} must not contain duplicates.`);
  return values;
}

function compareText(left: string, right: string): number {
  return left < right ? -1 : left > right ? 1 : 0;
}

function compareFoldedText(left: string, right: string): number {
  return compareText(left.toLocaleLowerCase("en-US"), right.toLocaleLowerCase("en-US"));
}
