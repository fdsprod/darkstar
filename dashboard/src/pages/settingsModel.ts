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
