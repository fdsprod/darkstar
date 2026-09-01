#!/usr/bin/env node

import { existsSync, readFileSync } from "node:fs";
import { isAbsolute, relative, resolve, sep } from "node:path";
import { pathToFileURL } from "node:url";

const DECISION_ID = /^DS-\d{3}$/;
const RISK_ID = /^RSK-\d{3}$/;
const ISSUE_ID = /^DAR-\d+$/;
const SEVERITIES = new Set(["critical", "high", "medium", "low"]);
const DECISION_LIFECYCLES = Object.freeze({
  proposed: ["state"],
  accepted: ["state", "acceptedAt"],
  superseded: ["state", "supersededAt", "supersededBy"],
  rejected: ["state", "rejectedAt", "rationale"],
});
const RISK_LIFECYCLES = Object.freeze({
  open: ["state"],
  accepted: ["state", "acceptedBy", "acceptedAt", "reviewBy"],
  mitigated: ["state", "verifiedAt", "verifiedBy"],
  closed: ["state", "closedAt", "rationale"],
});

export class GovernanceError extends Error {
  constructor(code, message, location = "") {
    super(message);
    this.code = code;
    this.location = location;
  }

  toJSON() {
    return { code: this.code, message: this.message, ...(this.location ? { location: this.location } : {}) };
  }
}

function error(code, message, location = "") {
  return new GovernanceError(code, message, location);
}

function exactKeys(value, expected, location, failures) {
  if (!value || typeof value !== "object" || Array.isArray(value)) {
    failures.push(error("GOVERNANCE_LIFECYCLE_INVALID", "lifecycle must be a tagged object", location));
    return false;
  }
  const actual = Object.keys(value).sort();
  const wanted = [...expected].sort();
  if (JSON.stringify(actual) !== JSON.stringify(wanted)) {
    failures.push(error("GOVERNANCE_LIFECYCLE_INVALID", `lifecycle fields must be exactly: ${wanted.join(", ")}`, location));
    return false;
  }
  return true;
}

function exactRecordKeys(value, expected, location, failures) {
  if (!value || typeof value !== "object" || Array.isArray(value)) return false;
  const unexpected = Object.keys(value).filter((key) => !expected.includes(key));
  const missing = expected.filter((key) => !Object.hasOwn(value, key));
  if (unexpected.length || missing.length) {
    failures.push(error(
      "GOVERNANCE_RECORD_INVALID",
      `record fields differ; missing [${missing.join(", ")}], unexpected [${unexpected.join(", ")}]`,
      location,
    ));
    return false;
  }
  return true;
}

function validDate(value) {
  if (typeof value !== "string" || !/^\d{4}-\d{2}-\d{2}$/.test(value)) return false;
  const parsed = new Date(`${value}T00:00:00Z`);
  return !Number.isNaN(parsed.valueOf()) && parsed.toISOString().slice(0, 10) === value;
}

function validateLifecycle(lifecycle, variants, location, failures) {
  const expected = lifecycle && variants[lifecycle.state];
  if (!expected) {
    failures.push(error("GOVERNANCE_LIFECYCLE_INVALID", `unknown lifecycle state '${lifecycle?.state}'`, location));
    return;
  }
  if (!exactKeys(lifecycle, expected, location, failures)) return;
  for (const [key, value] of Object.entries(lifecycle)) {
    if (key.endsWith("At") || key === "reviewBy") {
      if (!validDate(value)) failures.push(error("GOVERNANCE_LIFECYCLE_INVALID", `${key} must be a calendar date`, `${location}/${key}`));
    } else if (key !== "state" && (typeof value !== "string" || !value)) {
      failures.push(error("GOVERNANCE_LIFECYCLE_INVALID", `${key} must be a nonempty string`, `${location}/${key}`));
    }
  }
}

function validateSourceIssue(source, location, failures) {
  exactRecordKeys(source, ["id", "url"], location, failures);
  if (!source || !ISSUE_ID.test(source.id ?? "") || typeof source.url !== "string" || !source.url.includes(source.id)) {
    failures.push(error("GOVERNANCE_ISSUE_LINK_INVALID", "sourceIssue must contain a DAR ID and matching URL", location));
  }
}

function validateDocument(entry, repositoryRoot, location, failures) {
  if (typeof entry.document !== "string" || isAbsolute(entry.document) || !entry.document.startsWith("docs/")) {
    failures.push(error("GOVERNANCE_DOCUMENT_INVALID", "document must be a repository-relative docs path", `${location}/document`));
    return;
  }
  const absolute = resolve(repositoryRoot, entry.document);
  const fromRoot = relative(repositoryRoot, absolute);
  if (fromRoot === ".." || fromRoot.startsWith(`..${sep}`) || !existsSync(absolute)) {
    failures.push(error("GOVERNANCE_DOCUMENT_INVALID", `document does not exist inside the repository: ${entry.document}`, `${location}/document`));
    return;
  }
  if (!readFileSync(absolute, "utf8").includes(entry.id)) {
    failures.push(error("GOVERNANCE_DOCUMENT_INVALID", `document does not identify ${entry.id}`, `${location}/document`));
  }
}

