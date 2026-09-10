[CmdletBinding()]
param([string]$GoRoot, [string]$NodeRoot, [switch]$Reinstall)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"
if (-not $IsWindows) { throw "The portable toolchain installer currently supports Windows." }
$projectRoot = Split-Path -Parent $PSScriptRoot
$goVersion = (Get-Content -LiteralPath (Join-Path $projectRoot ".go-version") -Raw).Trim()
$nodeVersion = (Get-Content -LiteralPath (Join-Path $projectRoot ".node-version") -Raw).Trim()
$npmVersion = (Get-Content -LiteralPath (Join-Path $projectRoot ".npm-version") -Raw).Trim()
foreach ($version in @($goVersion, $nodeVersion, $npmVersion)) { if ($version -notmatch '^\d+\.\d+\.\d+$') { throw "Invalid toolchain version pin: $version" } }
$cache = Join-Path $projectRoot ".darkstar/toolchains"
New-Item -ItemType Directory -Force -Path $cache | Out-Null

function Expand-VerifiedDownload([string]$Url, [string]$Hash, [string]$Name, [string]$Destination) {
    if ($Hash -notmatch '^[a-fA-F0-9]{64}$') { throw "Missing official checksum for $Name" }
    $archive = Join-Path $cache $Name
    if (-not (Test-Path -LiteralPath $archive) -or (Get-FileHash -LiteralPath $archive -Algorithm SHA256).Hash -ne $Hash) {
        Invoke-WebRequest -Uri $Url -OutFile $archive
    }
    if ((Get-FileHash -LiteralPath $archive -Algorithm SHA256).Hash -ne $Hash) { throw "Checksum mismatch for $Name; no files were installed." }
    Expand-Archive -LiteralPath $archive -DestinationPath $Destination -Force
}

if (-not $GoRoot) {
    $destination = Join-Path $cache "go-$goVersion"
    $GoRoot = Join-Path $destination "go"
    if (-not $Reinstall -and (Test-Path -LiteralPath (Join-Path $destination "bin/go.exe"))) { $GoRoot = $destination }
    if ($Reinstall -or -not (Test-Path -LiteralPath (Join-Path $GoRoot "bin/go.exe"))) {
        $filename = "go$goVersion.windows-amd64.zip"
        $releases = Invoke-RestMethod -Uri 'https://go.dev/dl/?mode=json&include=all'
        $file = @($releases | Where-Object version -eq "go$goVersion" | ForEach-Object files | Where-Object filename -eq $filename)
        if ($file.Count -ne 1) { throw "Official Go release $goVersion was not found." }
        Expand-VerifiedDownload "https://go.dev/dl/$filename" $file[0].sha256 $filename $destination
    }
}
if (-not $NodeRoot) {
    $NodeRoot = Join-Path $cache "node-v$nodeVersion-win-x64"
    if ($Reinstall -or -not (Test-Path -LiteralPath (Join-Path $NodeRoot "node_modules/npm/bin/npx-cli.js"))) {
        $filename = "node-v$nodeVersion-win-x64.zip"
        $checksums = (Invoke-WebRequest -Uri "https://nodejs.org/dist/v$nodeVersion/SHASUMS256.txt").Content
        $match = [regex]::Match($checksums, "(?m)^([a-fA-F0-9]{64})\s+$([regex]::Escape($filename))\r?$" )
        if (-not $match.Success) { throw "Official Node checksum was not found for $filename." }
        Expand-VerifiedDownload "https://nodejs.org/dist/v$nodeVersion/$filename" $match.Groups[1].Value $filename $cache
    }
}
$GoRoot = (Resolve-Path -LiteralPath $GoRoot).Path
$NodeRoot = (Resolve-Path -LiteralPath $NodeRoot).Path
$previousRoot = [Environment]::GetEnvironmentVariable("GOROOT", "Process")
$previousToolchain = [Environment]::GetEnvironmentVariable("GOTOOLCHAIN", "Process")
try {
    $env:GOROOT = $GoRoot; $env:GOTOOLCHAIN = "local"
    $goOutput = & (Join-Path $GoRoot "bin/go.exe") version
    if ($LASTEXITCODE -ne 0 -or $goOutput -notmatch '^go version go(\d+\.\d+\.\d+)\s' -or [version]$Matches[1] -lt [version]$goVersion) { throw "Selected Go must be >= $goVersion : $goOutput" }
    $nodeOutput = & (Join-Path $NodeRoot "node.exe") --version
    if ($LASTEXITCODE -ne 0 -or $nodeOutput -notmatch '^v(\d+\.\d+\.\d+)$' -or [version]$Matches[1] -lt [version]$nodeVersion) { throw "Selected Node must be >= $nodeVersion : $nodeOutput" }
    foreach ($launcher in @("npm", "npx")) {
        if (-not (Test-Path -LiteralPath (Join-Path $NodeRoot "$launcher.cmd"))) { throw "Selected Node installation has no $launcher.cmd." }
        $output = & (Join-Path $NodeRoot "node.exe") (Join-Path $NodeRoot "node_modules/npm/bin/$launcher-cli.js") --version
        if ($LASTEXITCODE -ne 0 -or $output -notmatch '^\d+\.\d+\.\d+$' -or [version]$output -lt [version]$npmVersion) { throw "Selected $launcher must be >= $npmVersion : $output" }
    }
    $bindingPath = Join-Path $projectRoot ".darkstar/toolchains.json"
    $pending = "$bindingPath.$([guid]::NewGuid().ToString('N')).tmp"
    @{schemaVersion=1;goRoot=$GoRoot;nodeRoot=$NodeRoot} | ConvertTo-Json | Set-Content -LiteralPath $pending -Encoding utf8
    [IO.File]::Move($pending, $bindingPath, $true)
    Write-Host "Project toolchains saved: $goOutput, Node $nodeOutput, npm/npx $output."
}
finally {
    [Environment]::SetEnvironmentVariable("GOROOT", $previousRoot, "Process")
    [Environment]::SetEnvironmentVariable("GOTOOLCHAIN", $previousToolchain, "Process")
}
