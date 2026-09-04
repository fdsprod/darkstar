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
$linkerFlags = "-s -w -buildid= -X darkstar/src/cli.Version=$Version"
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

    if (-not $SkipDashboard) {
        & npm run build
        if ($LASTEXITCODE -ne 0) {
            throw "Dashboard build failed with exit code $LASTEXITCODE."
        }
    }

    $dashboardBuildDirectory = Join-Path $repositoryRoot "dashboard/dist"
    $dashboardIndex = Join-Path $dashboardBuildDirectory "index.html"
    if (-not (Test-Path -LiteralPath $dashboardIndex -PathType Leaf)) {
        throw "Dashboard output '$dashboardIndex' does not exist. Run without -SkipDashboard first."
    }
    $dashboardEmbedDirectory = Join-Path $repositoryRoot "runtime/src/dashboardassets/dist"
    $expectedEmbedDirectory = [IO.Path]::GetFullPath((Join-Path $repositoryRoot "runtime/src/dashboardassets/dist"))
    if ([IO.Path]::GetFullPath($dashboardEmbedDirectory) -ne $expectedEmbedDirectory) {
        throw "Dashboard embed staging path resolved outside its expected location."
    }
    if (Test-Path -LiteralPath $dashboardEmbedDirectory) {
        Remove-Item -LiteralPath $dashboardEmbedDirectory -Recurse -Force
    }
    New-Item -ItemType Directory -Force -Path $dashboardEmbedDirectory | Out-Null
    Copy-Item -Path (Join-Path $dashboardBuildDirectory "*") -Destination $dashboardEmbedDirectory -Recurse -Force

    $env:CGO_ENABLED = "0"
    $env:GOARCH = "amd64"
    $env:GOOS = "windows"
    $env:GOTOOLCHAIN = "local"

    & go -C runtime build -mod=readonly -trimpath -buildvcs=false -tags=dashboard "-ldflags=$linkerFlags" -o $binaryPath ./src/cmd/darkstar
    if ($LASTEXITCODE -ne 0) {
        throw "Go build failed with exit code $LASTEXITCODE."
    }

    $workflowDirectory = Join-Path $OutputDirectory "workflows"
    New-Item -ItemType Directory -Force -Path $workflowDirectory | Out-Null
    foreach ($workflowName in @("software-delivery.json", "story-execution.json")) {
        Copy-Item -LiteralPath (Join-Path $repositoryRoot "examples/workflows/$workflowName") -Destination (Join-Path $workflowDirectory $workflowName) -Force
    }
    $planningTemplateDirectory = Join-Path $OutputDirectory "templates/planning"
    New-Item -ItemType Directory -Force -Path $planningTemplateDirectory | Out-Null
    Get-ChildItem -LiteralPath (Join-Path $repositoryRoot "templates/planning") -File -Filter "*.md" | ForEach-Object {
        Copy-Item -LiteralPath $_.FullName -Destination (Join-Path $planningTemplateDirectory $_.Name) -Force
    }
    $schemaDirectory = Join-Path $OutputDirectory "schemas"
    New-Item -ItemType Directory -Force -Path $schemaDirectory | Out-Null
    foreach ($schemaName in @("planning-artifact-v1alpha1.schema.json", "delivery-evidence-v1alpha1.schema.json")) {
        Copy-Item -LiteralPath (Join-Path $repositoryRoot "schemas/$schemaName") -Destination $schemaDirectory -Force
    }
}
finally {
    Pop-Location
    foreach ($name in $previousGoEnvironment.Keys) {
        [Environment]::SetEnvironmentVariable($name, $previousGoEnvironment[$name], "Process")
    }
}

Write-Host "Built $binaryPath"
