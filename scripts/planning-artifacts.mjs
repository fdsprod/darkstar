import { readFileSync, readdirSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const repositoryRoot = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const defaultSchemaPath = resolve(repositoryRoot, "schemas/planning-artifact-v1alpha1.schema.json");
const defaultTemplatesDirectory = resolve(repositoryRoot, "templates/planning");

function localDefinition(schema, reference) {
  const match = reference.match(/^#\/\$defs\/([^/]+)$/);
  return match ? schema.$defs?.[match[1]] : undefined;
}

function variants(schema) {
  const values = new Map();
  for (const entry of schema.oneOf ?? []) {
    const definition = localDefinition(schema, entry.$ref);
    const artifactType = definition?.properties?.artifactType?.const;
    if (!artifactType) throw new Error(`planning artifact variant ${entry.$ref ?? "<inline>"} has no artifactType const`);
    values.set(artifactType, definition);
  }
  return values;
}

function frontmatter(content, file, issues) {
  const match = content.match(/^---\r?\n([\s\S]*?)\r?\n---(?:\r?\n|$)/);
  if (!match) {
    issues.push(`${file}: missing YAML frontmatter`);
    return {};
  }
  const metadata = {};
  for (const line of match[1].split(/\r?\n/)) {
    const field = line.match(/^([A-Za-z][A-Za-z0-9]*):\s*(.+?)\s*$/);
    if (!field) issues.push(`${file}: invalid frontmatter line ${JSON.stringify(line)}`);
    else metadata[field[1]] = field[2];
  }
  return metadata;
}

function levelTwoHeadings(content) {
  return [...content.matchAll(/^## ([^\r\n]+)\s*$/gm)].map((match) => match[1]);
}

export function lintPlanningTemplates(schemaPath = defaultSchemaPath, templatesDirectory = defaultTemplatesDirectory) {
  const schema = JSON.parse(readFileSync(schemaPath, "utf8"));
  const contracts = variants(schema);
  const issues = [];
  const seen = new Set();
  const files = readdirSync(templatesDirectory).filter((name) => name.endsWith(".md")).sort();
  for (const file of files) {
    const content = readFileSync(resolve(templatesDirectory, file), "utf8");
    const metadata = frontmatter(content, file, issues);
    const definition = contracts.get(metadata.artifactType);
    if (!definition) {
      issues.push(`${file}: unknown artifactType ${JSON.stringify(metadata.artifactType)}`);
      continue;
    }
    if (seen.has(metadata.artifactType)) issues.push(`${file}: duplicate template for ${metadata.artifactType}`);
    seen.add(metadata.artifactType);
    if (metadata.schemaVersion !== "1") issues.push(`${file}: schemaVersion must be 1`);
    const headings = levelTwoHeadings(content);
    if (new Set(headings).size !== headings.length) issues.push(`${file}: duplicate level-two heading`);
    const requiredSections = definition.required
      .filter((name) => name !== "artifactType" && name !== "schemaVersion")
      .map((name) => definition.properties[name]?.title);
    for (const heading of requiredSections) {
      if (!heading) issues.push(`${file}: required schema property has no title`);
      else if (!headings.includes(heading)) issues.push(`${file}: missing required section ${JSON.stringify(heading)}`);
    }
    for (const heading of headings) {
      if (!requiredSections.includes(heading)) issues.push(`${file}: section ${JSON.stringify(heading)} is not in the schema`);
    }
  }
  for (const artifactType of contracts.keys()) {
    if (!seen.has(artifactType)) issues.push(`missing template for ${artifactType}`);
  }
  return { artifactTypes: [...contracts.keys()].sort(), files, issues };
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const command = process.argv[2];
  if (command !== "check") {
    console.error("Usage: node scripts/planning-artifacts.mjs check");
    process.exitCode = 2;
  } else {
    const result = lintPlanningTemplates();
    if (result.issues.length) {
      console.error(result.issues.join("\n"));
      process.exitCode = 1;
    } else {
      console.log(`Validated ${result.files.length} planning artifact templates.`);
    }
  }
}
