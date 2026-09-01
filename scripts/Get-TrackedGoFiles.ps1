[CmdletBinding()]
param()

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$repositoryRoot = Split-Path -Parent $PSScriptRoot

Push-Location $repositoryRoot
try {
    $goFiles = @(& git ls-files -- "*.go")
    if ($LASTEXITCODE -ne 0) {
        throw "git ls-files failed with exit code $LASTEXITCODE."
    }

    foreach ($goFile in $goFiles) {
        if ($goFile) {
            Join-Path $repositoryRoot $goFile
        }
    }
}
finally {
    Pop-Location
}
