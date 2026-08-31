[CmdletBinding()]
param(
    [ValidatePattern("^[0-9A-Za-z.+_-]+$")]
    [string]$Version = "dev"
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$repositoryRoot = Split-Path -Parent $PSScriptRoot
$outputDirectory = Join-Path $repositoryRoot "out"
$binaryPath = Join-Path $outputDirectory "darkstar.exe"
$linkerFlags = "-X github.com/fdsprod/darkstar/runtime/src/cli.Version=$Version"

Push-Location $repositoryRoot
try {
    New-Item -ItemType Directory -Force -Path $outputDirectory | Out-Null

    & go -C runtime build -trimpath "-ldflags=$linkerFlags" -o $binaryPath ./src/cmd/darkstar
    if ($LASTEXITCODE -ne 0) {
        throw "Go build failed with exit code $LASTEXITCODE."
    }

    & npm run build
    if ($LASTEXITCODE -ne 0) {
        throw "Dashboard build failed with exit code $LASTEXITCODE."
    }
}
finally {
    Pop-Location
}

Write-Host "Built $binaryPath"
