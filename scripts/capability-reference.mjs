import { readFileSync } from "node:fs";
import { pathToFileURL } from "node:url";

const CLASS_RANK = Object.freeze({ guaranteed:0, registered:1, inherited:2, unsupported_discovery:3 });

function dependencyMissing(entry, registry) {
  return entry.dependencies.some((name) => !registry.some((candidate) =>
    candidate.name === name && candidate.availability === "available" && candidate.policy === "allow"));
}

function resolveOne(requirement, registry) {
  const named = registry.filter((entry) => entry.name === requirement.name && entry.kind === requirement.kind);
  if (named.length === 0) return { code:"CAPABILITY_REQUIRED_MISSING" };
  const versioned = requirement.version ? named.filter((entry) => entry.version === requirement.version) : named;
  if (versioned.length === 0) return { code:"CAPABILITY_VERSION_MISMATCH" };
  const ordered = [...versioned].sort((a,b) => CLASS_RANK[a.class] - CLASS_RANK[b.class] || a.id.localeCompare(b.id));
  const policyAllowed = ordered.filter((entry) => entry.policy !== "deny");
  if (policyAllowed.length === 0) return { code:"CAPABILITY_POLICY_DENIED" };
  const available = policyAllowed.filter((entry) => entry.availability === "available");
  if (available.length === 0) return { code:"CAPABILITY_UNHEALTHY" };
  const dependencyReady = available.filter((entry) => !dependencyMissing(entry, registry));
  if (dependencyReady.length === 0) return { code:"CAPABILITY_DEPENDENCY_MISSING" };
  const selected = dependencyReady[0];
  if (selected.class === "inherited" && requirement.acceptInherited !== true) return { code:"CAPABILITY_INHERITED_NOT_ALLOWED" };
  return { code:"OK", entry:selected, degraded:selected.class === "inherited" };
}

export function resolveRequirements(testCase, registry) {
  const selected = [];
  const omitted = [];
  let degraded = false;
  for (const requirement of testCase.requirements) {
    let result = resolveOne(requirement, registry);
    if (result.code !== "OK" && requirement.fallbacks) {
      for (const fallback of requirement.fallbacks) {
        result = resolveOne({ ...requirement, name:fallback, version:undefined, acceptInherited:false }, registry);
        if (result.code === "OK") { degraded = true; break; }
      }
    }
    if (result.code === "OK") {
      selected.push(result.entry.id);
      degraded ||= result.degraded;
      continue;
    }
    if (!requirement.required) {
      omitted.push({ name:requirement.name, reason: result.code === "CAPABILITY_REQUIRED_MISSING" ? "missing" : result.code.toLowerCase() });
      degraded = true;
      continue;
    }
    return { code:result.code, selected:[], omitted:[], degraded:false };
  }
  return { code:"OK", selected, omitted, degraded };
}

export function evaluatePostInvocation(entry) {
  if (entry.outcome === "permission_denied") return "stop_denied";
  if (entry.sideEffect !== "none" || entry.outcome === "ambiguous") return "reconcile_required";
  return entry.fallbackDeclared ? "fallback_allowed" : "stop_failed";
}

export function loadCatalog(path) { return JSON.parse(readFileSync(path, "utf8")); }

export function main(argv = process.argv.slice(2)) {
  if (argv.length !== 1) { process.stderr.write("Usage: node scripts/capability-reference.mjs <capability-scenarios.json>\n"); return 2; }
  const catalog = loadCatalog(argv[0]);
  const cases = catalog.cases.map((testCase) => {
    const actual = resolveRequirements(testCase, catalog.registry);
    return { id:testCase.id, pass:JSON.stringify(actual) === JSON.stringify(testCase.expected), actual, expected:testCase.expected };
  });
  const postInvocation = catalog.postInvocation.map((entry) => ({ id:entry.id, actual:evaluatePostInvocation(entry), expected:entry.expected }));
  const pass = cases.every((entry) => entry.pass) && postInvocation.every((entry) => entry.actual === entry.expected);
  process.stdout.write(`${JSON.stringify({ schemaVersion:catalog.schemaVersion, pass, cases, postInvocation }, null, 2)}\n`);
  return pass ? 0 : 1;
}

if (import.meta.url === pathToFileURL(process.argv[1]).href) process.exitCode = main();
