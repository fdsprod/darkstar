import { createHash } from "node:crypto";
import { execFileSync } from "node:child_process";
import { existsSync, readFileSync, readdirSync, writeFileSync } from "node:fs";
import { dirname, relative, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const repositoryRoot = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const schemasDirectory = resolve(repositoryRoot, "schemas");
const catalogPath = resolve(schemasDirectory, "catalog.generated.json");
const contractPattern = /(?:\.schema\.json|openapi-[^.]+\.json)$/;
const httpMethods = new Set(["get", "put", "post", "delete", "options", "head", "patch", "trace"]);

function stable(value) {
  if (Array.isArray(value)) return value.map(stable);
  if (value && typeof value === "object") {
    return Object.fromEntries(Object.keys(value).sort().map((key) => [key, stable(value[key])]));
  }
  return value;
}

function canonicalJson(value) {
  return `${JSON.stringify(stable(value), null, 2)}\n`;
}

function contractNames(directory = schemasDirectory) {
  return readdirSync(directory).filter((name) => contractPattern.test(name)).sort();
}

function parseJson(text, source, errors) {
  try {
    return JSON.parse(text);
  } catch (error) {
    errors.push(`${source}: invalid JSON (${error.message})`);
    return null;
  }
}

export function loadContracts(directory = schemasDirectory) {
  const errors = [];
  const contracts = new Map();
  for (const name of contractNames(directory)) {
    const text = readFileSync(resolve(directory, name), "utf8");
    const document = parseJson(text, name, errors);
    if (document) contracts.set(name, { name, text, document });
  }
  if (errors.length) throw new Error(errors.join("\n"));
  return contracts;
}

function decodePointerPart(part) {
  return decodeURIComponent(part).replaceAll("~1", "/").replaceAll("~0", "~");
}

function resolvePointer(document, fragment) {
  if (!fragment || fragment === "#") return document;
  if (!fragment.startsWith("#/")) return undefined;
  return fragment.slice(2).split("/").map(decodePointerPart).reduce((value, part) => value?.[part], document);
}

function resolveReference(reference, sourceName, contracts) {
  const [filePart, fragmentPart = ""] = reference.split("#", 2);
  if (/^[a-z][a-z0-9+.-]*:/i.test(filePart)) return { external: true };
  const targetName = filePart ? filePart.replace(/^\.\//, "") : sourceName;
  const target = contracts.get(targetName);
  if (!target) return undefined;
  const value = resolvePointer(target.document, fragmentPart ? `#${fragmentPart}` : "#");
  return value === undefined ? undefined : { targetName, value };
}

function visit(value, path, callback) {
  callback(value, path);
  if (Array.isArray(value)) {
    value.forEach((entry, index) => visit(entry, `${path}/${index}`, callback));
  } else if (value && typeof value === "object") {
    for (const [key, entry] of Object.entries(value)) visit(entry, `${path}/${key}`, callback);
  }
}

function validateSchemaNode(node, path, errors) {
  if (!node || typeof node !== "object" || Array.isArray(node)) return;
  if (Array.isArray(node.required)) {
    if (new Set(node.required).size !== node.required.length) {
      errors.push(`${path}: required must contain unique property names`);
    }
    if (node.properties) {
      for (const name of node.required) {
        if (!(name in node.properties)) errors.push(`${path}: required property ${JSON.stringify(name)} has no schema`);
      }
    }
  }
  if (node.enum && new Set(node.enum.map((entry) => JSON.stringify(entry))).size !== node.enum.length) {
    errors.push(`${path}: enum contains duplicate values`);
  }
  if (node.type !== undefined && typeof node.type !== "object" && !["string"].includes(typeof node.type)) errors.push(`${path}: type must be a string or array`);
  if (node.pattern) {
    try { new RegExp(node.pattern); } catch (error) { errors.push(`${path}: invalid pattern (${error.message})`); }
  }
}

export function validateContracts(contracts = loadContracts()) {
  const errors = [];
  const ids = new Map();
  const operationIds = new Map();
  for (const [name, contract] of contracts) {
    const document = contract.document;
    const isOpenApi = typeof document.openapi === "string";
    if (isOpenApi) {
      if (!document.openapi.startsWith("3.1.")) errors.push(`${name}: OpenAPI 3.1 is required`);
      if (!document.info?.title || !document.info?.version) errors.push(`${name}: info.title and info.version are required`);
      if (!document.paths || typeof document.paths !== "object") errors.push(`${name}: paths is required`);
      for (const [apiPath, pathItem] of Object.entries(document.paths ?? {})) {
        if (!apiPath.startsWith("/api/v")) errors.push(`${name}: path ${apiPath} is not versioned`);
        for (const [method, operation] of Object.entries(pathItem)) {
          if (!httpMethods.has(method)) continue;
          if (!operation.operationId) errors.push(`${name}: ${method.toUpperCase()} ${apiPath} has no operationId`);
          else if (operationIds.has(operation.operationId)) errors.push(`${name}: duplicate operationId ${operation.operationId}`);
          else operationIds.set(operation.operationId, `${method.toUpperCase()} ${apiPath}`);
          if (!operation.responses || Object.keys(operation.responses).length === 0) errors.push(`${name}: ${method.toUpperCase()} ${apiPath} has no responses`);
        }
      }
    } else {
      if (document.$schema !== "https://json-schema.org/draft/2020-12/schema") errors.push(`${name}: JSON Schema draft 2020-12 is required`);
      if (!document.$id) errors.push(`${name}: $id is required`);
      if (!document.title) errors.push(`${name}: title is required`);
      if (document.$id) {
        if (ids.has(document.$id)) errors.push(`${name}: duplicate $id also used by ${ids.get(document.$id)}`);
        ids.set(document.$id, name);
      }
    }
    visit(document, name, (node, path) => {
      validateSchemaNode(node, path, errors);
      if (!node || typeof node !== "object" || Array.isArray(node) || typeof node.$ref !== "string") return;
      const resolved = resolveReference(node.$ref, name, contracts);
      if (!resolved && !node.$ref.startsWith("https://json-schema.org/")) errors.push(`${path}: unresolved $ref ${node.$ref}`);
    });
  }
  return errors;
}

function versionFromName(name, document) {
  return name.match(/-(v[0-9]+(?:alpha|beta)?[0-9]+)\.(?:schema\.)?json$/)?.[1]
    ?? document.info?.version
    ?? "unversioned";
}

export function createCatalog(contracts = loadContracts()) {
  return {
    schemaVersion: 1,
    contracts: [...contracts.values()].map(({ name, text, document }) => ({
      name: name.replace(/(?:\.schema)?\.json$/, ""),
      file: name,
      format: document.openapi ? "openapi-3.1" : "json-schema-2020-12",
      version: versionFromName(name, document),
      id: document.$id ?? null,
      sha256: createHash("sha256").update(text.replaceAll("\r\n", "\n")).digest("hex")
    }))
  };
}

export function checkCatalog(contracts = loadContracts()) {
  const expected = canonicalJson(createCatalog(contracts));
  const actual = existsSync(catalogPath) ? readFileSync(catalogPath, "utf8").replaceAll("\r\n", "\n") : "";
  return { expected, actual, matches: expected === actual };
}

function typeSet(value) {
  if (value === undefined) return null;
  return new Set(Array.isArray(value) ? value : [value]);
}

function scalarChanged(oldNode, newNode, keyword, path, issues) {
  if (oldNode[keyword] !== undefined && newNode[keyword] !== undefined && oldNode[keyword] !== newNode[keyword]) {
    issues.push(`${path}: ${keyword} changed from ${JSON.stringify(oldNode[keyword])} to ${JSON.stringify(newNode[keyword])}`);
  }
}

function isPureAlternativeWrapper(node, previous) {
  const structuralKeys = Object.keys(node).filter((key) => !["title", "description", "$comment", "default", "examples"].includes(key));
  if (structuralKeys.length !== 1 || !["oneOf", "anyOf"].includes(structuralKeys[0])) return false;
  const alternatives = node[structuralKeys[0]];
  return Array.isArray(alternatives) && alternatives.some((alternative) => JSON.stringify(stable(alternative)) === JSON.stringify(stable(previous)));
}

function compareSchema(oldNode, newNode, path, issues) {
  if (!oldNode || typeof oldNode !== "object" || Array.isArray(oldNode)) return;
  if (!newNode || typeof newNode !== "object" || Array.isArray(newNode)) {
    issues.push(`${path}: schema was removed`);
    return;
  }
  if (isPureAlternativeWrapper(newNode, oldNode)) return;
  const oldTypes = typeSet(oldNode.type);
  const newTypes = typeSet(newNode.type);
  if (!oldTypes && newTypes) {
    issues.push(`${path}: type constraint was added`);
  } else if (oldTypes && newTypes) {
    const removed = [...oldTypes].filter((type) => !newTypes.has(type));
    if (removed.length) issues.push(`${path}: accepted type(s) removed: ${removed.join(", ")}`);
  }
  if (newNode.const !== undefined && (oldNode.const === undefined || JSON.stringify(oldNode.const) !== JSON.stringify(newNode.const))) {
    issues.push(`${path}: const constraint was added or changed`);
  }
  if (!Array.isArray(oldNode.enum) && Array.isArray(newNode.enum)) {
    issues.push(`${path}: enum constraint was added`);
  } else if (Array.isArray(oldNode.enum) && Array.isArray(newNode.enum)) {
    const next = new Set(newNode.enum.map((entry) => JSON.stringify(entry)));
    const removed = oldNode.enum.filter((entry) => !next.has(JSON.stringify(entry)));
    if (removed.length) issues.push(`${path}: enum value(s) removed: ${removed.map(JSON.stringify).join(", ")}`);
  }
  const oldRequired = new Set(oldNode.required ?? []);
  const newRequired = new Set(newNode.required ?? []);
  const requiredAdded = [...newRequired].filter((name) => !oldRequired.has(name));
  if (requiredAdded.length) issues.push(`${path}: required property/properties added: ${requiredAdded.join(", ")}`);
  const oldProperties = oldNode.properties ?? {};
  const newProperties = newNode.properties ?? {};
  for (const [name, schema] of Object.entries(oldProperties)) {
    if (!(name in newProperties)) {
      if (newNode.additionalProperties === false) issues.push(`${path}: property removed: ${name}`);
    } else compareSchema(schema, newProperties[name], `${path}/properties/${name}`, issues);
  }
  if (oldNode.additionalProperties !== false && newNode.additionalProperties === false) issues.push(`${path}: additional properties are no longer accepted`);
  if (oldNode.additionalProperties && typeof oldNode.additionalProperties === "object" && newNode.additionalProperties && typeof newNode.additionalProperties === "object") {
    compareSchema(oldNode.additionalProperties, newNode.additionalProperties, `${path}/additionalProperties`, issues);
  }
  const oldDefs = oldNode.$defs ?? oldNode.definitions ?? {};
  const newDefs = newNode.$defs ?? newNode.definitions ?? {};
  for (const [name, schema] of Object.entries(oldDefs)) {
    if (!(name in newDefs)) issues.push(`${path}: definition removed: ${name}`);
    else compareSchema(schema, newDefs[name], `${path}/$defs/${name}`, issues);
  }
  if (oldNode.items && newNode.items) compareSchema(oldNode.items, newNode.items, `${path}/items`, issues);
  if ((oldNode.pattern ?? null) !== (newNode.pattern ?? null) && newNode.pattern !== undefined) issues.push(`${path}: pattern constraint was added or changed`);
  if (oldNode.format === undefined && newNode.format !== undefined) issues.push(`${path}: format constraint was added`);
  else scalarChanged(oldNode, newNode, "format", path, issues);
  for (const keyword of ["$id", "$ref"]) scalarChanged(oldNode, newNode, keyword, path, issues);
  for (const [keyword, direction] of [["minimum", "increase"], ["exclusiveMinimum", "increase"], ["minLength", "increase"], ["minItems", "increase"], ["minProperties", "increase"], ["maximum", "decrease"], ["exclusiveMaximum", "decrease"], ["maxLength", "decrease"], ["maxItems", "decrease"], ["maxProperties", "decrease"]]) {
    if (newNode[keyword] === undefined) continue;
    if (oldNode[keyword] === undefined || (direction === "increase" && newNode[keyword] > oldNode[keyword]) || (direction === "decrease" && newNode[keyword] < oldNode[keyword])) {
      issues.push(`${path}: ${keyword} became more restrictive`);
    }
  }
  for (const keyword of ["oneOf", "anyOf", "allOf", "not", "if", "then", "else"]) {
    if (newNode[keyword] !== undefined && JSON.stringify(stable(oldNode[keyword])) !== JSON.stringify(stable(newNode[keyword]))) {
      issues.push(`${path}: ${keyword} changed; publish a new schema version for this potentially breaking change`);
    }
  }
}

function parameterKey(parameter) {
  return parameter.$ref ?? `${parameter.in}:${parameter.name}`;
}

function compareOpenApi(oldDocument, newDocument, name, issues) {
  for (const [apiPath, oldPathItem] of Object.entries(oldDocument.paths ?? {})) {
    const newPathItem = newDocument.paths?.[apiPath];
    if (!newPathItem) {
      issues.push(`${name}: API path removed: ${apiPath}`);
      continue;
    }
    for (const method of httpMethods) {
      const oldOperation = oldPathItem[method];
      if (!oldOperation) continue;
      const newOperation = newPathItem[method];
      const location = `${name}: ${method.toUpperCase()} ${apiPath}`;
      if (!newOperation) {
        issues.push(`${location} was removed`);
        continue;
      }
      const oldParameters = new Map((oldOperation.parameters ?? []).map((parameter) => [parameterKey(parameter), parameter]));
      for (const parameter of newOperation.parameters ?? []) {
        const key = parameterKey(parameter);
        if (parameter.required && !oldParameters.has(key)) issues.push(`${location}: required parameter added: ${key}`);
      }
      if (!oldOperation.requestBody?.required && newOperation.requestBody?.required) issues.push(`${location}: request body became required`);
      for (const status of Object.keys(oldOperation.responses ?? {})) {
        if (!newOperation.responses?.[status]) issues.push(`${location}: response removed: ${status}`);
      }
    }
  }
  compareSchema({ $defs: oldDocument.components?.schemas ?? {} }, { $defs: newDocument.components?.schemas ?? {} }, `${name}#/components/schemas`, issues);
}

export function compareContracts(oldContracts, newContracts) {
  const issues = [];
  for (const [name, oldContract] of oldContracts) {
    const next = newContracts.get(name);
    if (!next) {
      issues.push(`${name}: versioned contract file was removed; keep the old version and add a new one`);
      continue;
    }
    const contractIssues = [];
    if (oldContract.document.openapi) compareOpenApi(oldContract.document, next.document, name, contractIssues);
    else compareSchema(oldContract.document, next.document, name, contractIssues);
    if (contractIssues.length && !hasExactVersionPromotion(name, oldContract.document, oldContracts, newContracts)) issues.push(...contractIssues);
  }
  return issues;
}

function versionedContractName(name) {
  const match = name.match(/^(.*)-(v(\d+)(alpha|beta)?(\d+))(\.schema)?\.json$/);
  if (!match) return null;
  return { family: match[1], version: match[2], major: Number(match[3]), phase: match[4] ?? "", revision: Number(match[5]), schemaSuffix: match[6] ?? "" };
}

function normalizedVersionedDocument(document, version) {
  const replaceVersion = (value) => {
    if (Array.isArray(value)) return value.map(replaceVersion);
    if (value && typeof value === "object") return Object.fromEntries(Object.entries(value).map(([key, entry]) => [key, replaceVersion(entry)]));
    return typeof value === "string" ? value.replaceAll(version, "<contract-version>") : value;
  };
  return JSON.stringify(stable(replaceVersion(document)));
}

function hasExactVersionPromotion(name, oldDocument, oldContracts, newContracts) {
  const source = versionedContractName(name);
  if (!source) return false;
  const normalizedSource = normalizedVersionedDocument(oldDocument, source.version);
  for (const [candidateName, candidate] of newContracts) {
    if (oldContracts.has(candidateName)) continue;
    const target = versionedContractName(candidateName);
    if (!target || target.family !== source.family || target.schemaSuffix !== source.schemaSuffix || target.major !== source.major || target.phase !== source.phase || target.revision <= source.revision) continue;
    if (normalizedVersionedDocument(candidate.document, target.version) === normalizedSource) return true;
  }
  return false;
}

function loadContractsFromGit(baseRef) {
  const gitOptions = {
    cwd: repositoryRoot,
    encoding: "utf8",
    env: { ...process.env, GIT_CONFIG_GLOBAL: process.platform === "win32" ? "NUL" : "/dev/null" }
  };
  const gitArguments = ["-c", `safe.directory=${repositoryRoot.replaceAll("\\", "/")}`];
  const names = execFileSync("git", [...gitArguments, "ls-tree", "-r", "--name-only", baseRef, "schemas"], gitOptions)
    .split(/\r?\n/)
    .map((name) => name.replace(/^schemas\//, ""))
    .filter((name) => contractPattern.test(name));
  const errors = [];
  const contracts = new Map();
  for (const name of names.sort()) {
    const text = execFileSync("git", [...gitArguments, "show", `${baseRef}:schemas/${name}`], gitOptions);
    const document = parseJson(text, `${baseRef}:schemas/${name}`, errors);
    if (document) contracts.set(name, { name, text, document });
  }
  if (errors.length) throw new Error(errors.join("\n"));
  return contracts;
}

function usage() {
  console.error("Usage: node scripts/schema-tool.mjs <generate|check|validate|compatibility> [--base <git-ref>]");
}

function main(argv) {
  const [command, ...args] = argv;
  const contracts = loadContracts();
  if (command === "generate") {
    const output = canonicalJson(createCatalog(contracts));
    writeFileSync(catalogPath, output);
    console.log(`Generated ${relative(repositoryRoot, catalogPath)} for ${contracts.size} contracts.`);
    return;
  }
  if (command === "check") {
    const errors = validateContracts(contracts);
    const catalog = checkCatalog(contracts);
    if (!catalog.matches) errors.push("schemas/catalog.generated.json is stale; run: node scripts/schema-tool.mjs generate");
    if (errors.length) throw new Error(errors.join("\n"));
    console.log(`Validated ${contracts.size} contracts and deterministic catalog.`);
    return;
  }
  if (command === "validate") {
    const errors = validateContracts(contracts);
    if (errors.length) throw new Error(errors.join("\n"));
    console.log(`Validated ${contracts.size} contracts.`);
    return;
  }
  if (command === "compatibility") {
    const baseIndex = args.indexOf("--base");
    const baseRef = baseIndex >= 0 ? args[baseIndex + 1] : undefined;
    if (!baseRef) throw new Error("compatibility requires --base <git-ref>");
    const issues = compareContracts(loadContractsFromGit(baseRef), contracts);
    if (issues.length) throw new Error(`Breaking schema changes relative to ${baseRef}:\n${issues.map((issue) => `- ${issue}`).join("\n")}`);
    console.log(`No breaking schema changes relative to ${baseRef}.`);
    return;
  }
  usage();
  process.exitCode = 2;
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try { main(process.argv.slice(2)); } catch (error) { console.error(error.message); process.exitCode = 1; }
}
