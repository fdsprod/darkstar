[CmdletBinding()]
param([switch]$GoOnly)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"
$projectRoot = Split-Path -Parent $PSScriptRoot
$bindingPath = Join-Path $projectRoot ".darkstar/toolchains.json"
if (-not (Test-Path -LiteralPath $bindingPath)) {
    # A daemon-launched worktree consumes its owning project's local binding.
    $inheritedBinding = [Environment]::GetEnvironmentVariable("DARKSTAR_TOOLCHAIN_BINDING", "Process")
    if ($inheritedBinding) { $bindingPath = $inheritedBinding }
    else {
        $commonDirectory = & git -C $projectRoot rev-parse --path-format=absolute --git-common-dir 2>$null
        if ($LASTEXITCODE -eq 0 -and $commonDirectory) { $bindingPath = Join-Path (Split-Path -Parent $commonDirectory.Trim()) ".darkstar/toolchains.json" }
    }
}
if (-not (Test-Path -LiteralPath $bindingPath -PathType Leaf)) {
    throw "Project toolchains are not configured. Run ./scripts/Setup-Toolchain.ps1 once, then retry."
}
$binding = Get-Content -LiteralPath $bindingPath -Raw | ConvertFrom-Json
if ($binding.schemaVersion -ne 1 -or -not [IO.Path]::IsPathRooted($binding.goRoot) -or -not [IO.Path]::IsPathRooted($binding.nodeRoot)) { throw "Invalid project toolchain binding. Run ./scripts/Setup-Toolchain.ps1." }
$goBin = Join-Path $binding.goRoot "bin"
$directories = @($goBin)
if (-not $GoOnly) { $directories += $binding.nodeRoot }
$remainingPath = @($env:PATH -split [IO.Path]::PathSeparator | Where-Object { $_ -and $_ -notin $directories })
$env:PATH = (@($directories) + $remainingPath) -join [IO.Path]::PathSeparator
$env:GOROOT = $binding.goRoot
$env:GOTOOLCHAIN = "local"
$env:DARKSTAR_TOOLCHAIN_BINDING = $bindingPath
$cacheRoot = Join-Path $projectRoot ".darkstar/cache"
foreach ($entry in @{GOCACHE="go-build"; GOMODCACHE="go-mod"; npm_config_cache="npm"}.GetEnumerator()) {
    $cacheDirectory = Join-Path $cacheRoot $entry.Value
    New-Item -ItemType Directory -Force -Path $cacheDirectory | Out-Null
    [Environment]::SetEnvironmentVariable($entry.Key, $cacheDirectory, "Process")
}
foreach ($tool in @("go.exe", "gofmt.exe")) {
    if (-not (Test-Path -LiteralPath (Join-Path $goBin $tool) -PathType Leaf)) { throw "Pinned $tool is missing. Run ./scripts/Setup-Toolchain.ps1." }
}
if (-not $GoOnly) {
    foreach ($tool in @("node.exe", "npm.cmd", "npx.cmd", "node_modules/npm/bin/npm-cli.js", "node_modules/npm/bin/npx-cli.js")) {
        if (-not (Test-Path -LiteralPath (Join-Path $binding.nodeRoot $tool) -PathType Leaf)) { throw "Pinned $tool is missing. Run ./scripts/Setup-Toolchain.ps1." }
    }
}
