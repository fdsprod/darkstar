# DARKSTAR Approval and Permission Model

> [Documentation index](../../README.md)

**Status:** Proposed normative contract for DAR-8 / DS-005  
**Depends on:** DS-001 and DS-002  
**Scope:** Workflow checkpoints, implementation approvals, workflow-control
authorization, provider interaction permissions, external delivery authorization,
session grants, offline behavior, audit, and idempotent API transitions

---

## 1. Decision

DARKSTAR represents every approval request as a typed, durable record. The request
class is immutable, and one decision API validates an action against that class.
An action can affect only the resource and effect named by the request.

There are four approval classes:

1. **Workflow checkpoint** — accepts, revises, acknowledges, rejects, or externally
   satisfies a candidate or implementation point.
2. **Workflow control** — authorizes a frozen route or run-control change, such as
   a route patch or explicit retry whose policy requires approval.
3. **Provider permission** — answers one opaque provider command, file, network, or
   tool request within an already authorized attempt ceiling.
4. **External delivery** — authorizes one daemon-owned external mutation such as a
   push, pull-request creation, publication, or deployment handoff.

User-input requests are interactions, not approvals. They use a separate typed
response API and cannot carry an `approve`, `allow`, or delivery-authorizing
effect.

The central invariant is:

```text
provider permission response
  != workflow checkpoint action
  != workflow control authorization
  != external delivery authorization
```

No conversion exists between those types. A provider response cannot commit node
outputs, fire a transition, change a route, expand DARKSTAR policy, or authorize a
daemon-owned external effect.

## 2. Authority layers

The daemon, not the provider, is the policy authority. Effective provider access
is the intersection of independently evaluated constraints:

```text
effective provider access =
  installation ceiling
  ∩ project policy
  ∩ frozen workflow/node permission manifest
  ∩ attempt access class and canonical roots
  ∩ applicable explicit grant
  ∩ provider capability/sandbox enforcement
```

A deny at any layer wins. Approval at a lower layer never widens a higher layer.
Provider configuration inherited from a user profile is treated only as a further
restriction unless the same capability is already present in the frozen DARKSTAR
manifest. Prompts and model output are never policy input.

DARKSTAR-owned Git, publication, and delivery operations are not exposed as
provider tools. They require their own external-delivery authorization when policy
calls for one and follow the side-effect protocol in the
[crash recovery model](../recovery/RECOVERY_MODEL.md).

## 3. Approval classes

| Class | Typical request | Eligible actor | Scope | Permitted terminal decisions | Effect of approval |
|---|---|---|---|---|---|
| `workflow_checkpoint` | Artifact review, implementation-point review, acknowledge, external condition | Named user/role; for `external`, a configured connector identity | Checkpoint ID, visit, candidate revision/digest, checkpoint mode, policy digest | `approve`, `acknowledge`, `request_changes`, `reject`, `satisfy_external`, `cancel`, `expire` as allowed by mode | Commits or revises only that candidate; visit success and transition emission remain separate idempotent commits |
| `workflow_control` | Route patch, terminal expansion, retry/continue requiring policy approval | Named user/role with control authority | Run ID, expected run/route revision, exact proposed operation digest, policy digest | `approve`, `deny`, `cancel`, `expire` | Authorizes only the named control command; the command still validates and commits separately |
| `provider_permission` | Codex command/file/network/tool request | Named user/role with permission authority, or a matching bounded session grant | Attempt, provider thread/turn/request IDs, normalized permission, canonical targets, request digest, policy digest | `allow_once`, `allow_for_session`, `deny`, `cancel`, `expire` | Sends one response to the opaque provider request; never changes workflow or delivery state |
| `external_delivery` | Push, create/update PR, publish, deploy/handoff | Named user/role or configured external approval identity | Operation ID, delivery line, connector/account, exact destination and desired-effect digest, policy digest | `approve`, `deny`, `cancel`, `expire` | Authorizes preparation/dispatch of that operation only; it does not prove the effect completed |

Implementation approval is a `workflow_checkpoint` subtype, not a provider
permission. External checkpoint satisfaction is also a workflow checkpoint: the
external actor attests to a condition; it does not authorize unrelated delivery.

