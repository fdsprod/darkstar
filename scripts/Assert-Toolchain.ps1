[CmdletBinding()]
param(
    [switch]$GoOnly
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$repositoryRoot = Split-Path -Parent $PSScriptRoot
& (Join-Path $PSScriptRoot "Use-ProjectToolchain.ps1") -GoOnly:$GoOnly

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
if (-not $GoOnly) {
    Assert-Command node
    Assert-Command npm
}

$expectedGo = Read-PinnedVersion ".go-version"

$goOutput = (& go version).Trim()
if ($LASTEXITCODE -ne 0) {
    throw "go version failed with exit code $LASTEXITCODE."
}
if ($goOutput -notmatch '^go version go(\d+\.\d+\.\d+)\s' -or [version]$Matches[1] -lt [version]$expectedGo) {
    throw "Go $expectedGo or newer is required; found '$goOutput'."
}

if ($GoOnly) {
    Write-Host "Go toolchain verified: Go $expectedGo"
    return
}

$expectedNode = Read-PinnedVersion ".node-version"
$expectedNpm = Read-PinnedVersion ".npm-version"
$nodeOutput = (& node --version).Trim()
if ($LASTEXITCODE -ne 0) {
    throw "node --version failed with exit code $LASTEXITCODE."
}
if ($nodeOutput -notmatch '^v(\d+\.\d+\.\d+)$' -or [version]$Matches[1] -lt [version]$expectedNode) {
    throw "Node.js $expectedNode or newer is required; found '$nodeOutput'."
}

$npmOutput = (& npm --version).Trim()
if ($LASTEXITCODE -ne 0) {
    throw "npm --version failed with exit code $LASTEXITCODE."
}
if ($npmOutput -notmatch '^\d+\.\d+\.\d+$' -or [version]$npmOutput -lt [version]$expectedNpm) {
    throw "npm $expectedNpm or newer is required; found '$npmOutput'."
}

$npxOutput = (& npx --version).Trim()
if ($LASTEXITCODE -ne 0 -or $npxOutput -notmatch '^\d+\.\d+\.\d+$' -or [version]$npxOutput -lt [version]$expectedNpm) { throw "npx $expectedNpm or newer is unavailable. Run ./scripts/Setup-Toolchain.ps1." }

Write-Host "Toolchain verified: $goOutput, Node.js $nodeOutput, npm $npmOutput, npx $npxOutput"
