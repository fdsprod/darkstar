#!/usr/bin/env node

// Executable reference for the DS-003 recovery decision table. This does not
// perform real side effects; it makes crash-window recovery decisions testable
// before the production daemon and connector implementations exist.

import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { pathToFileURL } from "node:url";

const strategies = Object.freeze({
  transactional: {
    committed: "adopt",
    absent: "retry",
    conflict: "reconcile_required",
  },
  content_addressed: {
    exact: "adopt",
    absent: "retry",
    mismatch: "reconcile_required",
  },
  lease: {
    held_current: "resume",
    expired_dead: "retry",
    expired_live: "reconcile_required",
    uncertain: "reconcile_required",
  },
  process: {
    live_owned: "resume",
    terminal_result: "adopt",
    terminated_no_result: "interrupt",
    unknown_identity: "reconcile_required",
  },
  provider_turn: {
    resumable_active: "resume",
    terminal_result: "adopt",
    absent_proven: "interrupt",
    active_writer_unknown: "reconcile_required",
  },
  exact_external: {
    exact: "adopt",
    absent: "retry",
    conflict: "reconcile_required",
  },
  git_commit: {
    exact_trailer_parent_tree: "adopt",
    absent_head_unchanged: "retry",
    mismatch_or_multiple: "reconcile_required",
  },
  remote_ref: {
    equal: "adopt",
    absent_or_ancestor: "retry",
    diverged: "reconcile_required",
  },
  pr_create: {
    unique_owned_exact: "adopt",
    absent: "retry",
    unowned_or_multiple: "reconcile_required",
  },
  pr_update: {
    desired_owned_revision: "adopt",
    older_owned_revision: "retry",
    marker_or_coordinate_conflict: "reconcile_required",
  },
});

export const SIDE_EFFECTS = Object.freeze({
  attempt_record: { strategy: "transactional", owner: "node_visit", commitPoint: "sqlite_transaction" },
  context_manifest: { strategy: "content_addressed", owner: "attempt", commitPoint: "blob_and_binding" },
  lease_acquire: { strategy: "lease", owner: "attempt_scope", commitPoint: "lease_cas" },
  provider_process: { strategy: "process", owner: "attempt", commitPoint: "process_identity_record" },
  provider_turn: { strategy: "provider_turn", owner: "attempt", commitPoint: "terminal_result_binding" },
  candidate_blob: { strategy: "content_addressed", owner: "attempt_result_revision", commitPoint: "blob_and_binding" },
  artifact_registration: { strategy: "content_addressed", owner: "artifact_version", commitPoint: "artifact_transaction" },
  checkpoint_action: { strategy: "transactional", owner: "checkpoint", commitPoint: "checkpoint_transaction" },
  visit_success: { strategy: "transactional", owner: "node_visit", commitPoint: "visit_transaction" },
  transition_token: { strategy: "transactional", owner: "source_visit_join_epoch", commitPoint: "token_transaction" },
  branch_create: { strategy: "exact_external", owner: "delivery_line", commitPoint: "observed_git_ref" },
  worktree_attach: { strategy: "exact_external", owner: "run_attachment", commitPoint: "observed_worktree" },
  candidate_accept: { strategy: "transactional", owner: "point_revision", commitPoint: "acceptance_transaction" },
  commit_create: { strategy: "git_commit", owner: "point_revision", commitPoint: "observed_git_commit" },
  push: { strategy: "remote_ref", owner: "publication", commitPoint: "observed_remote_ref" },
  pr_create: { strategy: "pr_create", owner: "delivery_line", commitPoint: "observed_provider_pr" },
  pr_update: { strategy: "pr_update", owner: "pull_request_revision", commitPoint: "observed_owned_body_revision" },
});

export class RecoveryScenarioError extends Error {
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
  return new RecoveryScenarioError(code, message, location);
}

export function decide(operation, observation) {
  const effect = SIDE_EFFECTS[operation];
  if (!effect) throw error("RECOVERY_OPERATION_UNKNOWN", `unknown operation '${operation}'`);
  const table = strategies[effect.strategy];
  if (!Object.hasOwn(table, observation)) {
    throw error(
      "RECOVERY_OBSERVATION_INVALID",
      `observation '${observation}' is invalid for '${operation}' (${effect.strategy})`,
    );
  }
  return table[observation];
}

