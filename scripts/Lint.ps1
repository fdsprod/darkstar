[CmdletBinding()]
param(
    [switch]$SkipToolchainCheck
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$repositoryRoot = Split-Path -Parent $PSScriptRoot
$previousGoToolchain = [Environment]::GetEnvironmentVariable("GOTOOLCHAIN", "Process")
$previousLintCache = [Environment]::GetEnvironmentVariable("GOLANGCI_LINT_CACHE", "Process")

if (-not $SkipToolchainCheck) {
    & (Join-Path $PSScriptRoot "Assert-Toolchain.ps1") -GoOnly
}

Push-Location $repositoryRoot
try {
    $env:GOTOOLCHAIN = "local"
    if (-not $previousLintCache) {
        $env:GOLANGCI_LINT_CACHE = Join-Path $repositoryRoot "out/golangci-lint-cache"
    }
    New-Item -ItemType Directory -Force $env:GOLANGCI_LINT_CACHE | Out-Null

    $linter = & (Join-Path $PSScriptRoot "Resolve-GolangCiLint.ps1")
    $goModules = @(& git ls-files -- ":(glob)**/go.mod")
    if ($LASTEXITCODE -ne 0) {
        throw "git ls-files failed with exit code $LASTEXITCODE."
    }
    if ($goModules.Count -eq 0) {
        throw "No tracked Go modules were found."
    }

    foreach ($goModule in $goModules) {
        $moduleDirectory = Split-Path -Parent (Join-Path $repositoryRoot $goModule)
        Push-Location $moduleDirectory
        try {
            & $linter run --config (Join-Path $repositoryRoot ".golangci.yml") ./...
            if ($LASTEXITCODE -ne 0) {
                throw "golangci-lint failed for '$goModule' with exit code $LASTEXITCODE."
            }
        }
        finally {
            Pop-Location
        }
    }

    Write-Host "Linted $($goModules.Count) tracked Go module(s)."
}
finally {
    Pop-Location
    [Environment]::SetEnvironmentVariable("GOTOOLCHAIN", $previousGoToolchain, "Process")
    [Environment]::SetEnvironmentVariable("GOLANGCI_LINT_CACHE", $previousLintCache, "Process")
}
