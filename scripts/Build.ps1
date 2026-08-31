[CmdletBinding()]
param(
    [ValidatePattern("^[0-9A-Za-z.+_-]+$")]
    [string]$Version = "dev",

    [string]$OutputDirectory = "out",

    [switch]$SkipDashboard
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$repositoryRoot = Split-Path -Parent $PSScriptRoot
if (-not [IO.Path]::IsPathRooted($OutputDirectory)) {
    $OutputDirectory = Join-Path $repositoryRoot $OutputDirectory
}
$OutputDirectory = [IO.Path]::GetFullPath($OutputDirectory)
$binaryPath = Join-Path $OutputDirectory "darkstar.exe"
$linkerFlags = "-s -w -buildid= -X github.com/fdsprod/darkstar/runtime/src/cli.Version=$Version"
$previousGoEnvironment = @{
    CGO_ENABLED = [Environment]::GetEnvironmentVariable("CGO_ENABLED", "Process")
    GOARCH = [Environment]::GetEnvironmentVariable("GOARCH", "Process")
    GOOS = [Environment]::GetEnvironmentVariable("GOOS", "Process")
    GOTOOLCHAIN = [Environment]::GetEnvironmentVariable("GOTOOLCHAIN", "Process")
}

& (Join-Path $PSScriptRoot "Assert-Toolchain.ps1")

Push-Location $repositoryRoot
try {
    New-Item -ItemType Directory -Force -Path $OutputDirectory | Out-Null

    $env:CGO_ENABLED = "0"
    $env:GOARCH = "amd64"
    $env:GOOS = "windows"
    $env:GOTOOLCHAIN = "local"

    & go -C runtime build -mod=readonly -trimpath -buildvcs=false "-ldflags=$linkerFlags" -o $binaryPath ./src/cmd/darkstar
    if ($LASTEXITCODE -ne 0) {
        throw "Go build failed with exit code $LASTEXITCODE."
    }

    if (-not $SkipDashboard) {
        & npm run build
        if ($LASTEXITCODE -ne 0) {
            throw "Dashboard build failed with exit code $LASTEXITCODE."
        }
    }
}
finally {
    Pop-Location
    foreach ($name in $previousGoEnvironment.Keys) {
        [Environment]::SetEnvironmentVariable($name, $previousGoEnvironment[$name], "Process")
    }
}

Write-Host "Built $binaryPath"
