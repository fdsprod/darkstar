[CmdletBinding()]
param(
    [ValidateSet('read-only', 'resume', 'process-kill')]
    [string]$Scenario = 'read-only',

    [string]$CodexExe,

    [string]$Workspace = (Get-Location).Path,

    [string]$ResumeSessionId,

    [ValidatePattern('^[a-z0-9-]+$')]
    [string]$FixtureLabel,

    [string]$FixtureRoot = (Join-Path $PSScriptRoot 'fixtures'),

    [int]$TimeoutSeconds = 180
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function Resolve-CodexExecutable {
    param([string]$RequestedPath)
    if ($RequestedPath) {
        return (Resolve-Path -LiteralPath $RequestedPath).Path
    }
    $desktopBin = Join-Path $env:LOCALAPPDATA 'OpenAI\Codex\bin'
    if (Test-Path -LiteralPath $desktopBin) {
        $candidate = Get-ChildItem -LiteralPath $desktopBin -Filter codex.exe -Recurse -File |
            Sort-Object LastWriteTimeUtc -Descending |
            Select-Object -First 1
        if ($candidate) {
            return $candidate.FullName
        }
    }
    return (Get-Command codex -CommandType Application -ErrorAction Stop | Select-Object -First 1).Source
}

function Get-CodexVersion {
    param([string]$Executable)
    $versionText = (& $Executable --version 2>&1 | Out-String).Trim()
    if ($LASTEXITCODE -ne 0 -or $versionText -notmatch 'codex-cli\s+(?<version>\S+)') {
        throw "Could not determine Codex version: $versionText"
    }
    return $Matches.version
}

function Protect-Value {
    param($Value)
    if ($null -eq $Value) { return $null }
    if ($Value -is [string]) {
        $protected = $Value -replace '(?i)[a-z]:(?:\\+|/+)Users(?:\\+|/+)[^\\/\s"]+', 'C:\Users\<user>'
        $protected = $protected -replace '(?i)\b[A-Z0-9._%+-]+@[A-Z0-9.-]+\.[A-Z]{2,}\b', '<redacted-email>'
        return $protected
    }
    if ($Value -is [System.Collections.IDictionary]) {
        $copy = [ordered]@{}
        foreach ($key in $Value.Keys) { $copy[$key] = Protect-Value $Value[$key] }
        return $copy
    }
    if ($Value -is [System.Collections.IEnumerable] -and $Value -isnot [string]) {
        return @($Value | ForEach-Object { Protect-Value $_ })
    }
    if ($Value -is [pscustomobject]) {
        $copy = [ordered]@{}
        foreach ($property in $Value.PSObject.Properties) {
            $copy[$property.Name] = Protect-Value $property.Value
        }
        return $copy
    }
    return $Value
}

function Get-OptionalPropertyValue {
    param($Object, [string]$Name)
    if ($null -eq $Object) { return $null }
    $property = $Object.PSObject.Properties[$Name]
    if ($null -eq $property) { return $null }
    return $property.Value
}

$resolvedCodexExe = Resolve-CodexExecutable $CodexExe
$codexVersion = Get-CodexVersion $resolvedCodexExe
$resolvedWorkspace = (Resolve-Path -LiteralPath $Workspace).Path
$schemaPath = (Resolve-Path -LiteralPath (Join-Path $PSScriptRoot 'assets\exec-result.schema.json')).Path
$fixtureDirectory = Join-Path $FixtureRoot $codexVersion
New-Item -ItemType Directory -Path $fixtureDirectory -Force | Out-Null

$fixtureStem = if ($FixtureLabel) { "exec-$FixtureLabel" } else { "exec-$Scenario" }
$fixturePath = Join-Path $fixtureDirectory "$fixtureStem.jsonl"
$manifestPath = Join-Path $fixtureDirectory "$fixtureStem.manifest.json"
$stderrPath = Join-Path $fixtureDirectory "$fixtureStem.stderr.log"
Set-Content -LiteralPath $fixturePath -Value '' -NoNewline -Encoding utf8
Set-Content -LiteralPath $stderrPath -Value '' -NoNewline -Encoding utf8

if ($Scenario -eq 'resume' -and -not $ResumeSessionId) {
    $sourceManifestPath = Join-Path $fixtureDirectory 'exec-read-only.manifest.json'
    if (-not (Test-Path -LiteralPath $sourceManifestPath)) {
        throw 'ResumeSessionId was not supplied and no exec read-only manifest exists.'
    }
    $ResumeSessionId = (Get-Content -Raw -LiteralPath $sourceManifestPath | ConvertFrom-Json).sessionId
}

$prompt = if ($Scenario -eq 'read-only') {
    'Read only: inspect the workspace root without modifying anything. Return probe exec-read-only, success true, and a short detail naming at least one top-level Markdown file.'
}
elseif ($Scenario -eq 'process-kill') {
    'Run one interruptible wait for 60 seconds before replying. Do not perform any other work.'
}
else {
    'This is a resumed exec session. Do not modify files. Return probe exec-resume, success true, and detail resumed-session.'
}

$startInfo = [System.Diagnostics.ProcessStartInfo]::new()
$startInfo.FileName = $resolvedCodexExe
$startInfo.UseShellExecute = $false
$startInfo.RedirectStandardOutput = $true
$startInfo.RedirectStandardError = $true
$startInfo.CreateNoWindow = $true
$startInfo.WorkingDirectory = $resolvedWorkspace
$startInfo.ArgumentList.Add('exec')
if ($Scenario -eq 'resume') {
    $startInfo.ArgumentList.Add('resume')
    $startInfo.ArgumentList.Add('--json')
    $startInfo.ArgumentList.Add('--skip-git-repo-check')
    $startInfo.ArgumentList.Add('--output-schema')
    $startInfo.ArgumentList.Add($schemaPath)
    $startInfo.ArgumentList.Add($ResumeSessionId)
    $startInfo.ArgumentList.Add($prompt)
}
else {
    foreach ($argument in @(
        '--json',
        '--skip-git-repo-check',
        '--sandbox', 'read-only',
        '--config', 'approval_policy="never"',
        '--output-schema', $schemaPath,
        '--cd', $resolvedWorkspace,
        $prompt
    )) {
        $startInfo.ArgumentList.Add($argument)
    }
}

$process = [System.Diagnostics.Process]::new()
$process.StartInfo = $startInfo
$startedAt = [DateTimeOffset]::UtcNow
if (-not $process.Start()) { throw 'Failed to start codex exec.' }
$stderrTask = $process.StandardError.ReadToEndAsync()

$sequence = 0
$events = [System.Collections.Generic.List[object]]::new()
$processKilled = $false
while (-not $process.StandardOutput.EndOfStream) {
    if ([DateTimeOffset]::UtcNow -gt $startedAt.AddSeconds($TimeoutSeconds)) {
        $process.Kill($true)
        throw "codex exec exceeded $TimeoutSeconds seconds."
    }
    $line = $process.StandardOutput.ReadLine()
    if (-not $line) { continue }
    try {
        $message = $line | ConvertFrom-Json -Depth 100
    }
    catch {
        $message = [ordered]@{ type = 'non_json_output'; text = $line }
    }
    $events.Add($message)
    $sequence += 1
    $record = [ordered]@{
        fixtureSchemaVersion = 1
        sequence = $sequence
        direction = 'provider-to-host'
        message = Protect-Value $message
    }
    Add-Content -LiteralPath $fixturePath -Value ($record | ConvertTo-Json -Depth 100 -Compress) -Encoding utf8
    if ($Scenario -eq 'process-kill' -and (Get-OptionalPropertyValue $message 'type') -eq 'turn.started') {
        $process.Kill($true)
        $processKilled = $true
        break
    }
}
$process.WaitForExit()
$stderr = $stderrTask.GetAwaiter().GetResult()
if ($stderr) {
    Set-Content -LiteralPath $stderrPath -Value (Protect-Value $stderr) -Encoding utf8
}

$eventTypes = @($events | ForEach-Object { Get-OptionalPropertyValue $_ 'type' } | Where-Object { $_ })
$threadStarted = $events | Where-Object { (Get-OptionalPropertyValue $_ 'type') -eq 'thread.started' } | Select-Object -First 1
$sessionId = Get-OptionalPropertyValue $threadStarted 'thread_id'
if (-not $sessionId) {
    $sessionId = Get-OptionalPropertyValue $threadStarted 'threadId'
}
if (-not $processKilled -and $process.ExitCode -ne 0) {
    throw "codex exec exited with $($process.ExitCode)."
}
if ($eventTypes -notcontains 'thread.started' -or (-not $processKilled -and $eventTypes -notcontains 'turn.completed')) {
    throw 'codex exec did not emit the required thread.started and turn.completed boundaries.'
}
if ($Scenario -eq 'resume' -and $sessionId -ne $ResumeSessionId) {
    throw "exec resume returned session '$sessionId' instead of '$ResumeSessionId'."
}

if (-not $processKilled) {
    $finalEvent = $events | Where-Object {
        (Get-OptionalPropertyValue $_ 'type') -eq 'item.completed' -and
        (Get-OptionalPropertyValue (Get-OptionalPropertyValue $_ 'item') 'type') -in @('agent_message', 'agentMessage')
    } | Select-Object -Last 1
    $finalText = Get-OptionalPropertyValue (Get-OptionalPropertyValue $finalEvent 'item') 'text'
    $structured = $finalText | ConvertFrom-Json
    if ($structured.success -ne $true) {
        throw 'codex exec structured result did not report success.'
    }
}

$manifest = [ordered]@{
    fixtureSchemaVersion = 1
    scenario = $Scenario
    status = 'passed'
    startedAt = $startedAt.ToString('O')
    completedAt = [DateTimeOffset]::UtcNow.ToString('O')
    platform = 'windows'
    codexVersion = $codexVersion
    codexExecutable = Protect-Value $resolvedCodexExe
    workspace = Protect-Value $resolvedWorkspace
    transport = 'exec-jsonl'
    exitCode = $process.ExitCode
    processKilled = $processKilled
    sessionId = $sessionId
    resumedFromSessionId = if ($Scenario -eq 'resume') { $ResumeSessionId } else { $null }
    eventTypes = @($eventTypes | Select-Object -Unique)
    fixture = Split-Path -Leaf $fixturePath
    stderr = Split-Path -Leaf $stderrPath
}
Set-Content -LiteralPath $manifestPath -Value ($manifest | ConvertTo-Json -Depth 30) -Encoding utf8
Write-Output ($manifest | ConvertTo-Json -Depth 30)
