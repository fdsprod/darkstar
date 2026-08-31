[CmdletBinding()]
param(
    [ValidateSet('handshake', 'read-only', 'resume', 'write-approval', 'interrupt', 'process-kill', 'image-skill', 'user-input')]
    [string]$Scenario = 'handshake',

    [string]$CodexExe,

    [string]$Workspace = (Get-Location).Path,

    [string]$FixtureRoot = (Join-Path $PSScriptRoot 'fixtures'),

    [string]$ResumeThreadId,

    [string]$ImagePath,

    [string]$SkillPath,

    [ValidatePattern('^[a-z0-9-]+$')]
    [string]$FixtureLabel,

    [int]$ResponseTimeoutSeconds = 30,

    [int]$TurnTimeoutSeconds = 180
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
        $desktopCandidates = Get-ChildItem -LiteralPath $desktopBin -Filter codex.exe -Recurse -File |
            Sort-Object LastWriteTimeUtc -Descending
        if ($desktopCandidates.Count -gt 0) {
            return $desktopCandidates[0].FullName
        }
    }

    $command = Get-Command codex -CommandType Application -ErrorAction Stop |
        Select-Object -First 1
    return $command.Source
}

function Get-CodexVersion {
    param([string]$Executable)

    $versionText = (& $Executable --version 2>&1 | Out-String).Trim()
    if ($LASTEXITCODE -ne 0) {
        throw "codex --version failed with exit code $LASTEXITCODE`: $versionText"
    }
    if ($versionText -notmatch 'codex-cli\s+(?<version>\S+)') {
        throw "Could not parse Codex version from: $versionText"
    }
    return $Matches.version
}

function Protect-Value {
    param($Value)

    if ($null -eq $Value) {
        return $null
    }
    if ($Value -is [string]) {
        $protected = $Value -replace '(?i)[a-z]:(?:\\+|/+)Users(?:\\+|/+)[^\\/\s"]+', 'C:\Users\<user>'
        $protected = $protected -replace '(?i)\b[A-Z0-9._%+-]+@[A-Z0-9.-]+\.[A-Z]{2,}\b', '<redacted-email>'
        return $protected
    }
    if ($Value -is [System.Collections.IDictionary]) {
        $copy = [ordered]@{}
        foreach ($key in $Value.Keys) {
            $copy[$key] = if ($key -eq 'serverName') {
                '<redacted-device>'
            }
            elseif ($key -eq 'installationId') {
                '<redacted-installation-id>'
            }
            else {
                Protect-Value $Value[$key]
            }
        }
        return $copy
    }
    if ($Value -is [System.Collections.IEnumerable] -and $Value -isnot [string]) {
        return @($Value | ForEach-Object { Protect-Value $_ })
    }
    if ($Value -is [pscustomobject]) {
        $copy = [ordered]@{}
        foreach ($property in $Value.PSObject.Properties) {
            $copy[$property.Name] = if ($property.Name -eq 'serverName') {
                '<redacted-device>'
            }
            elseif ($property.Name -eq 'installationId') {
                '<redacted-installation-id>'
            }
            else {
                Protect-Value $property.Value
            }
        }
        return $copy
    }
    return $Value
}

$resolvedCodexExe = Resolve-CodexExecutable $CodexExe
$codexVersion = Get-CodexVersion $resolvedCodexExe
$resolvedWorkspace = (Resolve-Path -LiteralPath $Workspace).Path
$fixtureDirectory = Join-Path $FixtureRoot $codexVersion
New-Item -ItemType Directory -Path $fixtureDirectory -Force | Out-Null

$fixtureStem = if ($FixtureLabel) { "app-server-$FixtureLabel" } else { "app-server-$Scenario" }
$fixturePath = Join-Path $fixtureDirectory "$fixtureStem.jsonl"
$manifestPath = Join-Path $fixtureDirectory "$fixtureStem.manifest.json"
$stderrPath = Join-Path $fixtureDirectory "$fixtureStem.stderr.log"

Set-Content -LiteralPath $fixturePath -Value '' -NoNewline -Encoding utf8
Set-Content -LiteralPath $stderrPath -Value '' -NoNewline -Encoding utf8

