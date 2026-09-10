[CmdletBinding()]
param([string]$OutputDirectory = "out/dev")
Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"
$repositoryRoot = Split-Path -Parent $PSScriptRoot
& (Join-Path $PSScriptRoot "Assert-Toolchain.ps1")
& (Join-Path $PSScriptRoot "Build.ps1") -OutputDirectory $OutputDirectory
if (-not [IO.Path]::IsPathRooted($OutputDirectory)) { $OutputDirectory = Join-Path $repositoryRoot $OutputDirectory }
$binary = Join-Path $OutputDirectory "darkstar.exe"
& $binary daemon restart --json
if ($LASTEXITCODE -ne 0) { throw "Daemon restart failed with exit code $LASTEXITCODE." }
& $binary api status --json
if ($LASTEXITCODE -ne 0) { throw "Daemon readiness failed with exit code $LASTEXITCODE." }
