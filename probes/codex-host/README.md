# Codex host probes

This directory contains the Windows evidence harness for DAR-5 / DS-001. The
fixtures are grouped by the exact `codex-cli` version that emitted them.

## Run

Use the desktop-bundled executable explicitly when more than one Codex CLI is
on `PATH`:

```powershell
$codex = 'C:\Users\you\AppData\Local\OpenAI\Codex\bin\<build>\codex.exe'
./probes/codex-host/Invoke-CodexAppServerProbe.ps1 `
  -Scenario handshake `
  -CodexExe $codex

./probes/codex-host/Invoke-CodexAppServerProbe.ps1 `
  -Scenario read-only `
  -CodexExe $codex

./probes/codex-host/Invoke-CodexAppServerProbe.ps1 `
  -Scenario write-approval `
  -Workspace ./probes/codex-host/workspaces/write-probe `
  -CodexExe $codex

./probes/codex-host/Invoke-CodexExecProbe.ps1 `
  -Scenario read-only `
  -CodexExe $codex
```

App Server scenarios are `handshake`, `read-only`, `resume`,
`write-approval`, `interrupt`, `process-kill`, `image-skill`, and `user-input`.
Resume requires `-ResumeThreadId`; image/skill accepts `-ImagePath` and
`-SkillPath`. Exec scenarios are `read-only`, `resume`, and `process-kill`, with
`-ResumeSessionId` used for resume.

The App Server `read-only` scenario performs this sequence over stdio JSON-RPC:

1. `initialize` / `initialized`
2. `account/read`
3. `thread/start` with read-only sandbox and approval policy `never`
4. `turn/start` with a JSON output schema
5. streaming notifications through `turn/completed`
6. `thread/unsubscribe` before normal shutdown

The write probe is deliberately guarded: file changes are accepted only in the
disposable nested Git workspace, and command/network approval handlers accept
only the probe's exact marker path or `https://example.com` request.

Fixtures redact user-profile names and email addresses. They must still be
reviewed before publication because provider output and future protocol fields
are untrusted.

Validate all captured fixtures with:

```powershell
./probes/codex-host/Test-CodexHostFixtures.ps1
```

## Evidence rules

- Never overwrite a fixture from a different Codex version.
- Keep the JSONL stream and its manifest together.
- Record failures; a failed fixture is compatibility evidence.
- Do not normalize away unknown events or fields.
- Do not capture credentials, tokens, environment dumps, or unredacted prompts
  containing private source.
- Preserve expected failures with a failure class; they are compatibility and
  recovery evidence, not fixtures to delete.
