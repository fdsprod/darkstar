param(
  [Parameter(Mandatory)][string]$Root,
  [Parameter(Mandatory)][string]$Shell
)

$ErrorActionPreference = "Stop"
$gate = Join-Path $Root "gate"
$stop = Join-Path $Root "stop"
$pids = Join-Path $Root "pids.json"

while (-not (Test-Path -LiteralPath $gate)) { Start-Sleep -Milliseconds 20 }
$child = Start-Process -FilePath $Shell -ArgumentList '-NoProfile -NonInteractive -Command "Start-Sleep -Seconds 120"' -PassThru -WindowStyle Hidden
@{ parentPid = $PID; childPid = $child.Id } | ConvertTo-Json -Compress | Set-Content -LiteralPath $pids -Encoding utf8

try {
  while (-not (Test-Path -LiteralPath $stop)) {
    if ($child.HasExited) { exit $child.ExitCode }
    Start-Sleep -Milliseconds 20
  }
} finally {
  if (-not $child.HasExited) {
    $child.Kill($true)
    $child.WaitForExit(5000) | Out-Null
  }
}
