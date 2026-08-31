[CmdletBinding()]
param()

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$repositoryRoot = Split-Path -Parent $PSScriptRoot

& (Join-Path $PSScriptRoot "Assert-Toolchain.ps1")

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
