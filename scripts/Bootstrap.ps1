[CmdletBinding()]
param()

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$repositoryRoot = Split-Path -Parent $PSScriptRoot

try { & (Join-Path $PSScriptRoot "Use-ProjectToolchain.ps1") }
catch {
    & (Join-Path $PSScriptRoot "Setup-Toolchain.ps1")
}

& (Join-Path $PSScriptRoot "Assert-Toolchain.ps1")
& (Join-Path $PSScriptRoot "Install-Lint.ps1")

Push-Location $repositoryRoot
try {
    & npm ci
    if ($LASTEXITCODE -ne 0) {
        throw "npm ci failed with exit code $LASTEXITCODE."
    }
}
finally {
    Pop-Location
}