$sequence = 0
$approvalRequestCount = 0
$networkApprovalRequestCount = 0
$userInputRequestCount = 0
$handledServerRequestMethods = [System.Collections.Generic.List[string]]::new()
function Write-TranscriptRecord {
    param(
        [ValidateSet('client-to-server', 'server-to-client')]
        [string]$Direction,
        $Message
    )

    $script:sequence += 1
    $record = [ordered]@{
        fixtureSchemaVersion = 1
        sequence = $script:sequence
        direction = $Direction
        message = Protect-Value $Message
    }
    Add-Content -LiteralPath $fixturePath -Value ($record | ConvertTo-Json -Depth 100 -Compress) -Encoding utf8
}

$startInfo = [System.Diagnostics.ProcessStartInfo]::new()
$startInfo.FileName = $resolvedCodexExe
$startInfo.ArgumentList.Add('app-server')
$startInfo.ArgumentList.Add('--stdio')
$startInfo.UseShellExecute = $false
$startInfo.RedirectStandardInput = $true
$startInfo.RedirectStandardOutput = $true
$startInfo.RedirectStandardError = $true
$startInfo.CreateNoWindow = $true
$startInfo.WorkingDirectory = $resolvedWorkspace

$process = [System.Diagnostics.Process]::new()
$process.StartInfo = $startInfo
$process.EnableRaisingEvents = $true

if (-not $process.Start()) {
    throw 'Failed to start Codex App Server.'
}

$stderrTask = $process.StandardError.ReadToEndAsync()

function Send-Message {
    param($Message)

    $line = $Message | ConvertTo-Json -Depth 100 -Compress
    Write-TranscriptRecord -Direction client-to-server -Message $Message
    $process.StandardInput.WriteLine($line)
    $process.StandardInput.Flush()
}

function Get-OptionalPropertyValue {
    param(
        $Object,
        [string]$Name
    )
    if ($null -eq $Object) {
        return $null
    }
    $property = $Object.PSObject.Properties[$Name]
    if ($null -eq $property) {
        return $null
    }
    return $property.Value
}

function Handle-ServerRequest {
    param($Message)

    $method = Get-OptionalPropertyValue $Message 'method'
    $requestId = Get-OptionalPropertyValue $Message 'id'
    if ($null -eq $method -or $null -eq $requestId) {
        return $false
    }

    switch ($method) {
        'item/commandExecution/requestApproval' {
            if ($Scenario -ne 'write-approval') {
                throw "Refusing unexpected command approval during '$Scenario'."
            }
            $command = [string](Get-OptionalPropertyValue $Message.params 'command')
            $reason = [string](Get-OptionalPropertyValue $Message.params 'reason')
            $safeWrite = $command -match '(?i)probe-write\.txt'
            $safeNetwork = $command -match '(?i)https://example\.com' -or $reason -match '(?i)example\.com'
            if (-not $safeWrite -and -not $safeNetwork) {
                throw "Refusing an out-of-scope command approval: $command"
            }
            $script:approvalRequestCount += 1
            if ($safeNetwork) {
                $script:networkApprovalRequestCount += 1
            }
            $script:handledServerRequestMethods.Add($method)
            Send-Message ([ordered]@{ id = $requestId; result = @{ decision = 'accept' } })
            return $true
        }
        'item/fileChange/requestApproval' {
            if ($Scenario -ne 'write-approval') {
                throw "Refusing unexpected file-change approval during '$Scenario'."
            }
            if ($resolvedWorkspace -notmatch '(?i)codex-host[\\/]workspaces[\\/]write-probe$') {
                throw "Refusing file-change approval outside the disposable write-probe workspace: $resolvedWorkspace"
            }
            $script:approvalRequestCount += 1
            $script:handledServerRequestMethods.Add($method)
            Send-Message ([ordered]@{ id = $requestId; result = @{ decision = 'accept' } })
            return $true
        }
        'item/permissions/requestApproval' {
            if ($Scenario -ne 'write-approval') {
                throw "Refusing unexpected permission request during '$Scenario'."
            }
            $requestedNetwork = Get-OptionalPropertyValue $Message.params.permissions 'network'
            $requestedFileSystem = Get-OptionalPropertyValue $Message.params.permissions 'fileSystem'
            if ($null -eq $requestedNetwork -or $requestedNetwork.enabled -ne $true -or $null -ne $requestedFileSystem) {
                throw 'Refusing a permission request that is not a network-only grant.'
            }
            $script:approvalRequestCount += 1
            $script:networkApprovalRequestCount += 1
            $script:handledServerRequestMethods.Add($method)
            Send-Message ([ordered]@{
                id = $requestId
                result = [ordered]@{
                    permissions = @{ network = @{ enabled = $true } }
                    scope = 'turn'
                    strictAutoReview = $false
                }
            })
            return $true
        }
        'item/tool/requestUserInput' {
            if ($Scenario -ne 'user-input') {
                throw "Refusing unexpected user-input request during '$Scenario'."
            }
            $answers = [ordered]@{}
            foreach ($question in $Message.params.questions) {
                $answers[$question.id] = @{ answers = @('Continue') }
            }
            $script:userInputRequestCount += 1
            $script:handledServerRequestMethods.Add($method)
            Send-Message ([ordered]@{ id = $requestId; result = @{ answers = $answers } })
            return $true
        }
        default {
            return $false
        }
    }
}

