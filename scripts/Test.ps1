[CmdletBinding()]
param()

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$repositoryRoot = Split-Path -Parent $PSScriptRoot

Push-Location $repositoryRoot
try {
    $goFiles = Get-ChildItem -Path "runtime/src", "runtime/tests" -Recurse -Filter "*.go" |
        ForEach-Object { $_.FullName }
    $unformatted = & gofmt -l @goFiles
    if ($LASTEXITCODE -ne 0) {
        throw "gofmt failed with exit code $LASTEXITCODE."
    }
    if ($unformatted) {
        throw "The following Go files require gofmt:`n$($unformatted -join "`n")"
    }

    & go -C runtime vet ./...
    if ($LASTEXITCODE -ne 0) {
        throw "go vet failed with exit code $LASTEXITCODE."
    }

    & go -C runtime test ./...
    if ($LASTEXITCODE -ne 0) {
        throw "Go tests failed with exit code $LASTEXITCODE."
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
}
