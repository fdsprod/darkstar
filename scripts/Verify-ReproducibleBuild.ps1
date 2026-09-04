[CmdletBinding()]
param(
    [ValidatePattern("^[0-9A-Za-z.+_-]+$")]
    [string]$Version = "dev"
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$repositoryRoot = Split-Path -Parent $PSScriptRoot
$temporaryRoot = Join-Path ([IO.Path]::GetTempPath()) "darkstar-repro-$([Guid]::NewGuid().ToString('N'))"
$firstDirectory = Join-Path $temporaryRoot "first"
$secondDirectory = Join-Path $temporaryRoot "second"

function Get-Sha256 {
    param([Parameter(Mandatory)][string]$Path)

    return (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToLowerInvariant()
}

try {
    # Compile the dashboard once, then prove that two Go builds embed the exact
    # same staged output. This also keeps the reproducibility check usable from
    # a clean checkout instead of relying on a preceding Build.ps1 invocation.
    & (Join-Path $PSScriptRoot "Assert-Toolchain.ps1")
    Push-Location $repositoryRoot
    try {
        & npm run build
        if ($LASTEXITCODE -ne 0) {
            throw "Dashboard build failed with exit code $LASTEXITCODE."
        }
    }
    finally {
        Pop-Location
    }

    & (Join-Path $PSScriptRoot "Build.ps1") -Version $Version -OutputDirectory $firstDirectory -SkipDashboard
    & (Join-Path $PSScriptRoot "Package.ps1") -Version $Version -BinaryPath (Join-Path $firstDirectory "darkstar.exe") -OutputDirectory $firstDirectory

    & (Join-Path $PSScriptRoot "Build.ps1") -Version $Version -OutputDirectory $secondDirectory -SkipDashboard
    & (Join-Path $PSScriptRoot "Package.ps1") -Version $Version -BinaryPath (Join-Path $secondDirectory "darkstar.exe") -OutputDirectory $secondDirectory

    $archiveName = "darkstar-$Version-windows-amd64.zip"
    $comparisons = @(
        [pscustomobject]@{
            Name = "Windows binary"
            First = (Join-Path $firstDirectory "darkstar.exe")
            Second = (Join-Path $secondDirectory "darkstar.exe")
        }
        [pscustomobject]@{
            Name = "Windows package"
            First = (Join-Path $firstDirectory $archiveName)
            Second = (Join-Path $secondDirectory $archiveName)
        }
    )

    foreach ($comparison in $comparisons) {
        $firstHash = Get-Sha256 $comparison.First
        $secondHash = Get-Sha256 $comparison.Second
        if ($firstHash -ne $secondHash) {
            throw "$($comparison.Name) is not reproducible: $firstHash != $secondHash."
        }
        Write-Host "$($comparison.Name) is reproducible: $firstHash"
    }

    $firstBinary = $comparisons[0].First
    $versionOutput = (& $firstBinary version).Trim()
    if ($LASTEXITCODE -ne 0) {
        throw "Built binary failed with exit code $LASTEXITCODE."
    }
    if ($versionOutput -ne "darkstar $Version") {
        throw "Built binary reported '$versionOutput'; expected 'darkstar $Version'."
    }
}
finally {
    $resolvedTemporaryRoot = [IO.Path]::GetFullPath($temporaryRoot)
    $resolvedSystemTemp = [IO.Path]::GetFullPath([IO.Path]::GetTempPath())
    if ($resolvedTemporaryRoot.StartsWith($resolvedSystemTemp, [StringComparison]::OrdinalIgnoreCase) -and
        (Split-Path -Leaf $resolvedTemporaryRoot).StartsWith("darkstar-repro-", [StringComparison]::Ordinal)) {
        Remove-Item -LiteralPath $resolvedTemporaryRoot -Recurse -Force -ErrorAction SilentlyContinue
    }
}