function Read-Message {
    param([int]$TimeoutSeconds)

    $readTask = $process.StandardOutput.ReadLineAsync()
    if (-not $readTask.Wait([TimeSpan]::FromSeconds($TimeoutSeconds))) {
        throw "Timed out after $TimeoutSeconds seconds waiting for an App Server message."
    }
    $line = $readTask.Result
    if ($null -eq $line) {
        throw "App Server stdout closed unexpectedly (exit code: $($process.ExitCode))."
    }
    $message = $line | ConvertFrom-Json -Depth 100
    Write-TranscriptRecord -Direction server-to-client -Message $message
    return $message
}

function Wait-ForResponse {
    param(
        $Id,
        [int]$TimeoutSeconds
    )

    $deadline = [DateTimeOffset]::UtcNow.AddSeconds($TimeoutSeconds)
    while ([DateTimeOffset]::UtcNow -lt $deadline) {
        $remaining = [Math]::Max(1, [int]($deadline - [DateTimeOffset]::UtcNow).TotalSeconds)
        $message = Read-Message -TimeoutSeconds $remaining
        if ($null -ne $message.PSObject.Properties['id'] -and "$($message.id)" -eq "$Id" -and $null -eq $message.PSObject.Properties['method']) {
            if ($null -ne $message.PSObject.Properties['error']) {
                throw "App Server request $Id failed: $($message.error | ConvertTo-Json -Depth 20 -Compress)"
            }
            return $message
        }
        if ($null -ne $message.PSObject.Properties['id'] -and $null -ne $message.PSObject.Properties['method']) {
            if (-not (Handle-ServerRequest $message)) {
                throw "Unexpected App Server request '$($message.method)' while waiting for response $Id."
            }
        }
    }
    throw "Timed out waiting for App Server response $Id."
}

function Wait-ForNotification {
    param(
        [string]$Method,
        [int]$TimeoutSeconds
    )

    $deadline = [DateTimeOffset]::UtcNow.AddSeconds($TimeoutSeconds)
    while ([DateTimeOffset]::UtcNow -lt $deadline) {
        $remaining = [Math]::Max(1, [int]($deadline - [DateTimeOffset]::UtcNow).TotalSeconds)
        $message = Read-Message -TimeoutSeconds $remaining
        if ((Get-OptionalPropertyValue $message 'method') -eq $Method -and $null -eq (Get-OptionalPropertyValue $message 'id')) {
            return $message
        }
        if ($null -ne $message.PSObject.Properties['id'] -and $null -ne $message.PSObject.Properties['method']) {
            if (-not (Handle-ServerRequest $message)) {
                throw "Unexpected App Server request '$($message.method)' while waiting for '$Method'."
            }
        }
    }
    throw "Timed out waiting for App Server notification '$Method'."
}

$startedAt = [DateTimeOffset]::UtcNow
$threadId = $null
$turnId = $null
$unsubscribeStatus = $null
$processKilled = $false
$status = 'failed'
$failure = $null