## 4. Durable request contract

Every approval request stores at least:

```json
{
  "approvalRequestId": "apr_opaque",
  "class": "provider_permission",
  "status": "pending",
  "subject": {
    "runId": "run_opaque",
    "visitId": "visit_opaque",
    "attemptId": "attempt_opaque",
    "providerThreadId": "provider_opaque",
    "providerRequestId": "provider_request_opaque"
  },
  "scope": {},
  "scopeDigest": "sha256-canonical-scope",
  "policyDigest": "sha256-frozen-policy",
  "eligibleActors": ["user:opaque-or-role:release-manager"],
  "allowedActions": ["allow_once", "allow_for_session", "deny", "cancel"],
  "createdAt": "RFC3339",
  "expiresAt": "RFC3339-or-null",
  "providerEvidenceRef": "optional-opaque-reference",
  "aggregateRevision": 7
}
```

The canonical scope is class-specific:

- Checkpoints bind the exact candidate revision and content digest. A new candidate
  requires a new request; approval of an older draft cannot accept the new draft.
- Workflow controls bind the expected aggregate/route revision and the canonical
  operation payload. Editing a patch invalidates the request.
- Provider permissions bind the attempt plus opaque provider request and normalized
  target. File roots are canonical paths; commands are argument arrays; network
  targets include scheme, host, port, and method/tool class as applicable.
- Delivery approvals bind the exact operation identity, destination coordinates,
  and desired-effect digest. A changed commit, remote, PR coordinates, publication
  body revision, or deployment target requires a new request.

The server supplies `approvalRequestId`, immutable digests, allowed actions, and
expiry. Clients render them but do not synthesize them.

## 5. One idempotent decision API

All four classes use one command:

```http
POST /v1/approval-requests/{approvalRequestId}/decisions
Idempotency-Key: <client-generated opaque key>
If-Match: <aggregate revision>
```

```json
{
  "action": "allow_once",
  "scopeDigest": "sha256-canonical-scope",
  "policyDigest": "sha256-frozen-policy",
  "comment": "optional bounded text",
  "sessionGrant": null
}
```

The authenticated actor comes from the local API/session boundary, never from a
request-body actor field. The transaction:

1. reads the request by ID and checks class, pending state, expiry, actor, allowed
   action, aggregate revision, scope digest, and policy digest;
2. validates any session grant against the request ceiling;
3. inserts the immutable decision at unique key
   `(approval_request_id, client_action_key)`;
4. advances the request state and appends the audit event in the same SQLite
   transaction; and
5. returns the committed decision result.

Repeating the same key and canonical payload returns the original response. The
same key with a different payload returns `APPROVAL_IDEMPOTENCY_CONFLICT`. A new key
against a resolved request returns `APPROVAL_ALREADY_RESOLVED`. Neither case
executes a downstream effect.

Expiry is a system-authored use of the same transition service with stable action
key `expire:<approval_request_id>:<expires_at>`. Cancellation by a user or run
control uses the same service and records the initiating actor/control identity.
Provider response delivery, visit success, route-patch application, and delivery
dispatch are separate operations with their own commit points. A decision never
claims that its downstream effect completed.

User input uses `RespondToInteraction(interaction_id, response, action_key)` and a
distinct idempotency namespace. Passing a user-input ID to the approval endpoint
returns `APPROVAL_CLASS_INVALID`.

## 6. Transition tables

### 6.1 Common request lifecycle

| From | Command/event | Guard | To | Durable result |
|---|---|---|---|---|
| absent | create request | Subject active; frozen scope/policy valid | `pending` | Request and `approval.requested` event |
| `pending` | valid class action | Actor, revision, action, scope, policy, and time valid | class terminal state | Decision and `approval.decided` event |
| `pending` | expire | `now >= expiresAt` | `expired` | System decision; no downstream authorization |
| `pending` | cancel | Authorized cancellation or subject terminal | `cancelled` | Cancellation reason and evidence |
| resolved | duplicate same key/payload | Exact committed decision exists | unchanged | Original response; no new event/effect |
| resolved | same key/different payload | Key exists with different digest | unchanged | `APPROVAL_IDEMPOTENCY_CONFLICT` |
| resolved | new decision key | No exact duplicate | unchanged | `APPROVAL_ALREADY_RESOLVED` |

