[CmdletBinding()]
param(
    [string]$FixtureRoot = (Join-Path $PSScriptRoot 'fixtures')
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function Assert-True {
    param([bool]$Condition, [string]$Message)
    if (-not $Condition) { throw $Message }
}

function Get-OptionalValue {
    param($Object, [string]$Name)
    if ($null -eq $Object) { return $null }
    $property = $Object.PSObject.Properties[$Name]
    if ($null -eq $property) { return $null }
    return $property.Value
}

function Test-Redaction {
    param([string]$RawFixture, [string]$FixturePath)
    Assert-True ($RawFixture -notmatch '(?i)\b[A-Z0-9._%+-]+@[A-Z0-9.-]+\.[A-Z]{2,}\b') "Fixture '$FixturePath' contains an email address."
    $profileMatches = [regex]::Matches($RawFixture, '(?i)C:\\+Users\\+(?<name>[^\\/"\s]+)')
    foreach ($profileMatch in $profileMatches) {
        Assert-True ($profileMatch.Groups['name'].Value -eq '<user>') "Fixture '$FixturePath' contains an unredacted user profile path."
    }
}

$manifests = @(Get-ChildItem -LiteralPath $FixtureRoot -Filter '*.manifest.json' -Recurse -File)
Assert-True ($manifests.Count -gt 0) 'No Codex host fixture manifests were found.'

$results = foreach ($manifestFile in $manifests) {
    $manifest = Get-Content -Raw -LiteralPath $manifestFile.FullName | ConvertFrom-Json -Depth 100
    $fixturePath = Join-Path $manifestFile.DirectoryName $manifest.fixture
    Assert-True (Test-Path -LiteralPath $fixturePath) "Missing fixture '$fixturePath'."
    Assert-True ($manifest.platform -eq 'windows') "Fixture '$fixturePath' is not a Windows capture."
    Assert-True ([bool]$manifest.codexVersion) "Fixture '$fixturePath' has no Codex version."

    $rawFixture = Get-Content -Raw -LiteralPath $fixturePath
    Test-Redaction -RawFixture $rawFixture -FixturePath $fixturePath
    $records = @(Get-Content -LiteralPath $fixturePath | Where-Object { $_ } | ForEach-Object {
        $_ | ConvertFrom-Json -Depth 100
    })

    if ($manifest.status -eq 'failed-as-expected') {
        Assert-True ([bool]$manifest.failureClass) "Expected-failure fixture '$fixturePath' has no failure class."
        [pscustomobject]@{
            Transport = $manifest.transport
            Scenario = $manifest.scenario
            CodexVersion = $manifest.codexVersion
            Records = $records.Count
            Status = 'expected-failure'
        }
        continue
    }

    Assert-True ($manifest.status -eq 'passed') "Fixture '$fixturePath' did not pass."
    Assert-True ($records.Count -gt 0) "Fixture '$fixturePath' is empty."
    for ($index = 0; $index -lt $records.Count; $index += 1) {
        Assert-True ($records[$index].sequence -eq ($index + 1)) "Fixture '$fixturePath' has a sequence gap at record $($index + 1)."
    }

    if ($manifest.transport -eq 'stdio-jsonrpc-jsonl') {
        # JSON-RPC IDs are scoped to the sender. Server-initiated approval
        # requests can reuse client request IDs, so select responses by shape.
        $initializeResponse = $records | Where-Object {
            $result = Get-OptionalValue $_.message 'result'
            (Get-OptionalValue $result 'platformOs') -and
            (Get-OptionalValue $result 'userAgent')
        } | Select-Object -Last 1
        Assert-True ($null -ne $initializeResponse) "Fixture '$fixturePath' has no initialize response."
        Assert-True ($initializeResponse.message.result.platformOs -eq 'windows') "Fixture '$fixturePath' did not initialize on Windows."
        Assert-True ($initializeResponse.message.result.userAgent -match [regex]::Escape($manifest.codexVersion)) "Fixture '$fixturePath' initialize version does not match its manifest."

        $accountResponse = $records | Where-Object {
            $result = Get-OptionalValue $_.message 'result'
            $null -ne (Get-OptionalValue $result 'requiresOpenaiAuth') -and
            $null -ne (Get-OptionalValue $result 'account')
        } | Select-Object -Last 1
        Assert-True ($null -ne $accountResponse) "Fixture '$fixturePath' has no account response."
        Assert-True ($accountResponse.message.result.requiresOpenaiAuth -eq $true) "Fixture '$fixturePath' has an unexpected authentication mode."
        Assert-True ($accountResponse.message.result.account.type -eq 'chatgpt') "Fixture '$fixturePath' did not use configured ChatGPT authentication."

        $methods = @($records | ForEach-Object { Get-OptionalValue $_.message 'method' } | Where-Object { $_ })
        if ($manifest.scenario -ne 'handshake') {
            Assert-True ([bool]$manifest.threadId) "Fixture '$fixturePath' has no thread ID."
            Assert-True ([bool]$manifest.turnId) "Fixture '$fixturePath' has no turn ID."
        }

        switch ($manifest.scenario) {
            'read-only' {
                foreach ($requiredMethod in @(
                    'thread/started', 'turn/started', 'item/agentMessage/delta',
                    'item/commandExecution/outputDelta', 'thread/tokenUsage/updated',
                    'account/rateLimits/updated', 'turn/completed'
                )) {
                    Assert-True ($methods -contains $requiredMethod) "Fixture '$fixturePath' is missing '$requiredMethod'."
                }
                $finalMessage = $records | Where-Object {
                    (Get-OptionalValue $_.message 'method') -eq 'item/completed' -and
                    $_.message.params.item.type -eq 'agentMessage' -and
                    $_.message.params.item.phase -eq 'final_answer'
                } | Select-Object -Last 1
                $structured = $finalMessage.message.params.item.text | ConvertFrom-Json
                Assert-True ($structured.probe -eq 'dar-5' -and $structured.cwdReadable -eq $true) "Fixture '$fixturePath' has an invalid structured read-only result."
            }
            'resume' {
                Assert-True ($methods -contains 'thread/resume') "Fixture '$fixturePath' did not request thread/resume."
                Assert-True ($methods -contains 'turn/completed') "Fixture '$fixturePath' did not complete the resumed turn."
                Assert-True ($manifest.threadId -eq $manifest.resumedFromThreadId) "Fixture '$fixturePath' changed thread identity during resume."
            }
            'write-approval' {
                Assert-True ($manifest.approvalRequestCount -ge 2) "Fixture '$fixturePath' did not observe both scoped approvals."
                Assert-True ($manifest.networkApprovalRequestCount -ge 1) "Fixture '$fixturePath' did not observe a network approval."
                Assert-True ($methods -contains 'item/commandExecution/requestApproval') "Fixture '$fixturePath' has no command approval request."
                Assert-True ($methods -contains 'turn/completed') "Fixture '$fixturePath' write turn did not complete."
                Assert-True ($rawFixture -match 'DARKSTAR_WRITE_OK') "Fixture '$fixturePath' has no write marker."
            }
            'interrupt' {
                Assert-True ($methods -contains 'turn/interrupt') "Fixture '$fixturePath' did not send turn/interrupt."
                $completedTurn = $records | Where-Object { (Get-OptionalValue $_.message 'method') -eq 'turn/completed' } | Select-Object -Last 1
                Assert-True ($completedTurn.message.params.turn.status -eq 'interrupted') "Fixture '$fixturePath' did not end interrupted."
            }
            'process-kill' {
                Assert-True ($manifest.processKilled -eq $true) "Fixture '$fixturePath' did not kill the owned process."
                Assert-True ($methods -contains 'turn/started') "Fixture '$fixturePath' killed the process before the turn started."
            }
            'image-skill' {
                Assert-True ($rawFixture -match '"type":"imageView"') "Fixture '$fixturePath' has no image-view evidence."
                Assert-True ($rawFixture -match 'DARKSTAR_SKILL_001') "Fixture '$fixturePath' has no skill marker."
                Assert-True ($methods -contains 'turn/completed') "Fixture '$fixturePath' image/skill turn did not complete."
            }
            'user-input' {
                Assert-True ($manifest.userInputRequestCount -eq 1) "Fixture '$fixturePath' did not observe exactly one user-input request."
                Assert-True ($methods -contains 'item/tool/requestUserInput') "Fixture '$fixturePath' has no user-input server request."
                Assert-True ($methods -contains 'turn/completed') "Fixture '$fixturePath' user-input turn did not complete."
            }
        }
    }
    elseif ($manifest.transport -eq 'exec-jsonl') {
        $eventTypes = @($records | ForEach-Object { Get-OptionalValue $_.message 'type' } | Where-Object { $_ })
        Assert-True ($eventTypes -contains 'thread.started') "Fixture '$fixturePath' has no exec thread.started event."
        Assert-True ($eventTypes -contains 'turn.started') "Fixture '$fixturePath' has no exec turn.started event."
        if ((Get-OptionalValue $manifest 'processKilled') -eq $true) {
            Assert-True ($manifest.exitCode -eq -1) "Fixture '$fixturePath' has an unexpected killed-process exit code."
        }
        else {
            Assert-True ($manifest.exitCode -eq 0) "Fixture '$fixturePath' has a nonzero exec exit code."
            Assert-True ($eventTypes -contains 'turn.completed') "Fixture '$fixturePath' has no exec turn.completed event."
            if ($manifest.scenario -eq 'resume') {
                Assert-True ($manifest.sessionId -eq $manifest.resumedFromSessionId) "Fixture '$fixturePath' changed exec session identity during resume."
            }
        }
    }
    else {
        throw "Fixture '$fixturePath' uses unknown transport '$($manifest.transport)'."
    }

    [pscustomobject]@{
        Transport = $manifest.transport
        Scenario = $manifest.scenario
        CodexVersion = $manifest.codexVersion
        Records = $records.Count
        Status = 'passed'
    }
}

$results | Sort-Object CodexVersion, Transport, Scenario | Format-Table -AutoSize
