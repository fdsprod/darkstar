import { readFileSync } from "node:fs";
import { pathToFileURL } from "node:url";

const REQUIRED_TOPICS = ["local_api","prompt_injection","malicious_repo","path_traversal","unsafe_command","inherited_tool","secret_disclosure","git_damage","upload_parser","pr_side_effect"];

export function validateInventory(inventory) {
  const failures = [];
  const tests = new Map(inventory.tests.map((entry) => [entry.id, entry]));
  const boundaries = new Map(inventory.boundaries.map((entry) => [entry.id, entry]));
  for (const boundary of inventory.boundaries) {
    if (!boundary.owner) failures.push(`${boundary.id}: missing owner`);
    if (!Array.isArray(boundary.controls) || boundary.controls.length === 0) failures.push(`${boundary.id}: missing control`);
    if (!Array.isArray(boundary.negativeTests) || boundary.negativeTests.length === 0) failures.push(`${boundary.id}: missing test`);
    for (const control of boundary.controls) if (!/^DS-\d{3}$/.test(control)) failures.push(`${boundary.id}: invalid control ${control}`);
    for (const id of boundary.negativeTests) {
      const test = tests.get(id);
      if (!test) failures.push(`${boundary.id}: unknown test ${id}`);
      else if (!test.boundaries.includes(boundary.id)) failures.push(`${boundary.id}: test ${id} missing reverse mapping`);
    }
  }
  for (const entry of inventory.tests) {
    if (entry.failClosed !== true) failures.push(`${entry.id}: not fail closed`);
    if (!entry.expected) failures.push(`${entry.id}: missing expected evidence`);
    if (entry.boundaries.some((id) => !boundaries.has(id))) failures.push(`${entry.id}: unknown boundary`);
    if (entry.controls.length === 0 || entry.controls.some((id) => !/^DS-\d{3}$/.test(id))) failures.push(`${entry.id}: invalid controls`);
    for (const boundaryId of entry.boundaries) {
      const boundary = boundaries.get(boundaryId);
      if (boundary && !entry.controls.some((control) => boundary.controls.includes(control))) failures.push(`${entry.id}: no mapped boundary control`);
    }
  }
  const topics = new Set(inventory.tests.map((entry) => entry.topic));
  for (const topic of REQUIRED_TOPICS) if (!topics.has(topic)) failures.push(`missing topic: ${topic}`);
  if (inventory.reviewGate !== "DS-200") failures.push("missing DS-200 review gate");
  return failures;
}

export function summarizeInventory(inventory) {
  return {
    boundaries: inventory.boundaries.length,
    critical: inventory.boundaries.filter((entry) => entry.risk === "critical").length,
    high: inventory.boundaries.filter((entry) => entry.risk === "high").length,
    tests: inventory.tests.length,
    controls: new Set(inventory.boundaries.flatMap((entry) => entry.controls)).size,
  };
}

export function loadInventory(path) { return JSON.parse(readFileSync(path, "utf8")); }

export function main(argv = process.argv.slice(2)) {
  if (argv.length !== 1) { process.stderr.write("Usage: node scripts/threat-model-reference.mjs <threat-negative-tests.json>\n"); return 2; }
  const inventory = loadInventory(argv[0]);
  const failures = validateInventory(inventory);
  const result = { schemaVersion:inventory.schemaVersion, pass:failures.length === 0, summary:summarizeInventory(inventory), failures };
  process.stdout.write(`${JSON.stringify(result, null, 2)}\n`);
  return result.pass ? 0 : 1;
}

if (import.meta.url === pathToFileURL(process.argv[1]).href) process.exitCode = main();
