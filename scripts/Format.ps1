[CmdletBinding()]
param()

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$previousGoToolchain = [Environment]::GetEnvironmentVariable("GOTOOLCHAIN", "Process")

& (Join-Path $PSScriptRoot "Assert-Toolchain.ps1") -GoOnly

try {
    $env:GOTOOLCHAIN = "local"
    $goFiles = @(& (Join-Path $PSScriptRoot "Get-TrackedGoFiles.ps1"))

    foreach ($goFile in $goFiles) {
        & gofmt -w $goFile
        if ($LASTEXITCODE -ne 0) {
            throw "gofmt failed for '$goFile' with exit code $LASTEXITCODE."
        }
    }

    Write-Host "Formatted $($goFiles.Count) tracked Go files."
}
finally {
    [Environment]::SetEnvironmentVariable("GOTOOLCHAIN", $previousGoToolchain, "Process")
}
