# DARKSTAR capability registry contract

> [Documentation index](../../README.md)

**Status:** Proposed normative contract for DS-007  
**Scope:** Skills, MCP/tools, built-ins, discovery, versioning, policy, resolution, invocation, fallback, and audit

## 1. Decision

DARKSTAR never equates “Codex can see it” with “the workflow may depend on it.”
Every capability used by a run has one namespaced registry record, one provenance
class, a policy decision, an immutable attempt-time fingerprint, and an audit trail.
Automatic Codex discovery is observable input to the registry, not authority to
invoke or a compatibility guarantee.

The four MVP classes are:

| Class | Meaning | May satisfy a required workflow input? | Stability owner |
|---|---|---|---|
| `guaranteed` | Implemented and versioned by the installed DARKSTAR release | Yes | DARKSTAR |
| `registered` | Explicit project/user/admin registration with pinned identity and fingerprint | Yes, after health and policy checks | registrant + adapter |
| `inherited` | Reported by the exact Codex host/environment but not registered with DARKSTAR | No by default; only an explicit `acceptInherited` requirement may opt in | Codex/environment |
| `unsupported_discovery` | Found only by PATH scanning, filesystem guessing, prompt instructions, provider memory, or runtime surprise | No | none |

This classification matches the proven App Server boundary: DS-001 supplied an
exact skill path as a typed `turn/start` input and observed MCP startup status, but
did not prove stable semantic versions for arbitrary discovered skills or tools.

## 2. Names and records

Canonical names are lowercase ASCII `<namespace>:<name>`, where namespaces are:

- `darkstar:` for guaranteed built-ins;
- `project:` for repository-owned explicit registrations;
- `user:` and `admin:` for explicit local policy registrations;
- `plugin:<plugin-id>/` for an installed plugin capability;
- `mcp:<server>/` for an explicitly registered MCP tool; and
- `codex-inherited:<scope>/` for a host observation that has not been promoted.

Names are identifiers, not permission scopes. Duplicate canonical names are a
hard registry conflict; DARKSTAR does not choose by search order. A Codex skill
name collision remains two observations distinguished by canonical path and
scope, and neither silently shadows a registered record.

A registry record contains:

```json
{
  "schemaVersion": 1,
  "id": "capability:project:review-guidance@sha256:...",
  "name": "project:review-guidance",
  "kind": "skill",
  "class": "registered",
  "declaredVersion": "1.2.0",
  "fingerprint": "sha256:...",
  "source": {"type":"skill_path","locator":".agents/skills/review/SKILL.md"},
  "interfaces": {"inputs":"sha256:...","outputs":"sha256:..."},
  "dependencies": [],
  "risk": {"reads":true,"writes":false,"network":false,"externalSideEffect":false},
  "availability": "available",
  "observedAt": "2026-08-31T00:00:00Z"
}
```

`declaredVersion` is optional evidence, never the fingerprint. For a local skill,
the fingerprint hashes normalized `SKILL.md`, referenced files/scripts/assets,
resolved canonical roots, and invocation metadata. For an MCP tool, it hashes the
server registration identity/transport, tool name, description, input/output
schemas, relevant server instructions, and adapter version. Secrets, tokens, and
environment values are excluded and stored only as presence/credential health.

## 3. Discovery boundaries

The Codex host can enumerate skill metadata by working directory and emits
`skills/changed` as an invalidation signal. Official Codex documentation describes
repository, user, admin, and system skill locations, progressive disclosure, name
collisions, enable/disable configuration, and optional tool dependencies. DARKSTAR
records these observations with exact host version and working directory, then
classifies them `inherited` unless a registration independently pins them.

Codex MCP configuration supports STDIO and Streamable HTTP servers, enable/disable,
required startup, allow/deny tool lists, approval modes, and per-tool policy.
DARKSTAR may read normalized availability from its adapter, but it does not parse
or rewrite a user's global `config.toml` as registry management. An explicit
DARKSTAR registration refers to a server alias and tool; the host must prove that
the exact observed fingerprint is present at attempt preparation.

The MVP does not automatically register:

- arbitrary executables or scripts on `PATH`;
- every `SKILL.md` found by recursive repository search;
- MCP servers or tools mentioned in a prompt, skill, or AGENTS.md;
- a tool returned after an attempt manifest freezes;
- provider built-ins with no negotiated descriptor; or
- remote/plugin capabilities merely visible to a different ChatGPT/Codex surface.

Discovery has no network side effects, OAuth login, installation, or skill script
execution. Those require an explicit user/admin operation outside resolution.

## 4. Guaranteed capabilities

An installed DARKSTAR release publishes a signed/packaged manifest of guaranteed
capabilities and their semantic versions. The MVP contract reserves these minimum
interfaces once their implementation stories land:

| Name | Interface promise |
|---|---|
| `darkstar:artifact.read` | read one policy-authorized immutable artifact representation |
| `darkstar:workspace.read` | bounded read within the canonical owned workspace |
| `darkstar:workspace.patch` | propose/apply an auditable patch under provider permission policy |
| `darkstar:command.run` | deterministic owned-process execution with declared cwd/env/time/output bounds |
| `darkstar:output.structured` | validate a result against an attempt-pinned JSON Schema |

“Guaranteed” means available when the relevant DARKSTAR component reports healthy;
it does not bypass workspace, approval, artifact, or threat-model controls.

## 5. Skills

