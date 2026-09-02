[CmdletBinding()]
param(
    [ValidatePattern("^[0-9A-Za-z.+_-]+$")]
    [string]$Version = "dev",

    [string]$BinaryPath = "out/darkstar.exe",

    [string]$OutputDirectory = "out"
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$repositoryRoot = Split-Path -Parent $PSScriptRoot

function Resolve-RepositoryPath {
    param([Parameter(Mandatory)][string]$Path)

    if (-not [IO.Path]::IsPathRooted($Path)) {
        $Path = Join-Path $repositoryRoot $Path
    }
    return [IO.Path]::GetFullPath($Path)
}

$BinaryPath = Resolve-RepositoryPath $BinaryPath
$OutputDirectory = Resolve-RepositoryPath $OutputDirectory

if (-not (Test-Path -LiteralPath $BinaryPath -PathType Leaf)) {
    throw "Binary '$BinaryPath' does not exist. Run scripts/Build.ps1 first."
}

$archiveName = "darkstar-$Version-windows-amd64.zip"
$archivePath = Join-Path $OutputDirectory $archiveName
$checksumPath = "$archivePath.sha256"
$packageRoot = "darkstar-$Version-windows-amd64"
$fixedTimestamp = [DateTimeOffset]::new(1980, 1, 1, 0, 0, 0, [TimeSpan]::Zero)
$files = @(
    [pscustomobject]@{ Source = $BinaryPath; Entry = "darkstar.exe" }
    [pscustomobject]@{ Source = (Join-Path $repositoryRoot "LICENSE"); Entry = "LICENSE" }
    [pscustomobject]@{ Source = (Join-Path $repositoryRoot "README.md"); Entry = "README.md" }
)
$skillRoot = Join-Path $repositoryRoot "skills/builtin"
$files += Get-ChildItem -LiteralPath $skillRoot -Recurse -File | ForEach-Object {
    $relative = [IO.Path]::GetRelativePath($skillRoot, $_.FullName).Replace('\', '/')
    [pscustomobject]@{ Source = $_.FullName; Entry = "skills/$relative" }
}
$workflowRoot = Join-Path $repositoryRoot "examples/workflows"
$files += @("software-delivery.json", "story-execution.json") | ForEach-Object {
    [pscustomobject]@{ Source = (Join-Path $workflowRoot $_); Entry = "workflows/$_" }
}
$planningTemplateRoot = Join-Path $repositoryRoot "templates/planning"
$files += Get-ChildItem -LiteralPath $planningTemplateRoot -File -Filter "*.md" | ForEach-Object {
    [pscustomobject]@{ Source = $_.FullName; Entry = "templates/planning/$($_.Name)" }
}
$files += @("planning-artifact-v1alpha1.schema.json", "delivery-evidence-v1alpha1.schema.json") | ForEach-Object {
    [pscustomobject]@{
        Source = (Join-Path $repositoryRoot "schemas/$_")
        Entry = "schemas/$_"
    }
}
$files = $files | Sort-Object Entry

New-Item -ItemType Directory -Force -Path $OutputDirectory | Out-Null
Remove-Item -LiteralPath $archivePath, $checksumPath -Force -ErrorAction SilentlyContinue

Add-Type -AssemblyName System.IO.Compression
$archiveStream = [IO.File]::Open($archivePath, [IO.FileMode]::CreateNew, [IO.FileAccess]::Write, [IO.FileShare]::None)
try {
    $archive = [IO.Compression.ZipArchive]::new(
        $archiveStream,
        [IO.Compression.ZipArchiveMode]::Create,
        $false
    )
    try {
        foreach ($file in $files) {
            $entry = $archive.CreateEntry(
                "$packageRoot/$($file.Entry)",
                [IO.Compression.CompressionLevel]::NoCompression
            )
            $entry.LastWriteTime = $fixedTimestamp

            $entryStream = $entry.Open()
            $sourceStream = [IO.File]::OpenRead($file.Source)
            try {
                $sourceStream.CopyTo($entryStream)
            }
            finally {
                $sourceStream.Dispose()
                $entryStream.Dispose()
            }
        }
    }
    finally {
        $archive.Dispose()
    }
}
finally {
    $archiveStream.Dispose()
}

$archiveHash = (Get-FileHash -LiteralPath $archivePath -Algorithm SHA256).Hash.ToLowerInvariant()
$checksum = "$archiveHash  $archiveName`n"
[IO.File]::WriteAllText($checksumPath, $checksum, [Text.UTF8Encoding]::new($false))

Write-Host "Packaged $archivePath"
Write-Host "SHA-256 $archiveHash"