try {
    Send-Message ([ordered]@{
        id = 1
        method = 'initialize'
        params = [ordered]@{
            clientInfo = [ordered]@{
                name = 'darkstar-codex-host-probe'
                title = 'DARKSTAR Codex host probe'
                version = '0.1.0'
            }
            capabilities = [ordered]@{
                experimentalApi = $true
            }
        }
    })
    $initialize = Wait-ForResponse -Id 1 -TimeoutSeconds $ResponseTimeoutSeconds

    Send-Message ([ordered]@{ method = 'initialized'; params = @{} })

    Send-Message ([ordered]@{ id = 2; method = 'account/read'; params = @{ refreshToken = $false } })
    $account = Wait-ForResponse -Id 2 -TimeoutSeconds $ResponseTimeoutSeconds

    switch ($Scenario) {
        'read-only' {
            Send-Message ([ordered]@{
                id = 3
                method = 'thread/start'
                params = [ordered]@{
                    cwd = $resolvedWorkspace
                    ephemeral = $false
                    sandbox = 'read-only'
                    approvalPolicy = 'never'
                    threadSource = 'darkstar-probe'
                }
            })
            $threadStart = Wait-ForResponse -Id 3 -TimeoutSeconds $ResponseTimeoutSeconds
            $threadId = $threadStart.result.thread.id
            if (-not $threadId) {
                throw 'thread/start did not return result.thread.id.'
            }

            $outputSchema = [ordered]@{
                type = 'object'
                additionalProperties = $false
                properties = [ordered]@{
                    probe = @{ type = 'string'; const = 'dar-5' }
                    cwdReadable = @{ type = 'boolean' }
                    observedMarkdownFiles = @{ type = 'array'; items = @{ type = 'string' } }
                }
                required = @('probe', 'cwdReadable', 'observedMarkdownFiles')
            }
            Send-Message ([ordered]@{
                id = 4
                method = 'turn/start'
                params = [ordered]@{
                    threadId = $threadId
                    input = @([ordered]@{
                        type = 'text'
                        text = 'Read only: inspect the workspace root without modifying anything. Return the requested structured result. Set probe to dar-5, cwdReadable to whether the root was readable, and observedMarkdownFiles to the sorted names of top-level Markdown files.'
                    })
                    outputSchema = $outputSchema
                }
            })
            $turnStart = Wait-ForResponse -Id 4 -TimeoutSeconds $ResponseTimeoutSeconds
            $turnId = $turnStart.result.turn.id
            $turnCompleted = Wait-ForNotification -Method 'turn/completed' -TimeoutSeconds $TurnTimeoutSeconds
            if ($turnCompleted.params.turn.status -ne 'completed') {
                throw "Turn ended with status '$($turnCompleted.params.turn.status)'."
            }
        }
        'resume' {
            if (-not $ResumeThreadId) {
                $readOnlyManifestPath = Join-Path $fixtureDirectory 'app-server-read-only.manifest.json'
                if (-not (Test-Path -LiteralPath $readOnlyManifestPath)) {
                    throw 'ResumeThreadId was not supplied and no read-only fixture manifest exists.'
                }
                $ResumeThreadId = (Get-Content -Raw -LiteralPath $readOnlyManifestPath | ConvertFrom-Json).threadId
            }
            Send-Message ([ordered]@{
                id = 3
                method = 'thread/resume'
                params = [ordered]@{
                    threadId = $ResumeThreadId
                    excludeTurns = $false
                }
            })
            $threadResume = Wait-ForResponse -Id 3 -TimeoutSeconds $ResponseTimeoutSeconds
            $threadId = $threadResume.result.thread.id
            if ($threadId -ne $ResumeThreadId) {
                throw "thread/resume returned '$threadId' instead of '$ResumeThreadId'."
            }
            $outputSchema = [ordered]@{
                type = 'object'
                additionalProperties = $false
                properties = [ordered]@{
                    probe = @{ type = 'string'; const = 'dar-5-resume' }
                    resumed = @{ type = 'boolean'; const = $true }
                }
                required = @('probe', 'resumed')
            }
            Send-Message ([ordered]@{
                id = 4
                method = 'turn/start'
                params = [ordered]@{
                    threadId = $threadId
                    input = @(@{ type = 'text'; text = 'This is a fresh-process resume probe. Return probe dar-5-resume and resumed true without modifying files.' })
                    outputSchema = $outputSchema
                }
            })
            $turnStart = Wait-ForResponse -Id 4 -TimeoutSeconds $ResponseTimeoutSeconds
            $turnId = $turnStart.result.turn.id
            $turnCompleted = Wait-ForNotification -Method 'turn/completed' -TimeoutSeconds $TurnTimeoutSeconds
            if ($turnCompleted.params.turn.status -ne 'completed') {
                throw "Resumed turn ended with status '$($turnCompleted.params.turn.status)'."
            }
        }
        'write-approval' {
            if ($resolvedWorkspace -notmatch '(?i)codex-host[\\/]workspaces[\\/]write-probe$') {
                throw "The write-approval scenario must run in the disposable write-probe workspace, not '$resolvedWorkspace'."
            }
            Send-Message ([ordered]@{
                id = 3
                method = 'thread/start'
                params = [ordered]@{
                    cwd = $resolvedWorkspace
                    ephemeral = $false
                    sandbox = 'read-only'
                    approvalPolicy = 'on-request'
                    approvalsReviewer = 'user'
                    threadSource = 'darkstar-probe'
                }
            })
            $threadStart = Wait-ForResponse -Id 3 -TimeoutSeconds $ResponseTimeoutSeconds
            $threadId = $threadStart.result.thread.id
            $outputSchema = [ordered]@{
                type = 'object'
                additionalProperties = $false
                properties = [ordered]@{
                    wrote = @{ type = 'boolean' }
                    content = @{ type = 'string' }
                    networkResult = @{ type = 'string' }
                }
                required = @('wrote', 'content', 'networkResult')
            }
            Send-Message ([ordered]@{
                id = 4
                method = 'turn/start'
                params = [ordered]@{
                    threadId = $threadId
                    input = @(@{
                        type = 'text'
                        text = 'This is an isolated approval probe. Use two separate PowerShell tool calls. First create probe-write.txt in the current workspace with exactly DARKSTAR_WRITE_OK and no trailing spaces. Second request https://example.com with Invoke-WebRequest and observe the status or policy failure. Do not touch any other file. Return whether the write succeeded, its exact content, and a short networkResult.'
                    })
                    outputSchema = $outputSchema
                }
            })
            $turnStart = Wait-ForResponse -Id 4 -TimeoutSeconds $ResponseTimeoutSeconds
            $turnId = $turnStart.result.turn.id
            $turnCompleted = Wait-ForNotification -Method 'turn/completed' -TimeoutSeconds $TurnTimeoutSeconds
            if ($turnCompleted.params.turn.status -ne 'completed') {
                throw "Write/approval turn ended with status '$($turnCompleted.params.turn.status)'."
            }
            $writeTarget = Join-Path $resolvedWorkspace 'probe-write.txt'
            if (-not (Test-Path -LiteralPath $writeTarget)) {
                throw 'The approved write did not create probe-write.txt.'
            }
            if ((Get-Content -Raw -LiteralPath $writeTarget).TrimEnd("`r", "`n") -ne 'DARKSTAR_WRITE_OK') {
                throw 'probe-write.txt did not contain the expected marker.'
            }
            if ($approvalRequestCount -lt 1) {
                throw 'No provider approval request was observed during the write-capable attempt.'
            }
            if ($networkApprovalRequestCount -lt 1) {
                throw 'No network approval request was observed.'
            }
        }
        'interrupt' {
            Send-Message ([ordered]@{
                id = 3
                method = 'thread/start'
                params = [ordered]@{
                    cwd = $resolvedWorkspace
                    ephemeral = $false
                    sandbox = 'read-only'
                    approvalPolicy = 'never'
                    threadSource = 'darkstar-probe'
                }
            })
            $threadStart = Wait-ForResponse -Id 3 -TimeoutSeconds $ResponseTimeoutSeconds
            $threadId = $threadStart.result.thread.id
            Send-Message ([ordered]@{
                id = 4
                method = 'turn/start'
                params = [ordered]@{
                    threadId = $threadId
                    input = @(@{ type = 'text'; text = 'Run one interruptible wait for 60 seconds before replying. Do not perform any other work.' })
                }
            })
            $turnStart = Wait-ForResponse -Id 4 -TimeoutSeconds $ResponseTimeoutSeconds
            $turnId = $turnStart.result.turn.id
            $turnStarted = Wait-ForNotification -Method 'turn/started' -TimeoutSeconds $ResponseTimeoutSeconds
            Send-Message ([ordered]@{
                id = 5
                method = 'turn/interrupt'
                params = @{ threadId = $threadId; turnId = $turnId }
            })
            $interruptResponse = Wait-ForResponse -Id 5 -TimeoutSeconds $ResponseTimeoutSeconds
            $turnCompleted = Wait-ForNotification -Method 'turn/completed' -TimeoutSeconds $TurnTimeoutSeconds
            if ($turnCompleted.params.turn.status -ne 'interrupted') {
                throw "Interrupted turn ended with status '$($turnCompleted.params.turn.status)'."
            }
        }
        'process-kill' {
            Send-Message ([ordered]@{
                id = 3
                method = 'thread/start'
                params = [ordered]@{
                    cwd = $resolvedWorkspace
                    ephemeral = $false
                    sandbox = 'read-only'
                    approvalPolicy = 'never'
                    threadSource = 'darkstar-probe'
                }
            })
            $threadStart = Wait-ForResponse -Id 3 -TimeoutSeconds $ResponseTimeoutSeconds
            $threadId = $threadStart.result.thread.id
            Send-Message ([ordered]@{
                id = 4
                method = 'turn/start'
                params = [ordered]@{
                    threadId = $threadId
                    input = @(@{ type = 'text'; text = 'Run one interruptible wait for 60 seconds before replying. Do not perform any other work.' })
                }
            })
            $turnStart = Wait-ForResponse -Id 4 -TimeoutSeconds $ResponseTimeoutSeconds
            $turnId = $turnStart.result.turn.id
            $turnStarted = Wait-ForNotification -Method 'turn/started' -TimeoutSeconds $ResponseTimeoutSeconds
            $process.Kill($true)
            $process.WaitForExit()
            $processKilled = $true
        }
        'image-skill' {
            if (-not $ImagePath) {
                $ImagePath = Join-Path $PSScriptRoot 'assets\dar5-red.png'
            }
            if (-not $SkillPath) {
                $SkillPath = Join-Path $PSScriptRoot 'assets\dar5-skill\SKILL.md'
            }
            $resolvedImagePath = (Resolve-Path -LiteralPath $ImagePath).Path
            $resolvedSkillPath = (Resolve-Path -LiteralPath $SkillPath).Path
            Send-Message ([ordered]@{
                id = 3
                method = 'thread/start'
                params = [ordered]@{
                    cwd = $resolvedWorkspace
                    ephemeral = $false
                    sandbox = 'read-only'
                    approvalPolicy = 'never'
                    threadSource = 'darkstar-probe'
                }
            })
            $threadStart = Wait-ForResponse -Id 3 -TimeoutSeconds $ResponseTimeoutSeconds
            $threadId = $threadStart.result.thread.id
            $outputSchema = [ordered]@{
                type = 'object'
                additionalProperties = $false
                properties = [ordered]@{
                    skillMarker = @{ type = 'string'; const = 'DARKSTAR_SKILL_001' }
                    imageObservation = @{ type = 'string' }
                }
                required = @('skillMarker', 'imageObservation')
            }
            Send-Message ([ordered]@{
                id = 4
                method = 'turn/start'
                params = [ordered]@{
                    threadId = $threadId
                    input = @(
                        @{ type = 'text'; text = 'Use the supplied skill instruction and inspect the supplied local image. Return the required skill marker and a concise description of the image color.' },
                        @{ type = 'skill'; name = 'dar5-probe-skill'; path = $resolvedSkillPath },
                        @{ type = 'localImage'; path = $resolvedImagePath; detail = 'original' }
                    )
                    outputSchema = $outputSchema
                }
            })
            $turnStart = Wait-ForResponse -Id 4 -TimeoutSeconds $ResponseTimeoutSeconds
            $turnId = $turnStart.result.turn.id
            $turnCompleted = Wait-ForNotification -Method 'turn/completed' -TimeoutSeconds $TurnTimeoutSeconds
            if ($turnCompleted.params.turn.status -ne 'completed') {
                throw "Image/skill turn ended with status '$($turnCompleted.params.turn.status)'."
            }
            $finalItem = @($turnCompleted.params.turn.items) | Where-Object { $_.type -eq 'agentMessage' } | Select-Object -Last 1
            if ($finalItem.text -notmatch 'DARKSTAR_SKILL_001') {
                throw 'The final image/skill result did not contain the skill marker.'
            }
        }
        'user-input' {
            Send-Message ([ordered]@{
                id = 3
                method = 'thread/start'
                params = [ordered]@{
                    cwd = $resolvedWorkspace
                    ephemeral = $false
                    sandbox = 'read-only'
                    approvalPolicy = 'on-request'
                    approvalsReviewer = 'user'
                    threadSource = 'darkstar-probe'
                }
            })
            $threadStart = Wait-ForResponse -Id 3 -TimeoutSeconds $ResponseTimeoutSeconds
            $threadId = $threadStart.result.thread.id
            $outputSchema = [ordered]@{
                type = 'object'
                additionalProperties = $false
                properties = [ordered]@{
                    selected = @{ type = 'string'; const = 'Continue' }
                    requestObserved = @{ type = 'boolean'; const = $true }
                }
                required = @('selected', 'requestObserved')
            }
            Send-Message ([ordered]@{
                id = 4
                method = 'turn/start'
                params = [ordered]@{
                    threadId = $threadId
                    collaborationMode = [ordered]@{
                        mode = 'plan'
                        settings = [ordered]@{
                            model = 'gpt-5.6-sol'
                            reasoning_effort = 'low'
                            developer_instructions = $null
                        }
                    }
                    input = @(@{
                        type = 'text'
                        text = 'Call request_user_input exactly once. Ask a one-question choice with id dar5_action and recommended option Continue. After the client answers, return selected Continue and requestObserved true.'
                    })
                    outputSchema = $outputSchema
                }
            })
            $turnStart = Wait-ForResponse -Id 4 -TimeoutSeconds $ResponseTimeoutSeconds
            $turnId = $turnStart.result.turn.id
            $turnCompleted = Wait-ForNotification -Method 'turn/completed' -TimeoutSeconds $TurnTimeoutSeconds
            if ($turnCompleted.params.turn.status -ne 'completed') {
                throw "User-input turn ended with status '$($turnCompleted.params.turn.status)'."
            }
            if ($userInputRequestCount -ne 1) {
                throw "Expected exactly one user-input request; observed $userInputRequestCount."
            }
        }
    }

    if ($threadId -and -not $process.HasExited) {
        Send-Message ([ordered]@{
            id = 90
            method = 'thread/unsubscribe'
            params = @{ threadId = $threadId }
        })
        $unsubscribeResponse = Wait-ForResponse -Id 90 -TimeoutSeconds $ResponseTimeoutSeconds
        $unsubscribeStatus = $unsubscribeResponse.result.status
    }

    $status = 'passed'
}
catch {
    $failure = $_.Exception.Message
    throw
}
finally {
    if (-not $process.HasExited) {
        $process.StandardInput.Close()
        if (-not $process.WaitForExit(5000)) {
            $process.Kill($true)
            $process.WaitForExit()
        }
    }
    $stderr = $stderrTask.GetAwaiter().GetResult()
    if ($stderr) {
        Set-Content -LiteralPath $stderrPath -Value (Protect-Value $stderr) -Encoding utf8
    }

    $manifest = [ordered]@{
        fixtureSchemaVersion = 1
        scenario = $Scenario
        status = $status
        failure = $failure
        startedAt = $startedAt.ToString('O')
        completedAt = [DateTimeOffset]::UtcNow.ToString('O')
        platform = 'windows'
        codexVersion = $codexVersion
        codexExecutable = Protect-Value $resolvedCodexExe
        workspace = Protect-Value $resolvedWorkspace
        transport = 'stdio-jsonrpc-jsonl'
        threadId = $threadId
        turnId = $turnId
        resumedFromThreadId = if ($Scenario -eq 'resume') { $ResumeThreadId } else { $null }
        approvalRequestCount = $approvalRequestCount
        networkApprovalRequestCount = $networkApprovalRequestCount
        userInputRequestCount = $userInputRequestCount
        handledServerRequestMethods = @($handledServerRequestMethods)
        unsubscribeStatus = $unsubscribeStatus
        processKilled = $processKilled
        fixture = Split-Path -Leaf $fixturePath
        stderr = Split-Path -Leaf $stderrPath
    }
    Set-Content -LiteralPath $manifestPath -Value ($manifest | ConvertTo-Json -Depth 20) -Encoding utf8
}

Write-Output ($manifest | ConvertTo-Json -Depth 20)