### 6.2 Workflow checkpoint

| Mode | Action | Request state | Checkpoint/visit effect | Run effect |
|---|---|---|---|---|
| `approve` / `approve_on_change` | `approve` | `approved` | Candidate becomes eligible for separate visit-success commit | Resume after durable success/transition work |
| `approve` / `approve_on_change` | `request_changes` | `changes_requested` | Candidate retained; create next revision attempt with feedback | Remains active/waiting until scheduling |
| `approve` / `approve_on_change` | `reject` | `rejected` | Candidate retained and visit rejected | Waits for explicit route/control decision |
| `acknowledge` | `acknowledge` | `approved` | Candidate becomes eligible for visit-success commit | Resume |
| `external` | `satisfy_external` | `approved` | Exact condition/evidence becomes eligible for checkpoint commit | Resume |
| any | `cancel` / `expire` | `cancelled` / `expired` | Candidate retained; never accepted | Waits or follows explicit run-control policy; no implicit retry |

### 6.3 Workflow control

| Action | Request state | Effect |
|---|---|---|
| `approve` | `approved` | Named control operation may be submitted once using its own operation key and expected revision |
| `deny` | `denied` | Proposal is retained and not applied; the run remains at its prior route/control state |
| `cancel` / `expire` | `cancelled` / `expired` | Proposal closes without changing the run |

Approval does not bypass current validation. A route patch that became invalid
between request and application fails with its route/revision error and requires a
new proposal; it is not silently rebased.

### 6.4 Provider permission

| Action | Request state | Provider effect | DARKSTAR effect |
|---|---|---|---|
| `allow_once` | `approved` | Respond once to exact opaque request | None beyond interaction audit |
| `allow_for_session` | `approved` | Respond once and create bounded grant | Grant may answer later exact matching requests in the same attempt/thread |
| `deny` | `denied` | Send denial once if provider request remains active | Attempt continues or fails according to provider result; no workflow rejection |
| `cancel` | `cancelled` | Cancel pending interaction; attempt cancellation is a separate control | No workflow checkpoint action |
| `expire` | `expired` | Deny/close only if provider protocol supports an exact safe response; otherwise keep attempt waiting or cancel by policy | Never auto-allow |

Provider disconnection after the DARKSTAR decision but before response
acknowledgement is reconciled by opaque request ID. DARKSTAR retries only when the
adapter proves the same request remains pending; otherwise it adopts provider
evidence or pauses. It never turns an uncertain response into a broader grant.

### 6.5 External delivery

| Action | Request state | Operation effect |
|---|---|---|
| `approve` | `approved` | Exact operation may enter the prepare/execute/observe/record protocol |
| `deny` | `denied` | No dispatch; candidate/commit remains preserved |
| `cancel` / `expire` | `cancelled` / `expired` | No dispatch; operator may create a new request if policy permits |

An approval is not evidence that a push, PR, publication, or deployment occurred.
Only authority-specific observation can commit that external effect.

## 7. Session grants

A session grant is available only for `provider_permission` and is never a
workflow or delivery approval. `allow_for_session` must include:

- grant ID and source decision ID;
- exact `attempt_id` and provider thread ID;
- normalized permission kind and canonical scope ceiling;
- frozen policy digest and provider capability fingerprint;
- issuer identity and issue/expiry timestamps; and
- revocation state.

MVP grants cannot be installation-wide, project-wide, wildcard-targeted, or
transferred to another attempt/thread. Their expiry cannot exceed the attempt,
provider session, request ceiling, or configured maximum. `allow_for_session`
offered by a provider UI is accepted only if DARKSTAR can express and enforce this
same bound; otherwise the dashboard offers `allow_once` only.

