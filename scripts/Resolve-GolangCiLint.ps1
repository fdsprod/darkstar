[CmdletBinding()]
param()

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$repositoryRoot = Split-Path -Parent $PSScriptRoot
$expectedVersion = (Get-Content -Raw (Join-Path $repositoryRoot ".golangci-version")).Trim()
$executableName = if ($IsWindows) { "golangci-lint.exe" } else { "golangci-lint" }

$command = Get-Command golangci-lint -ErrorAction SilentlyContinue
if ($command) {
    $executablePath = $command.Source
}
else {
    $goBin = (& go env GOBIN).Trim()
    if ($LASTEXITCODE -ne 0) {
        throw "go env GOBIN failed with exit code $LASTEXITCODE."
    }
    if (-not $goBin) {
        $goPaths = (& go env GOPATH).Trim() -split [IO.Path]::PathSeparator
        if ($LASTEXITCODE -ne 0) {
            throw "go env GOPATH failed with exit code $LASTEXITCODE."
        }
        $goBin = Join-Path $goPaths[0] "bin"
    }

    $executablePath = Join-Path $goBin $executableName
}

if (-not (Test-Path -LiteralPath $executablePath -PathType Leaf)) {
    throw "golangci-lint $expectedVersion is required. Run ./scripts/Install-Lint.ps1."
}

$versionOutput = (& $executablePath version 2>&1) -join "`n"
if ($LASTEXITCODE -ne 0) {
    throw "golangci-lint version failed with exit code $LASTEXITCODE."
}

$versionNumber = $expectedVersion.TrimStart("v")
if ($versionOutput -notmatch "\bversion\s+$([Regex]::Escape($versionNumber))\b") {
    throw "golangci-lint $expectedVersion is required; found '$versionOutput'. Run ./scripts/Install-Lint.ps1."
}

Write-Output $executablePath
