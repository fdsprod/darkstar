[CmdletBinding()]
param()

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$repositoryRoot = Split-Path -Parent $PSScriptRoot

function Assert-Command {
    param([Parameter(Mandatory)][string]$Name)

    if (-not (Get-Command $Name -ErrorAction SilentlyContinue)) {
        throw "Required command '$Name' was not found on PATH."
    }
}

function Read-PinnedVersion {
    param([Parameter(Mandatory)][string]$Path)

    return (Get-Content -Raw (Join-Path $repositoryRoot $Path)).Trim()
}

Assert-Command go
Assert-Command node
Assert-Command npm

$expectedGo = Read-PinnedVersion ".go-version"
$expectedNode = Read-PinnedVersion ".node-version"
$expectedNpm = Read-PinnedVersion ".npm-version"

$goOutput = (& go version).Trim()
if ($LASTEXITCODE -ne 0) {
    throw "go version failed with exit code $LASTEXITCODE."
}
if ($goOutput -notmatch "^go version go$([Regex]::Escape($expectedGo))\s") {
    throw "Go $expectedGo is required; found '$goOutput'."
}

$nodeOutput = (& node --version).Trim()
if ($LASTEXITCODE -ne 0) {
    throw "node --version failed with exit code $LASTEXITCODE."
}
if ($nodeOutput -ne "v$expectedNode") {
    throw "Node.js $expectedNode is required; found '$nodeOutput'."
}

$npmOutput = (& npm --version).Trim()
if ($LASTEXITCODE -ne 0) {
    throw "npm --version failed with exit code $LASTEXITCODE."
}
if ($npmOutput -ne $expectedNpm) {
    throw "npm $expectedNpm is required; found '$npmOutput'."
}

Write-Host "Toolchain verified: Go $expectedGo, Node.js $expectedNode, npm $expectedNpm"
