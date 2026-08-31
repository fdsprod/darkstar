$ErrorActionPreference = "Stop"
$probe = Join-Path $PSScriptRoot "Invoke-WindowsProcessProbe.ps1"
$result = (& $probe) | ConvertFrom-Json

$required = @(
  "application-data-path",
  "exclusive-lock",
  "endpoint-state",
  "atomic-replace-sharing-interference",
  "process-identity-and-stale-state",
  "job-object-tree-kill",
  "graceful-stop-owned-tree",
  "long-path",
  "executable-discovery"
)

if (-not $result.passed) { throw "DS-009 probe reported failure" }
$names = @($result.checks.name)
foreach ($name in $required) {
  if ($name -notin $names) { throw "Missing required probe: $name" }
  $check = $result.checks | Where-Object name -eq $name
  if (-not $check.passed) { throw "Failed probe: $name ($($check.detail))" }
}

"DS-009 Windows process-control contract passed $($required.Count) checks."
