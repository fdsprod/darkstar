import { createHash } from "node:crypto";
import { existsSync, readFileSync, readdirSync, writeFileSync } from "node:fs";
import { dirname, relative, resolve } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const skillsRoot = resolve(root, "skills/builtin");
const manifestPath = resolve(skillsRoot, "manifest.json");

export const REQUIRED_CAPABILITIES = Object.freeze([
  "darkstar:artifact-reconciliation",
  "darkstar:change-inspection",
  "darkstar:evidence-research",
  "darkstar:pr-authoring",
  "darkstar:questions",
  "darkstar:readiness",
  "darkstar:route-assessment",
  "darkstar:story-decomposition",
  "darkstar:technical-design",
  "darkstar:tracer-bullets"
]);

function normalizedBytes(path) {
  return readFileSync(path, "utf8").replace(/\r\n/g, "\n");
}

function digest(value) {
  return createHash("sha256").update(value).digest("hex");
}

function quotedField(content, field) {
  const match = content.match(new RegExp(`^\\s*${field}:\\s*"([^"]+)"\\s*$`, "m"));
  return match?.[1] ?? "";
}

function plainField(content, field) {
  const match = content.match(new RegExp(`^${field}:\\s*(?:"([^"]+)"|([^\\r\\n]+))\\s*$`, "m"));
  return (match?.[1] ?? match?.[2] ?? "").trim();
}

function packageFiles(directory) {
  const files = [];
  const visit = (current) => {
    for (const entry of readdirSync(current, { withFileTypes:true }).sort((a,b) => a.name.localeCompare(b.name))) {
      const path = resolve(current, entry.name);
      if (entry.isDirectory()) visit(path);
      else files.push(path);
    }
  };
  visit(directory);
  return files;
}

export function inspectSkill(directory) {
  const folder = directory.split(/[\\/]/).at(-1);
  const skillPath = resolve(directory, "SKILL.md");
  const uiPath = resolve(directory, "agents/openai.yaml");
  if (!existsSync(skillPath) || !existsSync(uiPath)) throw new Error(`${folder}: SKILL.md and agents/openai.yaml are required`);
  const skill = normalizedBytes(skillPath);
  const ui = normalizedBytes(uiPath);
  const name = plainField(skill, "name");
  const version = quotedField(skill, "version");
  const capability = quotedField(skill, "capability");
  if (name !== folder) throw new Error(`${folder}: frontmatter name must match the folder`);
  if (!/^1\.\d+\.\d+$/.test(version)) throw new Error(`${folder}: metadata.version must be a v1 semantic version`);
  if (capability !== `darkstar:${folder}`) throw new Error(`${folder}: metadata.capability must be darkstar:${folder}`);
  if (/\[TODO|TODO:/i.test(skill + ui)) throw new Error(`${folder}: unfinished scaffold placeholder`);
  if (!ui.includes(`$${folder}`)) throw new Error(`${folder}: default prompt must mention $${folder}`);
  const files = packageFiles(directory).map((path) => ({
    path: relative(directory, path).replaceAll("\\", "/"),
    sha256: digest(normalizedBytes(path))
  }));
  return {
    name: capability,
    kind: "skill",
    class: "guaranteed",
    version,
    path: `${folder}/SKILL.md`,
    source: { type:"builtin_skill", locator:`${folder}/SKILL.md` },
    fingerprint: digest(JSON.stringify({ schemaVersion:1, name:capability, files })),
    dependencies: [],
    permissions: []
  };
}

export function buildManifest() {
  const skills = readdirSync(skillsRoot, { withFileTypes:true })
    .filter((entry) => entry.isDirectory())
    .map((entry) => inspectSkill(resolve(skillsRoot, entry.name)))
    .sort((a,b) => a.name.localeCompare(b.name));
  const capabilities = skills.map((skill) => skill.name);
  if (JSON.stringify(capabilities) !== JSON.stringify(REQUIRED_CAPABILITIES)) {
    throw new Error(`built-in capability set differs: ${JSON.stringify(capabilities)}`);
  }
  return { schemaVersion:1, kind:"darkstar_builtin_skills", version:"1.0.0", skills };
}

export function encodedManifest() {
  return `${JSON.stringify(buildManifest(), null, 2)}\n`;
}

export function main(argv = process.argv.slice(2)) {
  if (argv.length !== 1 || !["generate", "check"].includes(argv[0])) {
    process.stderr.write("Usage: node scripts/builtin-skills.mjs <generate|check>\n");
    return 2;
  }
  const encoded = encodedManifest();
  if (argv[0] === "generate") {
    writeFileSync(manifestPath, encoded, "utf8");
    process.stdout.write(`Generated ${relative(root, manifestPath)} with ${buildManifest().skills.length} skills.\n`);
    return 0;
  }
  if (!existsSync(manifestPath) || normalizedBytes(manifestPath) !== encoded) {
    process.stderr.write("skills/builtin/manifest.json is stale; run: node scripts/builtin-skills.mjs generate\n");
    return 1;
  }
  process.stdout.write(`Validated ${buildManifest().skills.length} versioned built-in skills.\n`);
  return 0;
}

if (import.meta.url === pathToFileURL(process.argv[1]).href) process.exitCode = main();