export function validateGovernance(decisionRegister, riskRegister, repositoryRoot) {
  const failures = [];
  if (!decisionRegister || decisionRegister.schemaVersion !== 1 || decisionRegister.kind !== "DecisionRegister" || !Array.isArray(decisionRegister.decisions)) {
    return [error("GOVERNANCE_REGISTER_INVALID", "unsupported decision register")];
  }
  if (!riskRegister || riskRegister.schemaVersion !== 1 || riskRegister.kind !== "RiskRegister" || !Array.isArray(riskRegister.risks)) {
    return [error("GOVERNANCE_REGISTER_INVALID", "unsupported risk register")];
  }

  const decisions = new Map();
  decisionRegister.decisions.forEach((entry, index) => {
    const location = `/decisions/${index}`;
    if (!entry || !DECISION_ID.test(entry.id ?? "")) {
      failures.push(error("GOVERNANCE_DECISION_INVALID", "decision requires a DS-NNN id", `${location}/id`));
      return;
    }
    exactRecordKeys(entry, ["id", "title", "document", "sourceIssue", "affectedIssues", "lifecycle"], location, failures);
    if (decisions.has(entry.id)) failures.push(error("GOVERNANCE_DECISION_INVALID", `duplicate decision ${entry.id}`, `${location}/id`));
    else decisions.set(entry.id, entry);
    if (typeof entry.title !== "string" || !entry.title) failures.push(error("GOVERNANCE_DECISION_INVALID", "title is required", `${location}/title`));
    validateSourceIssue(entry.sourceIssue, `${location}/sourceIssue`, failures);
    if (!Array.isArray(entry.affectedIssues) || entry.affectedIssues.length === 0 || entry.affectedIssues.some((id) => !DECISION_ID.test(id))) {
      failures.push(error("GOVERNANCE_ISSUE_LINK_INVALID", "affectedIssues must contain DS-NNN keys", `${location}/affectedIssues`));
    }
    validateLifecycle(entry.lifecycle, DECISION_LIFECYCLES, `${location}/lifecycle`, failures);
    if (entry.lifecycle?.state === "superseded" && !DECISION_ID.test(entry.lifecycle.supersededBy ?? "")) {
      failures.push(error("GOVERNANCE_SUPERSESSION_INVALID", "supersededBy must identify a DS-NNN decision", `${location}/lifecycle/supersededBy`));
    }
    validateDocument(entry, repositoryRoot, location, failures);
  });

  for (const required of Array.from({ length: 10 }, (_, index) => `DS-${String(index + 1).padStart(3, "0")}`)) {
    if (!decisions.has(required)) failures.push(error("GOVERNANCE_SPIKE_DECISION_MISSING", `foundational decision ${required} is not registered`));
  }
  for (const entry of decisions.values()) {
    if (entry.lifecycle?.state === "superseded" && !decisions.has(entry.lifecycle.supersededBy)) {
      failures.push(error("GOVERNANCE_SUPERSESSION_INVALID", `${entry.id} names unknown replacement ${entry.lifecycle.supersededBy}`));
    }
  }

  const risks = new Map();
  riskRegister.risks.forEach((entry, index) => {
    const location = `/risks/${index}`;
    if (!entry || !RISK_ID.test(entry.id ?? "")) {
      failures.push(error("GOVERNANCE_RISK_INVALID", "risk requires an RSK-NNN id", `${location}/id`));
      return;
    }
    exactRecordKeys(entry, ["id", "title", "severity", "owner", "statement", "affectedDecisions", "sourceIssues", "controls", "reviewTriggers", "lifecycle"], location, failures);
    if (risks.has(entry.id)) failures.push(error("GOVERNANCE_RISK_INVALID", `duplicate risk ${entry.id}`, `${location}/id`));
    else risks.set(entry.id, entry);
    if (!SEVERITIES.has(entry.severity)) failures.push(error("GOVERNANCE_RISK_INVALID", "invalid severity", `${location}/severity`));
    for (const key of ["title", "owner", "statement"]) if (typeof entry[key] !== "string" || !entry[key]) failures.push(error("GOVERNANCE_RISK_INVALID", `${key} is required`, `${location}/${key}`));
    for (const [key, pattern] of [["affectedDecisions", DECISION_ID], ["sourceIssues", ISSUE_ID], ["controls", DECISION_ID]]) {
      if (!Array.isArray(entry[key]) || entry[key].length === 0 || entry[key].some((id) => !pattern.test(id))) failures.push(error("GOVERNANCE_RISK_INVALID", `${key} contains invalid identifiers`, `${location}/${key}`));
    }
    if (!Array.isArray(entry.reviewTriggers) || entry.reviewTriggers.length === 0) failures.push(error("GOVERNANCE_RISK_INVALID", "reviewTriggers must not be empty", `${location}/reviewTriggers`));
    for (const id of entry.affectedDecisions ?? []) if (!decisions.has(id)) failures.push(error("GOVERNANCE_RISK_INVALID", `risk names unknown decision ${id}`, `${location}/affectedDecisions`));
    validateLifecycle(entry.lifecycle, RISK_LIFECYCLES, `${location}/lifecycle`, failures);
    if (entry.lifecycle?.state === "accepted" && (!DECISION_ID.test(entry.lifecycle.acceptedBy ?? "") || !decisions.has(entry.lifecycle.acceptedBy))) {
      failures.push(error("GOVERNANCE_RISK_INVALID", "acceptedBy must identify a registered decision", `${location}/lifecycle/acceptedBy`));
    }
    if (entry.lifecycle?.state === "accepted" && !entry.affectedDecisions?.includes(entry.lifecycle.acceptedBy)) {
      failures.push(error("GOVERNANCE_RISK_INVALID", "accepting decision must be affected by the risk", `${location}/lifecycle/acceptedBy`));
    }
    if (entry.lifecycle?.state === "accepted" && entry.lifecycle.reviewBy <= entry.lifecycle.acceptedAt) {
      failures.push(error("GOVERNANCE_RISK_INVALID", "reviewBy must be after acceptedAt", `${location}/lifecycle/reviewBy`));
    }
  });
  return failures;
}

