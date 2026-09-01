[CmdletBinding()]
param()

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$repositoryRoot = Split-Path -Parent $PSScriptRoot
$previousGoToolchain = [Environment]::GetEnvironmentVariable("GOTOOLCHAIN", "Process")

& (Join-Path $PSScriptRoot "Assert-Toolchain.ps1") -GoOnly

try {
    $env:GOTOOLCHAIN = "local"
    $version = (Get-Content -Raw (Join-Path $repositoryRoot ".golangci-version")).Trim()

    & go install "github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$version"
    if ($LASTEXITCODE -ne 0) {
        throw "Installing golangci-lint $version failed with exit code $LASTEXITCODE."
    }

    $executablePath = & (Join-Path $PSScriptRoot "Resolve-GolangCiLint.ps1")
    Write-Host "Installed golangci-lint $version at $executablePath"
}
finally {
    [Environment]::SetEnvironmentVariable("GOTOOLCHAIN", $previousGoToolchain, "Process")
}