export function validateSuite(document) {
  const errors = [];
  if (!document || document.apiVersion !== "darkstar.local/v1alpha1" || document.kind !== "RecoveryScenarioSuite") {
    return [error("RECOVERY_SUITE_INVALID", "unsupported apiVersion or kind")];
  }
  if (!Array.isArray(document.scenarios) || document.scenarios.length === 0) {
    return [error("RECOVERY_SUITE_INVALID", "scenarios must be a nonempty array", "/scenarios")];
  }

  const ids = new Set();
  const coveredOperations = new Set();
  const coveredStrategyObservations = new Set();
  document.scenarios.forEach((scenario, index) => {
    const location = `/scenarios/${index}`;
    if (!scenario || typeof scenario !== "object" || Array.isArray(scenario)) {
      errors.push(error("RECOVERY_SCENARIO_INVALID", "scenario must be an object", location));
      return;
    }
    if (typeof scenario.id !== "string" || !/^[a-z][a-z0-9_]{0,79}$/.test(scenario.id)) {
      errors.push(error("RECOVERY_SCENARIO_INVALID", "scenario needs a stable snake-case id", `${location}/id`));
    } else if (ids.has(scenario.id)) {
      errors.push(error("RECOVERY_SCENARIO_INVALID", `duplicate scenario id '${scenario.id}'`, `${location}/id`));
    } else ids.add(scenario.id);
    if (!SIDE_EFFECTS[scenario.operation]) {
      errors.push(error("RECOVERY_OPERATION_UNKNOWN", `unknown operation '${scenario.operation}'`, `${location}/operation`));
      return;
    }
    coveredOperations.add(scenario.operation);
    if (![
      "before_intent",
      "after_intent",
      "after_dispatch",
      "after_observation",
      "after_record",
    ].includes(scenario.crashAt)) {
      errors.push(error("RECOVERY_SCENARIO_INVALID", `invalid crash boundary '${scenario.crashAt}'`, `${location}/crashAt`));
    }
    const strategy = SIDE_EFFECTS[scenario.operation].strategy;
    if (!Object.hasOwn(strategies[strategy], scenario.observation)) {
      errors.push(error("RECOVERY_OBSERVATION_INVALID", `invalid observation '${scenario.observation}' for strategy '${strategy}'`, `${location}/observation`));
      return;
    }
    coveredStrategyObservations.add(`${strategy}:${scenario.observation}`);
    const actual = decide(scenario.operation, scenario.observation);
    if (scenario.expectedDecision !== actual) {
      errors.push(error("RECOVERY_EXPECTATION_MISMATCH", `expected '${scenario.expectedDecision}', strategy decides '${actual}'`, `${location}/expectedDecision`));
    }
  });

  for (const operation of Object.keys(SIDE_EFFECTS)) {
    if (!coveredOperations.has(operation)) {
      errors.push(error("RECOVERY_COVERAGE_MISSING", `operation '${operation}' has no failure-injection scenario`, "/scenarios"));
    }
  }
  for (const [strategy, observations] of Object.entries(strategies)) {
    if (!Object.values(SIDE_EFFECTS).some((effect) => effect.strategy === strategy)) continue;
    for (const observation of Object.keys(observations)) {
      if (!coveredStrategyObservations.has(`${strategy}:${observation}`)) {
        errors.push(error("RECOVERY_COVERAGE_MISSING", `strategy observation '${strategy}:${observation}' is not exercised`, "/scenarios"));
      }
    }
  }
  return errors;
}

export function runSuite(document) {
  const errors = validateSuite(document);
  if (errors.length) return { status: "invalid", errors: errors.map((item) => item.toJSON()) };
  const results = document.scenarios.map((scenario) => ({
    id: scenario.id,
    operation: scenario.operation,
    crashAt: scenario.crashAt,
    observation: scenario.observation,
    decision: decide(scenario.operation, scenario.observation),
  }));
  const counts = Object.fromEntries(["adopt", "resume", "retry", "interrupt", "reconcile_required"].map((decision) => [
    decision,
    results.filter((result) => result.decision === decision).length,
  ]));
  return { status: "passed", operationsCovered: Object.keys(SIDE_EFFECTS).length, scenarios: results.length, decisions: counts, results };
}

function load(path) {
  try {
    return JSON.parse(readFileSync(path, "utf8"));
  } catch (cause) {
    throw error("RECOVERY_SUITE_INVALID", `cannot load JSON: ${cause.message}`, path);
  }
}

export function main(argv = process.argv.slice(2)) {
  if (argv.length !== 1) {
    process.stderr.write("Usage: node scripts/recovery-reference.mjs <scenario-suite.json>\n");
    return 2;
  }
  try {
    const result = runSuite(load(resolve(argv[0])));
    process.stdout.write(`${JSON.stringify(result, null, 2)}\n`);
    return result.status === "passed" ? 0 : 1;
  } catch (cause) {
    const normalized = cause instanceof RecoveryScenarioError ? cause : error("RECOVERY_SUITE_INVALID", cause.message);
    process.stdout.write(`${JSON.stringify({ status: "invalid", errors: [normalized.toJSON()] }, null, 2)}\n`);
    return 1;
  }
}

if (import.meta.url === pathToFileURL(process.argv[1]).href) process.exitCode = main();

