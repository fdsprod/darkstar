[CmdletBinding()]
param(
    [ValidatePattern("^[0-9A-Za-z.+_-]+$")]
    [string]$Version = "dev"
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

& (Join-Path $PSScriptRoot "Test.ps1")
if ($LASTEXITCODE -ne 0) {
    exit $LASTEXITCODE
}

& (Join-Path $PSScriptRoot "Build.ps1") -Version $Version
if ($LASTEXITCODE -ne 0) {
    exit $LASTEXITCODE
}
