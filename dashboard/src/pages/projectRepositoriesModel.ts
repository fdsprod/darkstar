import type { components } from "../api/schema.generated";

export type RepositorySettings = components["schemas"]["RepositorySettings"];

// Text stays in the draft when a control switches back to inheritance. The mode
// is the only authority for whether that text contributes an override.
export type RepositorySettingsDraft = {
  configurationRoot: string;
  baseRef: string;
  worktreeBase: string;
  remote: string;
  targetBranch: string;
  scopeMode: "inherit" | "custom";
  scopeLines: string;
  validationMode: "inherit" | "custom";
  validationText: string;
};

const settingsKeys = new Set(["configurationRoot", "baseRef", "worktreeBase", "delivery", "pathScope", "validationProfiles"]);
const draftKeys = new Set(["configurationRoot", "baseRef", "worktreeBase", "remote", "targetBranch", "scopeMode", "scopeLines", "validationMode", "validationText"]);
const forbiddenKeys = new Set(["__proto__", "prototype", "constructor"]);

function objectValue(value: unknown, label: string): Record<string, unknown> {
  if (value === null || typeof value !== "object" || Array.isArray(value)) {
    throw new Error(`${label} must be an object.`);
  }
  const prototype = Object.getPrototypeOf(value);
  if (prototype !== Object.prototype && prototype !== null) {
    throw new Error(`${label} must contain plain values only.`);
  }
  for (const key of Object.keys(value)) {
    if (forbiddenKeys.has(key)) {
      throw new Error(`${label} cannot contain the key "${key}".`);
    }
  }
  return value as Record<string, unknown>;
}

function supportedKeys(value: Record<string, unknown>, allowed: Set<string>, label: string): void {
  for (const key of Object.keys(value)) {
    if (!allowed.has(key)) {
      throw new Error(`${label} contains an unsupported field: ${key}.`);
    }
  }
}

function optionalText(value: unknown, label: string): string {
  if (value === undefined) {
    return "";
  }
  if (typeof value !== "string") {
    throw new Error(`${label} must be text.`);
  }
  if (/[\u0000-\u001f\u007f]/u.test(value)) {
    throw new Error(`${label} cannot contain control characters.`);
  }
  return value;
}

function absolutePath(value: string): boolean {
  return value.startsWith("/") || /^[A-Za-z]:[\\/]/u.test(value) || /^\\\\[^\\]+\\[^\\]+/u.test(value);
}

function scopePaths(value: unknown): string[] {
  if (!Array.isArray(value) || !value.every((entry) => typeof entry === "string")) {
    throw new Error("Path scope must be a list of repository-relative paths.");
  }
  return value.map((entry: string) => {
    const path = entry.trim();
    if (!path || /[\\\u0000-\u001f\u007f]/u.test(path) || absolutePath(path) || /^[A-Za-z]:/u.test(path)) {
      throw new Error("Path scope entries must be relative paths using forward slashes.");
    }
    let depth = 0;
    for (const part of path.split("/")) {
      if (part === "..") {
        depth -= 1;
      } else if (part !== "" && part !== ".") {
        depth += 1;
      }
      if (depth < 0) {
        throw new Error("Path scope cannot leave the repository with '..'.");
      }
    }
    return path;
  });
}

function validationProfiles(value: unknown): Record<string, string[]> {
  const source = objectValue(value, "Validation profiles");
  const result: Record<string, string[]> = {};
  for (const [name, commands] of Object.entries(source)) {
    if (!name.trim() || name.trim() !== name || /[\u0000-\u001f\u007f]/u.test(name)) {
      throw new Error("Each validation profile needs a name without surrounding whitespace.");
    }
    if (!Array.isArray(commands) || !commands.every((command) => typeof command === "string" && command.trim() !== "" && !command.includes("\u0000"))) {
      throw new Error(`Validation profile "${name}" must contain an array of nonempty command strings.`);
    }
    result[name] = [...commands];
  }
  return result;
}

export function repositorySettingsDraft(settings: RepositorySettings = {}): RepositorySettingsDraft {
  const source = objectValue(settings, "Repository settings");
  supportedKeys(source, settingsKeys, "Repository settings");
  const delivery = source.delivery === undefined ? {} : objectValue(source.delivery, "Delivery defaults");
  supportedKeys(delivery, new Set(["remote", "targetBranch"]), "Delivery defaults");
  const scope = source.pathScope == null ? null : scopePaths(source.pathScope);
  const profiles = source.validationProfiles == null ? null : validationProfiles(source.validationProfiles);
  return {
    configurationRoot: optionalText(source.configurationRoot, "Configuration root"),
    baseRef: optionalText(source.baseRef, "Base ref"),
    worktreeBase: optionalText(source.worktreeBase, "Worktree directory"),
    remote: optionalText(delivery.remote, "Delivery remote"),
    targetBranch: optionalText(delivery.targetBranch, "Target branch"),
    scopeMode: scope === null ? "inherit" : "custom",
    scopeLines: scope === null ? "" : scope.join("\n"),
    validationMode: profiles === null ? "inherit" : "custom",
    validationText: JSON.stringify(profiles ?? {}, null, 2),
  };
}

export function repositorySettingsFromDraft(draft: RepositorySettingsDraft): RepositorySettings {
  const source = objectValue(draft, "Repository settings draft");
  supportedKeys(source, draftKeys, "Repository settings draft");
  if (draft.scopeMode !== "inherit" && draft.scopeMode !== "custom") {
    throw new Error("Choose whether path scope is inherited or customized.");
  }
  if (draft.validationMode !== "inherit" && draft.validationMode !== "custom") {
    throw new Error("Choose whether validation profiles are inherited or customized.");
  }
  const configurationRoot = optionalText(draft.configurationRoot, "Configuration root").trim();
  const baseRef = optionalText(draft.baseRef, "Base ref").trim();
  const worktreeBase = optionalText(draft.worktreeBase, "Worktree directory").trim();
  const remote = optionalText(draft.remote, "Delivery remote").trim();
  const targetBranch = optionalText(draft.targetBranch, "Target branch").trim();
  if (configurationRoot && !absolutePath(configurationRoot)) {
    throw new Error("Configuration root must be an absolute folder path.");
  }
  if (worktreeBase && !absolutePath(worktreeBase) && !configurationRoot) {
    throw new Error("Set an absolute configuration root before using a relative worktree directory.");
  }
  let pathScope: string[] | null = null;
  if (draft.scopeMode === "custom") {
    if (typeof draft.scopeLines !== "string") {
      throw new Error("Enter path scope as one path per line.");
    }
    pathScope = scopePaths(draft.scopeLines.split(/\r?\n/u).map((line) => line.trim()).filter((line) => line !== ""));
  }
  let profiles: Record<string, string[]> | null = null;
  if (draft.validationMode === "custom") {
    if (typeof draft.validationText !== "string" || !draft.validationText.trim()) {
      throw new Error("Enter validation profiles as a JSON object; use {} for an empty override.");
    }
    let parsed: unknown;
    try {
      parsed = JSON.parse(draft.validationText);
    } catch {
      throw new Error("Validation profiles must be valid JSON, such as {\"test\": [\"npm test\"]}.");
    }
    profiles = validationProfiles(parsed);
  }
  return {
    ...(configurationRoot ? { configurationRoot } : {}),
    ...(baseRef ? { baseRef } : {}),
    ...(worktreeBase ? { worktreeBase } : {}),
    ...(remote || targetBranch ? { delivery: { ...(remote ? { remote } : {}), ...(targetBranch ? { targetBranch } : {}) } } : {}),
    pathScope,
    validationProfiles: profiles,
  };
}
