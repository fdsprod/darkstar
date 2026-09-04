import { readFile, readdir, writeFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const schemaPath = resolve(here, "../../schemas/openapi-v1alpha1.json");
const outputPath = resolve(here, "../src/api/schema.generated.ts");
const document = JSON.parse(await readFile(schemaPath, "utf8"));
const schemaDirectory = dirname(schemaPath);
const localDocuments = new Map([[schemaPath, document]]);
await Promise.all((await readdir(schemaDirectory))
  .filter((name) => name.endsWith(".json"))
  .map(async (name) => {
    const localPath = resolve(schemaDirectory, name);
    if (!localDocuments.has(localPath)) localDocuments.set(localPath, JSON.parse(await readFile(localPath, "utf8")));
  }));

const quote = (value) => JSON.stringify(value);
const referenceName = (ref) => ref.startsWith("#/components/schemas/") ? ref.slice("#/components/schemas/".length) : null;

function pointerValue(root, fragment) {
  if (!fragment || fragment === "#") return root;
  if (!fragment.startsWith("#/")) return undefined;
  return fragment.slice(2)
    .split("/")
    .map((part) => decodeURIComponent(part).replaceAll("~1", "/").replaceAll("~0", "~"))
    .reduce((value, part) => value?.[part], root);
}

function resolveLocalReference(ref, sourcePath) {
  const hash = ref.indexOf("#");
  const filePart = hash >= 0 ? ref.slice(0, hash) : ref;
  const fragment = hash >= 0 ? ref.slice(hash) : "#";
  if (/^[a-z][a-z0-9+.-]*:/i.test(filePart)) return null;
  const targetPath = filePart ? resolve(dirname(sourcePath), decodeURIComponent(filePart)) : sourcePath;
  const target = localDocuments.get(targetPath);
  if (!target) return null;
  const schema = pointerValue(target, fragment);
  return schema === undefined ? null : { schema, sourcePath: targetPath };
}

function typeFor(schema, depth = 0, sourcePath = schemaPath, resolving = new Set()) {
  if (!schema || depth > 24) return "unknown";
  if (schema.$ref) {
    const name = sourcePath === schemaPath ? referenceName(schema.$ref) : null;
    if (name) return `components["schemas"][${quote(name)}]`;
    const key = `${sourcePath}|${schema.$ref}`;
    if (resolving.has(key)) return "unknown";
    const target = resolveLocalReference(schema.$ref, sourcePath);
    if (!target) return "unknown";
    const nextResolving = new Set(resolving);
    nextResolving.add(key);
    return typeFor(target.schema, depth + 1, target.sourcePath, nextResolving);
  }
  if (Object.hasOwn(schema, "const")) return quote(schema.const);
  if (schema.enum) return schema.enum.map(quote).join(" | ") || "never";
  if (schema.oneOf) return schema.oneOf.map((item) => typeFor(item, depth + 1, sourcePath, resolving)).join(" | ");
  if (schema.anyOf) return schema.anyOf.map((item) => typeFor(item, depth + 1, sourcePath, resolving)).join(" | ");
  if (Array.isArray(schema.type)) return schema.type.map((type) => typeFor({ ...schema, type }, depth + 1, sourcePath, resolving)).join(" | ");
  if (schema.type === "array") return `Array<${typeFor(schema.items, depth + 1, sourcePath, resolving)}>`;
  if (schema.type === "object" || schema.properties) {
    const required = new Set(schema.required ?? []);
    const fields = Object.entries(schema.properties ?? {}).map(([name, value]) => `${quote(name)}${required.has(name) ? "" : "?"}: ${typeFor(value, depth + 1, sourcePath, resolving)};`);
    if (schema.additionalProperties && typeof schema.additionalProperties === "object") fields.push(`[key: string]: ${typeFor(schema.additionalProperties, depth + 1, sourcePath, resolving)};`);
    return `{ ${fields.join(" ")} }`;
  }
  if (schema.type === "string") return "string";
  if (schema.type === "integer" || schema.type === "number") return "number";
  if (schema.type === "boolean") return "boolean";
  if (schema.type === "null") return "null";
  return "unknown";
}

function responseType(operation) {
  const success = Object.entries(operation.responses ?? {}).find(([status]) => /^2\d\d$/.test(status));
  if (!success) return "unknown";
  const content = success[1]?.content ?? {};
  const representation = content["application/json"] ?? content["application/octet-stream"] ?? content["application/zip"] ?? content["text/event-stream"];
  return typeFor(representation?.schema);
}

function bodyType(operation) {
  const schema = operation.requestBody?.content?.["application/json"]?.schema;
  return schema ? typeFor(schema) : "never";
}

const schemas = Object.entries(document.components?.schemas ?? {})
  .map(([name, schema]) => `    ${quote(name)}: ${typeFor(schema)};`)
  .join("\n");

const operations = [];
const definitions = [];
for (const [path, pathItem] of Object.entries(document.paths ?? {})) {
  for (const method of ["get", "post", "put", "patch", "delete"]) {
    const operation = pathItem[method];
    if (!operation?.operationId) continue;
    operations.push(`    ${quote(operation.operationId)}: { method: ${quote(method.toUpperCase())}; path: ${quote(path)}; response: ${responseType(operation)}; body: ${bodyType(operation)}; };`);
    definitions.push(`  ${quote(operation.operationId)}: { method: ${quote(method.toUpperCase())}, path: ${quote(path)} },`);
  }
}

const output = `/* This file is generated by dashboard/scripts/generate-api-client.mjs. */
/* Source: schemas/openapi-v1alpha1.json. Do not edit by hand. */

export interface components {
  schemas: {
${schemas}
  };
}

export interface ApiOperations {
${operations.join("\n")}
}

export type ApiOperationId = keyof ApiOperations;

export const operationDefinitions: Record<ApiOperationId, { method: string; path: string }> = {
${definitions.join("\n")}
};
`;

if (process.argv.includes("--check")) {
  const current = await readFile(outputPath, "utf8").catch(() => "");
  if (current !== output) {
    console.error("dashboard API types are stale; run npm run api:generate --workspace @darkstar/dashboard");
    process.exitCode = 1;
  }
} else {
  await writeFile(outputPath, output, "utf8");
}
