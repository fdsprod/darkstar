# DAR-5 Windows probe matrix

Target host: Windows 11 (`windows-x86_64`)

Tested Codex CLI versions: `0.151.0-alpha.7.1` and
`0.151.0-alpha.7.2`. The completed decision corpus is under the `.7.2`
fixture directory; `.7.1` supplies the prior-version handshake/read-only
baseline and the legacy writer-recovery case.

| Capability | App Server | `exec --json` | Evidence / conclusion |
|---|---|---|---|
| Exact executable and version | Pass | Pass | Every manifest records the pinned desktop executable and exact version. A different npm CLI appears earlier on the normal user `PATH`, so executable identity is part of health. |
| Authentication health | Pass | Indirect | App Server `account/read` returned configured ChatGPT auth with email redacted. Exec completed authenticated turns. |
| Initialize/platform negotiation | Pass | N/A | `initialize` returned Windows platform and the exact version in its user agent. |
| Streaming and correlation | Pass | Pass, reduced | App Server interleaved correlated JSON-RPC responses, notifications, and server requests. Exec emitted ordered JSONL lifecycle/items but no bidirectional request bridge. |
| Structured read-only turn | Pass | Pass | Both transports produced schema-valid structured output and terminal completion. |
| Write-capable turn | Pass | Not required for fallback | App Server performed an approved mutation in a disposable Git workspace and produced the exact `DARKSTAR_WRITE_OK` marker. Exec is restricted to bounded scenarios by policy. |
| Command/file/network approvals | Pass | No interactive bridge proved | App Server captured two command approvals and one network approval, with guarded request handling. This disqualifies exec as the primary write transport. |
| User input request | Pass | No interactive bridge proved | App Server received and answered exactly one `item/tool/requestUserInput` request. |
| Skills and images | Pass | Not required for fallback | App Server used the deterministic `DARKSTAR_SKILL_001` skill and emitted an `imageView` item for the local red-square asset. |
| Usage and rate-limit signals | Pass | Terminal result only | App Server emitted token-usage and account rate-limit updates. Live account exhaustion was deliberately not induced; generated error schemas and later synthetic adapter tests cover exhaustion classification. |
| Fresh-process completed-thread resume | Pass with release rule | Pass | Both transports preserved thread/session identity. A normal App Server owner must send `thread/unsubscribe` before shutdown. |
| Graceful cancellation | Pass | Process-level only | App Server `turn/interrupt` reached terminal `interrupted` and the thread subsequently resumed. |
| Forced process interruption | Pass | Pass | Killing the owned host after turn start left durable identity; a fresh process resumed both transports successfully. |
| Legacy unreleased writer | Expected failure | N/A | A `.7.1` thread captured before unsubscribe support failed resume on `.7.2` with `already has an active writer`. Recovery must pause/reconcile rather than start a duplicate turn. |
| Version drift | Schema-stable in sample | CLI flag drift observed | Generated `.7.1` and `.7.2` App Server schemas were byte-identical across 413 files, while exec removed/rejected `--ask-for-approval`. Schema diff alone is insufficient. |

## Decision

Use App Server over stdio as the primary Codex transport. It is the only tested
surface that satisfies the full Windows read/write, approval, user-input,
image/skill, usage, interruption, and recovery contract.

Keep `codex exec --json` as a visible, version-gated fallback for bounded nodes
that need structured output, streaming, resume, and process recovery but do not
need an interactive approval/input bridge. Never silently fall back after an
authentication failure, permission decision, usage exhaustion, or uncertain
write outcome.

## Evidence index

- passing and expected-failure transcripts:
  `fixtures/0.151.0-alpha.7.2/`
- prior-version baseline:
  `fixtures/0.151.0-alpha.7.1/`
- generated protocol schemas:
  `generated/0.151.0-alpha.7.1/schema/` and
  `generated/0.151.0-alpha.7.2/schema/`
- conformance validator: `Test-CodexHostFixtures.ps1`