The stable DARKSTAR skill path is explicit supply, not implicit activation:

1. resolve a registered skill and all declared dependencies;
2. canonicalize and hash its closed package below an allowed root;
3. copy or bind the immutable package into the attempt context;
4. pass the exact skill item/path through the proven Codex adapter;
5. record the supplied name, fingerprint, source, and host version; and
6. validate requested structured output independently of skill prose.

Skill instructions are untrusted executable guidance. Referenced scripts are tools
subject to the same command/filesystem/network approval policy as any other tool.
`agents/openai.yaml` tool dependencies are availability hints; a skill cannot
install a connector, authorize a tool, widen sandbox roots, satisfy an approval,
or convert an inherited tool into a registered capability.

Implicit Codex skill matching may be permitted for an exploratory, non-required
node, but it is recorded as inherited behavior and cannot be an acceptance
dependency. Required nodes use typed explicit supply and a pinned fingerprint.

## 6. Tools and MCP

A registered tool capability names one server and one tool. Tool names are kept
namespaced even if Codex presents a flattened alias. Initialization and tool-list
failure produce `unavailable`, not an empty successful registry. Changes to schema,
description, server instructions, transport identity, or host version produce a
new fingerprint and require compatibility review when the tool is required.

Resolution policy is the intersection of:

1. workflow requirement (name, kind, version/fingerprint constraint, required or
   optional, and declared fallbacks);
2. DARKSTAR project/user/admin allow/deny policy;
3. DS-005 provider permission policy and attempt access class;
4. Codex host availability and MCP enabled/disabled tool filters; and
5. live health/credential state without secret disclosure.

The narrowest result wins. An allow at one layer cannot override a deny or absent
capability at another. Codex `approval_mode` is enforced in addition to DARKSTAR
policy and can prompt more often; it cannot broaden DARKSTAR policy. Every tool
request is audited with opaque provider request ID, normalized capability ID,
argument digest/redacted summary, decision, result class, duration, and evidence
reference.

## 7. Resolution and frozen manifests

Resolution is deterministic for `(registry snapshot, requirement set, policy,
host fingerprint)`:

1. reject duplicate canonical records and invalid/unhealthy guaranteed entries;
2. locate exact-name candidates and apply kind/version/fingerprint constraints;
3. remove disabled, denied, unhealthy, or dependency-incomplete candidates;
4. prefer `guaranteed`, then `registered`; consider `inherited` only when the
   requirement explicitly opts in;
5. if no candidate remains, evaluate declared fallbacks in order under the same
   checks—never by similarity;
6. fail a missing required capability before starting an attempt; omit an optional
   capability with an explicit reason; and
7. freeze selected IDs/fingerprints, policy digest, host version, and omissions in
   the attempt capability manifest.

`capabilities/changed`, `skills/changed`, MCP restart, plugin install, or filesystem
change invalidates future resolution only. It never mutates an active attempt. A
resume must use the same manifest digest or become a new attempt.

Stable outcomes are `CAPABILITY_REQUIRED_MISSING`, `CAPABILITY_VERSION_MISMATCH`,
`CAPABILITY_FINGERPRINT_CHANGED`, `CAPABILITY_DEPENDENCY_MISSING`,
`CAPABILITY_POLICY_DENIED`, `CAPABILITY_UNHEALTHY`, `CAPABILITY_AMBIGUOUS`, and
`CAPABILITY_INHERITED_NOT_ALLOWED`.

## 8. Fallback and degradation

A fallback is legal only when the workflow declares an ordered alternative with
an equivalent input/output contract and policy permits it. The selected fallback
is visible in the attempt, event stream, and result. Automatic fallback is
prohibited after a possibly side-effecting invocation, authentication/permission
denial, ambiguous timeout, or changed fingerprint.

| Situation | Required | Optional |
|---|---|---|
| exact registered/guaranteed capability available | select and freeze | select when useful |
| declared equivalent fallback available | select fallback; mark degraded | select or omit by policy |
| only inherited capability exists | fail unless `acceptInherited` | omit unless explicitly allowed |
| unavailable before any invocation | fail before attempt | omit with reason |
| fails after read-only, side-effect-free invocation | retry/fallback only if workflow declares it | omit/fallback and record |
| ambiguous or side-effecting failure | reconcile; no fallback | reconcile; no fallback |

## 9. Audit and acceptance

Audit events cover discovery snapshot, registration/revocation, resolution inputs,
policy decision, manifest freeze, invocation request/response, permission
interaction, fallback, failure, and invalidation. Logs never contain credentials,
full sensitive arguments, or raw tool output; those live in controlled evidence.

[`capability-scenarios.json`](../../../examples/capabilities/capability-scenarios.json),
[`capability-reference.mjs`](../../../scripts/capability-reference.mjs), and the
contract tests demonstrate required/optional resolution, class precedence,
inherited opt-in, deny precedence, fingerprints, declared fallback, and
side-effect ambiguity.

Source evidence: the [official Codex skill documentation](https://learn.chatgpt.com/docs/build-skills)
describes full `SKILL.md` loading after selection, explicit/implicit invocation,
discovery scopes, duplicate names, and declared tool dependencies; the
[official MCP documentation](https://learn.chatgpt.com/docs/extend/mcp?surface=cli)
describes server transports, shared host configuration, required startup, tool
allow/deny, and approval modes. DS-001 fixtures remain the authoritative evidence
for the exact supported App Server version.
