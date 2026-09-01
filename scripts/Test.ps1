[CmdletBinding()]
param()

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$repositoryRoot = Split-Path -Parent $PSScriptRoot
$previousGoToolchain = [Environment]::GetEnvironmentVariable("GOTOOLCHAIN", "Process")

& (Join-Path $PSScriptRoot "Assert-Toolchain.ps1")

Push-Location $repositoryRoot
try {
    $env:GOTOOLCHAIN = "local"

    $goFiles = @(& (Join-Path $PSScriptRoot "Get-TrackedGoFiles.ps1"))
    $unformatted = foreach ($goFile in $goFiles) {
        & gofmt -l $goFile
        if ($LASTEXITCODE -ne 0) {
            throw "gofmt failed for '$goFile' with exit code $LASTEXITCODE."
        }
    }
    if ($unformatted) {
        throw "The following tracked Go files require gofmt:`n$($unformatted -join "`n")`nRun ./scripts/Format.ps1 to fix them."
    }

    & (Join-Path $PSScriptRoot "Lint.ps1") -SkipToolchainCheck
    if ($LASTEXITCODE -ne 0) {
        throw "Go lint checks failed with exit code $LASTEXITCODE."
    }

    & go -C runtime test -mod=readonly ./...
    if ($LASTEXITCODE -ne 0) {
        throw "Go tests failed with exit code $LASTEXITCODE."
    }

    & node scripts/schema-tool.mjs check
    if ($LASTEXITCODE -ne 0) {
        throw "Schema validation or generation check failed with exit code $LASTEXITCODE."
    }

    $schemaBaseRef = [Environment]::GetEnvironmentVariable("DARKSTAR_SCHEMA_BASE_REF", "Process")
    if ($schemaBaseRef -and $schemaBaseRef -notmatch "^0+$") {
        & node scripts/schema-tool.mjs compatibility --base $schemaBaseRef
        if ($LASTEXITCODE -ne 0) {
            throw "Schema compatibility check failed with exit code $LASTEXITCODE."
        }
    }

    $contractTests = Get-ChildItem -Path "tests" -Filter "*.test.mjs" |
        Sort-Object Name |
        ForEach-Object { $_.FullName }
    & node --test @contractTests
    if ($LASTEXITCODE -ne 0) {
        throw "Contract tests failed with exit code $LASTEXITCODE."
    }

    & npm run check
    if ($LASTEXITCODE -ne 0) {
        throw "Dashboard checks failed with exit code $LASTEXITCODE."
    }
}
finally {
    Pop-Location
    [Environment]::SetEnvironmentVariable("GOTOOLCHAIN", $previousGoToolchain, "Process")
}