On a later matching provider request, DARKSTAR creates a distinct approval request
and records a deterministic grant-authored `allow_once` decision for that opaque
request. Matching requires exact class, attempt, thread, permission kind, scope
containment, policy digest, capability fingerprint, time validity, and non-revoked
state. Thus every provider response still has one auditable idempotent transition.

After restart, a grant may be reused only after the exact provider thread/attempt is
resumed and all matching fields remain valid. Free-form conversation, provider
memory, a prior `approved` event, or an inherited provider setting never
reconstructs a grant.

## 8. Offline, denial, expiry, and cancellation behavior

### 8.1 Offline clients and services

- Creating a request never assumes a dashboard or CLI is connected. The durable
  request remains `pending`, the run/attempt records an explicit wait reason, and
  notifications are rebuildable from state.
- No approval is inferred from timeout, silence, a closed browser, daemon restart,
  provider disconnect, or an unavailable external approval service.
- A user may decide a still-valid request from any authorized client. API and
  dashboard use the same command and result.
- An external condition must be read from its configured authority and bound to the
  exact request. Cached evidence is accepted only if policy explicitly permits it
  and its identity/freshness constraints match.
- Offline policy may fail closed by expiry/cancellation; it may not default to
  approval. Unattended execution must be expressed before the run as policy that
  removes a checkpoint or pre-authorizes a bounded provider scope.

### 8.2 Denial and rejection

- Provider `deny` answers only that provider interaction. It does not reject the
  candidate or cancel the run. Provider completion determines the attempt outcome.
- Checkpoint `reject` preserves the candidate and rejects the visit. It does not
  deny a provider request that may already have completed.
- Workflow-control or delivery `deny` preserves the proposal/effect intent without
  applying or dispatching it.
- `request_changes` is not denial. It closes the candidate request, retains feedback
  and drafts, and creates a new attempt/revision under the checkpoint contract.

### 8.3 Cancellation

Cancelling an approval request closes only the request. Cancelling a run or attempt
is a separate idempotent control that also cancels its pending requests with causal
links. It then follows owned-process and workspace reconciliation. Cancellation
does not revoke an already committed checkpoint, undo a commit, retract an observed
delivery effect, or overwrite a provider response.

## 9. Dashboard and CLI presentation

All clients render a common approval projection. They must not place unlike
requests behind one unlabeled **Approve** action.

Each card/row shows:

- a prominent class badge: **Workflow checkpoint**, **Workflow control**,
  **Provider permission**, or **External delivery**;
- exact subject and consequence in plain language;
- actor/role eligibility, creation time, expiry, and offline/wait status;
- canonical scope summary with an expandable exact representation/digest;
- policy source/digest and why manual action is required;
- class-specific buttons only; and
- committed actor, decision, comment, timestamps, and downstream-effect status.

Provider cards additionally show attempt/thread, command argument array or
canonical file/network/tool target, `allow once` versus the exact session bound,
and this warning: **Allows this provider interaction only; does not approve the
workflow result.**

Checkpoint cards show candidate revision/digest, artifact/diff/validation evidence,
and descendant invalidation impact for requested changes. Delivery cards show the
connector/account, destination, commit or payload digest, and the warning:
**Authorization is recorded; delivery completes only after external verification.**

Dashboard, CLI, and API consume the same `allowedActions`. Clients never enable an
action by guessing from status text. Bulk approval is outside MVP because it would
hide per-request scope and idempotency.

## 10. Audit records

Audit is append-only. At minimum, request, decision, downstream dispatch/response,
observation, expiry, cancellation, grant creation/use/revocation, and reconciliation
are separate events linked by stable IDs.

Every decision event records:

- request ID/class, subject IDs, and aggregate revision;
- actor identity, authentication/session identity, and authorization role;
- action, action-key hash, canonical payload digest, comment, and timestamp;
- scope digest, policy digest, provider capability fingerprint when applicable;
- previous/new request state and causal control/event ID;
- session-grant ID and bound ceiling when applicable; and
- outcome code plus downstream operation/provider-response ID, without claiming its
  completion.