export function implementationPreflight(decisionRegister, riskRegister, decisionIds) {
  const decisions = new Map(decisionRegister.decisions.map((entry) => [entry.id, entry]));
  const failures = [];
  const checked = [];
  for (const id of [...new Set(decisionIds)]) {
    const decision = decisions.get(id);
    if (!decision) {
      failures.push(error("GOVERNANCE_DECISION_UNKNOWN", `decision ${id} is not registered`));
      continue;
    }
    checked.push({ id, state: decision.lifecycle.state, document: decision.document });
    if (decision.lifecycle.state !== "accepted") {
      const replacement = decision.lifecycle.state === "superseded" ? `; use ${decision.lifecycle.supersededBy}` : "";
      failures.push(error("GOVERNANCE_DECISION_NOT_CURRENT", `${id} is ${decision.lifecycle.state}${replacement}`));
    }
  }
  const relatedRisks = riskRegister.risks
    .filter((risk) => risk.affectedDecisions.some((id) => decisionIds.includes(id)))
    .map((risk) => ({ id: risk.id, severity: risk.severity, state: risk.lifecycle.state, owner: risk.owner }));
  return { status: failures.length ? "blocked" : "passed", checked, relatedRisks, failures: failures.map((item) => item.toJSON()) };
}

export function loadRegister(path) {
  try { return JSON.parse(readFileSync(path, "utf8")); }
  catch (cause) { throw error("GOVERNANCE_REGISTER_INVALID", `cannot load JSON: ${cause.message}`, path); }
}

export function main(argv = process.argv.slice(2)) {
  if (argv.length < 2) {
    process.stderr.write("Usage: node scripts/governance-reference.mjs <decision-register.json> <risk-register.json> [DS-NNN ...]\n");
    return 2;
  }
  try {
    const decisions = loadRegister(resolve(argv[0]));
    const risks = loadRegister(resolve(argv[1]));
    const failures = validateGovernance(decisions, risks, process.cwd());
    const result = failures.length
      ? { status: "invalid", failures: failures.map((item) => item.toJSON()) }
      : { status: "passed", decisions: decisions.decisions.length, risks: risks.risks.length, ...(argv.length > 2 ? { preflight: implementationPreflight(decisions, risks, argv.slice(2)) } : {}) };
    if (result.preflight?.status === "blocked") result.status = "blocked";
    process.stdout.write(`${JSON.stringify(result, null, 2)}\n`);
    return result.status === "passed" ? 0 : 1;
  } catch (cause) {
    const normalized = cause instanceof GovernanceError ? cause : error("GOVERNANCE_REGISTER_INVALID", cause.message);
    process.stdout.write(`${JSON.stringify({ status: "invalid", failures: [normalized.toJSON()] }, null, 2)}\n`);
    return 1;
  }
}

if (import.meta.url === pathToFileURL(process.argv[1]).href) process.exitCode = main();
