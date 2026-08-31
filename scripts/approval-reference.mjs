import { readFileSync } from "node:fs";
import { pathToFileURL } from "node:url";

const CLASS_ACTIONS = Object.freeze({
  workflow_checkpoint: new Set(["approve", "acknowledge", "request_changes", "reject", "satisfy_external", "cancel"]),
  workflow_control: new Set(["approve", "deny", "cancel"]),
  provider_permission: new Set(["allow_once", "allow_for_session", "deny", "cancel"]),
  external_delivery: new Set(["approve", "deny", "cancel"]),
});

const ACTION_STATES = Object.freeze({
  approve: "approved",
  acknowledge: "approved",
  satisfy_external: "approved",
  allow_once: "approved",
  allow_for_session: "approved",
  request_changes: "changes_requested",
  reject: "rejected",
  deny: "denied",
  cancel: "cancelled",
});

const ACTION_EFFECTS = Object.freeze({
  workflow_checkpoint: Object.freeze({
    approve: "checkpoint.commit_candidate",
    acknowledge: "checkpoint.commit_candidate",
    satisfy_external: "checkpoint.commit_candidate",
    request_changes: "checkpoint.create_revision",
    reject: "checkpoint.reject_candidate",
    cancel: "none",
  }),
  workflow_control: Object.freeze({ approve: "workflow.authorize_control", deny: "none", cancel: "none" }),
  provider_permission: Object.freeze({
    allow_once: "provider.respond_once",
    allow_for_session: "provider.respond_once_and_create_grant",
    deny: "provider.respond_once",
    cancel: "none",
  }),
  external_delivery: Object.freeze({ approve: "delivery.authorize_operation", deny: "none", cancel: "none" }),
});

function result(code, state, effect = "none") {
  return { code, state, effect };
}

function isExpired(at, expiresAt) {
  return expiresAt !== null && Date.parse(at) >= Date.parse(expiresAt);
}

function canonicalAction(action) {
  return JSON.stringify({
    action: action.action,
    scopeDigest: action.scopeDigest,
    policyDigest: action.policyDigest,
    sessionGrant: action.sessionGrant ?? null,
  });
}

function validSessionGrant(request, action) {
  const grant = action.sessionGrant;
  if (!grant || action.action !== "allow_for_session") return false;
  if (!request.subject?.attemptId || !request.subject?.providerThreadId) return false;
  if (grant.attemptId !== request.subject.attemptId || grant.providerThreadId !== request.subject.providerThreadId) return false;
  if (grant.policyDigest !== request.policyDigest || grant.scopeDigest !== request.scopeDigest) return false;
  if (grant.wildcard === true) return false;
  if (Date.parse(grant.expiresAt) > Date.parse(request.grantCeilingExpiresAt)) return false;
  return Date.parse(action.at) < Date.parse(grant.expiresAt);
}

export function decideApproval(testCase) {
  const { request, action, priorDecision = null } = testCase;
  if (!CLASS_ACTIONS[request.class]) return result("APPROVAL_CLASS_INVALID", request.status);

  if (priorDecision && action.actionKey === priorDecision.actionKey) {
    if (canonicalAction(action) === priorDecision.canonicalAction) {
      return { code: priorDecision.result.code, state: priorDecision.result.state, effect: "none", replayed: true };
    }
    return result("APPROVAL_IDEMPOTENCY_CONFLICT", request.status);
  }

  if (request.status !== "pending") return result("APPROVAL_ALREADY_RESOLVED", request.status);
  if (isExpired(action.at, request.expiresAt)) return result("APPROVAL_EXPIRED", "expired");
  if (!request.eligibleActors.includes(action.actor)) return result("APPROVAL_ACTOR_FORBIDDEN", request.status);
  if (action.expectedRevision !== request.aggregateRevision) return result("APPROVAL_REVISION_CONFLICT", request.status);
  if (action.scopeDigest !== request.scopeDigest) return result("APPROVAL_SCOPE_MISMATCH", request.status);
  if (action.policyDigest !== request.policyDigest) return result("APPROVAL_POLICY_STALE", request.status);
  if (!CLASS_ACTIONS[request.class].has(action.action) || !request.allowedActions.includes(action.action)) {
    return result("APPROVAL_CLASS_INVALID", request.status);
  }
  if (action.action === "allow_for_session" && !validSessionGrant(request, action)) {
    return result("APPROVAL_SESSION_GRANT_INVALID", request.status);
  }

  return result("OK", ACTION_STATES[action.action], ACTION_EFFECTS[request.class][action.action]);
}

export function evaluateOffline(testCase) {
  const { request, at } = testCase;
  if (request.status !== "pending") return result("NO_CHANGE", request.status);
  if (isExpired(at, request.expiresAt)) return result("EXPIRE_REQUIRED", "pending");
  return result("WAITING_FOR_DECISION", "pending");
}

export function reuseSessionGrant(testCase) {
  const { request, grant, at } = testCase;
  const matches = request.class === "provider_permission"
    && grant.attemptId === request.subject?.attemptId
    && grant.providerThreadId === request.subject?.providerThreadId
    && grant.policyDigest === request.policyDigest
    && grant.scopeDigest === request.scopeDigest
    && grant.capabilityFingerprint === request.capabilityFingerprint
    && grant.revoked !== true
    && grant.wildcard !== true
    && Date.parse(at) < Date.parse(grant.expiresAt);
  return matches
    ? result("OK", "pending", "provider.create_grant_authored_allow_once")
    : result("APPROVAL_SESSION_GRANT_INVALID", "pending");
}

export function runCatalog(catalog) {
  return catalog.cases.map((testCase) => {
    const actual = testCase.kind === "decision"
      ? decideApproval(testCase)
      : testCase.kind === "offline"
        ? evaluateOffline(testCase)
        : testCase.kind === "reuse_session_grant"
          ? reuseSessionGrant(testCase)
          : result("CASE_KIND_INVALID", testCase.request?.status ?? "unknown");
    return { id: testCase.id, pass: JSON.stringify(actual) === JSON.stringify(testCase.expected), actual, expected: testCase.expected };
  });
}

export function loadCatalog(path) {
  return JSON.parse(readFileSync(path, "utf8"));
}

export function main(argv = process.argv.slice(2)) {
  if (argv.length !== 1) {
    process.stderr.write("Usage: node scripts/approval-reference.mjs <approval-scenarios.json>\n");
    return 2;
  }
  const catalog = loadCatalog(argv[0]);
  const results = runCatalog(catalog);
  process.stdout.write(`${JSON.stringify({ schemaVersion: catalog.schemaVersion, results }, null, 2)}\n`);
  return results.every((entry) => entry.pass) ? 0 : 1;
}

if (import.meta.url === pathToFileURL(process.argv[1]).href) process.exitCode = main();