Sensitive command arguments, paths, URLs, headers, secrets, and provider payloads
use the normal redaction/evidence policy. Redaction must retain a stable digest and
safe structural summary so the authorization remains attributable without leaking
credentials.

## 11. Deterministic errors

| Code | Meaning |
|---|---|
| `APPROVAL_NOT_FOUND` | Request ID is unknown. |
| `APPROVAL_CLASS_INVALID` | Request/interaction type is not accepted by this API or action is illegal for the class/mode. |
| `APPROVAL_ACTOR_FORBIDDEN` | Authenticated actor is not eligible. |
| `APPROVAL_SCOPE_MISMATCH` | Submitted scope differs from the immutable request scope. |
| `APPROVAL_POLICY_STALE` | Frozen/current policy digest does not permit the decision. |
| `APPROVAL_REVISION_CONFLICT` | Expected aggregate revision is stale. |
| `APPROVAL_EXPIRED` | Request expired before the decision committed. |
| `APPROVAL_ALREADY_RESOLVED` | A new decision targets a terminal request. |
| `APPROVAL_IDEMPOTENCY_CONFLICT` | Same action key was reused with a different canonical payload. |
| `APPROVAL_SESSION_GRANT_INVALID` | Grant is too broad, stale, expired, revoked, or bound to another attempt/thread. |
| `APPROVAL_DOWNSTREAM_UNCERTAIN` | Decision committed but exact provider/external effect cannot yet be proven. |

Errors are stable API codes. Safe provider details and authority observations are
retained as evidence but do not replace the DARKSTAR classification.

## 12. Security invariants and rejected alternatives

1. Provider responses never call workflow checkpoint/control or delivery reducers.
2. Workflow approval never mutates an attempt permission manifest.
3. Effective permission never exceeds the frozen policy intersection.
4. Every approved scope is immutable and content-addressed.
5. Actor identity is server-derived; actor eligibility is checked at commit time.
6. Every downstream effect has a separate idempotency key and commit observation.
7. Silence, expiry, disconnect, and uncertainty fail closed.

Rejected alternatives:

- **One generic untyped Approve button:** rejected because the same gesture could
  cross trust boundaries and clients would have to infer semantics.
- **Treat provider approval as checkpoint approval:** rejected because it lets an
  execution transport control workflow policy and candidate acceptance.
- **Let a workflow checkpoint broaden provider access:** rejected because artifact
  quality acceptance says nothing about command/file/network authority.
- **Reuse provider “session” approval globally:** rejected because provider session
  meanings vary and cannot satisfy DARKSTAR attempt, policy, and scope bounds.
- **Default allow while offline or on timeout:** rejected because absence of an
  actor is not authorization.
- **Mark delivery complete when approved:** rejected because authorization is not
  authority-specific evidence of an external effect.
- **Retry a provider response or delivery under uncertainty without read-back:**
  rejected because duplicate effects and contradictory responses are possible.

## 13. Executable acceptance contract

[`approval-scenarios.json`](../../../examples/approvals/approval-scenarios.json)
contains positive, negative, offline, expiry, session-grant, and idempotency cases.
The dependency-free reference evaluator and tests run with:

```powershell
node scripts/approval-reference.mjs examples/approvals/approval-scenarios.json
node --test tests/approval-reference.test.mjs
```

The suite fails if a cross-class action succeeds, a stale scope/policy/actor is
accepted, a session grant escapes its attempt/thread ceiling, an offline request
auto-approves, a duplicate action emits a second effect, or the documented case
catalog is not exercised.

## 14. Acceptance mapping

| DS-005 requirement | Evidence |
|---|---|
| Approval classes and actors | Sections 3–4 |
| Scope and expiration | Sections 4–6 |
| Session grants and restart behavior | Section 7 |
| Offline, denial, and cancellation | Section 8 |
| Dashboard presentation | Section 9 |
| Audit records | Section 10 |
| Approval transition table | Section 6 |
| Negative and idempotency cases | Section 13 and executable scenario suite |
| Provider approval cannot satisfy workflow policy or broaden permissions | Sections 1–3 and security invariants |
| Every approval has one idempotent API transition | Section 5 and duplicate-key scenarios |
