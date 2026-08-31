[CmdletBinding()]
param([string]$OutputPath)

$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

if ($env:OS -ne "Windows_NT") { throw "DS-009 probe requires Windows" }

Add-Type -TypeDefinition @"
using System;
using System.Runtime.InteropServices;

public static class DarkstarJobObject {
    [StructLayout(LayoutKind.Sequential)]
    private struct IO_COUNTERS {
        public UInt64 ReadOperationCount, WriteOperationCount, OtherOperationCount;
        public UInt64 ReadTransferCount, WriteTransferCount, OtherTransferCount;
    }
    [StructLayout(LayoutKind.Sequential)]
    private struct JOBOBJECT_BASIC_LIMIT_INFORMATION {
        public Int64 PerProcessUserTimeLimit, PerJobUserTimeLimit;
        public UInt32 LimitFlags;
        public UIntPtr MinimumWorkingSetSize, MaximumWorkingSetSize;
        public UInt32 ActiveProcessLimit;
        public UIntPtr Affinity;
        public UInt32 PriorityClass, SchedulingClass;
    }
    [StructLayout(LayoutKind.Sequential)]
    private struct JOBOBJECT_EXTENDED_LIMIT_INFORMATION {
        public JOBOBJECT_BASIC_LIMIT_INFORMATION BasicLimitInformation;
        public IO_COUNTERS IoInfo;
        public UIntPtr ProcessMemoryLimit, JobMemoryLimit, PeakProcessMemoryUsed, PeakJobMemoryUsed;
    }
    [DllImport("kernel32.dll", CharSet = CharSet.Unicode, SetLastError = true)]
    private static extern IntPtr CreateJobObject(IntPtr attributes, string name);
    [DllImport("kernel32.dll", SetLastError = true)]
    private static extern bool SetInformationJobObject(IntPtr job, int infoClass, IntPtr info, uint length);
    [DllImport("kernel32.dll", SetLastError = true)]
    public static extern bool AssignProcessToJobObject(IntPtr job, IntPtr process);
    [DllImport("kernel32.dll", SetLastError = true)]
    public static extern bool CloseHandle(IntPtr handle);

    public static IntPtr CreateKillOnClose() {
        IntPtr job = CreateJobObject(IntPtr.Zero, null);
        if (job == IntPtr.Zero) throw new System.ComponentModel.Win32Exception(Marshal.GetLastWin32Error());
        var info = new JOBOBJECT_EXTENDED_LIMIT_INFORMATION();
        info.BasicLimitInformation.LimitFlags = 0x00002000;
        int size = Marshal.SizeOf(info);
        IntPtr pointer = Marshal.AllocHGlobal(size);
        try {
            Marshal.StructureToPtr(info, pointer, false);
            if (!SetInformationJobObject(job, 9, pointer, (uint)size))
                throw new System.ComponentModel.Win32Exception(Marshal.GetLastWin32Error());
            return job;
        } catch { CloseHandle(job); throw; }
        finally { Marshal.FreeHGlobal(pointer); }
    }
}
"@

function Invoke-Check([string]$Name, [scriptblock]$Body) {
  try {
    $detail = & $Body
    [pscustomobject]@{ name = $Name; passed = $true; detail = $detail }
  } catch {
    [pscustomobject]@{ name = $Name; passed = $false; detail = $_.Exception.Message }
  }
}

function Test-ProcessIdentity([pscustomobject]$Identity) {
  try {
    $process = [System.Diagnostics.Process]::GetProcessById($Identity.pid)
    return $process.StartTime.ToUniversalTime().ToString("O") -eq $Identity.startTimeUtc
  } catch { return $false }
}

$probeRoot = Join-Path ([System.IO.Path]::GetTempPath()) ("darkstar-ds009-" + [guid]::NewGuid().ToString("N"))
[System.IO.Directory]::CreateDirectory($probeRoot) | Out-Null
$resolvedRoot = [System.IO.Path]::GetFullPath($probeRoot)
$tempRoot = [System.IO.Path]::GetFullPath([System.IO.Path]::GetTempPath())
if (-not $resolvedRoot.StartsWith($tempRoot, [System.StringComparison]::OrdinalIgnoreCase)) { throw "Unsafe probe root" }

try {
  $shell = (Get-Command pwsh -ErrorAction Stop).Source
  $scriptRoot = $PSScriptRoot
  $checks = @()

  $checks += Invoke-Check "application-data-path" {
    $local = [Environment]::GetFolderPath([Environment+SpecialFolder]::LocalApplicationData)
    if (-not [System.IO.Path]::IsPathFullyQualified($local)) { throw "LocalApplicationData is not absolute" }
    Join-Path $local "DARKSTAR"
  }

  $checks += Invoke-Check "exclusive-lock" {
    $lockPath = Join-Path $probeRoot "daemon.lock"
    $first = [System.IO.File]::Open($lockPath, "OpenOrCreate", "ReadWrite", "None")
    try {
      $blocked = $false
      try { $second = [System.IO.File]::Open($lockPath, "Open", "ReadWrite", "None"); $second.Dispose() }
      catch { $blocked = $true }
      if (-not $blocked) { throw "Second lock owner was admitted" }
      "sharing violation observed"
    } finally { $first.Dispose() }
  }

  $checks += Invoke-Check "endpoint-state" {
    $listener = [System.Net.Sockets.TcpListener]::new([System.Net.IPAddress]::Loopback, 0)
    $listener.Start()
    try {
      $token = [System.Security.Cryptography.RandomNumberGenerator]::GetBytes(32)
      $state = [pscustomobject]@{ schemaVersion=1; pid=$PID; processStartTime=([System.Diagnostics.Process]::GetCurrentProcess().StartTime.ToUniversalTime().ToString("O")); port=([System.Net.IPEndPoint]$listener.LocalEndpoint).Port; token=[Convert]::ToBase64String($token) }
      $json = $state | ConvertTo-Json -Compress
      $path = Join-Path $probeRoot "endpoint.json"
      [System.IO.File]::WriteAllText($path + ".tmp", $json)
      [System.IO.File]::Move($path + ".tmp", $path, $true)
      $read = Get-Content -Raw -LiteralPath $path | ConvertFrom-Json
      if ($read.port -le 0 -or [Convert]::FromBase64String($read.token).Length -ne 32) { throw "Invalid endpoint state" }
      "loopback=$($listener.LocalEndpoint); tokenBytes=32"
    } finally { $listener.Stop() }
  }

  $checks += Invoke-Check "atomic-replace-sharing-interference" {
    $path = Join-Path $probeRoot "state.json"
    [System.IO.File]::WriteAllText($path, "old")
    $held = [System.IO.File]::Open($path, "Open", "Read", "None")
    $blocked = $false
    try {
      [System.IO.File]::WriteAllText($path + ".tmp", "new")
      try { [System.IO.File]::Move($path + ".tmp", $path, $true) }
      catch { $blocked = $true }
    } finally { $held.Dispose() }
    if (-not $blocked -or [System.IO.File]::ReadAllText($path) -ne "old") { throw "Atomic replace did not preserve old state" }
    [System.IO.File]::Move($path + ".tmp", $path, $true)
    if ([System.IO.File]::ReadAllText($path) -ne "new") { throw "Retry did not publish new state" }
    "old state preserved, retry succeeded"
  }

  $checks += Invoke-Check "process-identity-and-stale-state" {
    $current = [System.Diagnostics.Process]::GetCurrentProcess()
    $identity = [pscustomobject]@{ pid=$current.Id; startTimeUtc=$current.StartTime.ToUniversalTime().ToString("O") }
    if (-not (Test-ProcessIdentity $identity)) { throw "Current identity was not recognized" }
    if (Test-ProcessIdentity ([pscustomobject]@{ pid=2147483647; startTimeUtc="2000-01-01T00:00:00.0000000Z" })) { throw "Stale identity was accepted" }
    "PID plus creation-time match"
  }

  $checks += Invoke-Check "job-object-tree-kill" {
    $treeRoot = Join-Path $probeRoot "tree"
    [System.IO.Directory]::CreateDirectory($treeRoot) | Out-Null
    $parentScript = Join-Path $scriptRoot "OwnedProcessTree.ps1"
    $job = [DarkstarJobObject]::CreateKillOnClose()
    $parent = $null
    try {
      $arguments = '-NoProfile -NonInteractive -File "{0}" -Root "{1}" -Shell "{2}"' -f $parentScript, $treeRoot, $shell
      $parent = Start-Process -FilePath $shell -ArgumentList $arguments -PassThru -WindowStyle Hidden
      if (-not [DarkstarJobObject]::AssignProcessToJobObject($job, $parent.Handle)) { throw "AssignProcessToJobObject failed: $([Runtime.InteropServices.Marshal]::GetLastWin32Error())" }
      [System.IO.File]::WriteAllText((Join-Path $treeRoot "gate"), "go")
      $pidsPath = Join-Path $treeRoot "pids.json"
      $deadline = [DateTime]::UtcNow.AddSeconds(10)
      while (-not (Test-Path -LiteralPath $pidsPath) -and [DateTime]::UtcNow -lt $deadline) { Start-Sleep -Milliseconds 20 }
      if (-not (Test-Path -LiteralPath $pidsPath)) { throw "Process tree did not start" }
      $pids = Get-Content -Raw -LiteralPath $pidsPath | ConvertFrom-Json
      if (-not (Get-Process -Id $pids.childPid -ErrorAction SilentlyContinue)) { throw "Child was not running" }
      [DarkstarJobObject]::CloseHandle($job) | Out-Null
      $job = [IntPtr]::Zero
      $deadline = [DateTime]::UtcNow.AddSeconds(10)
      while ((Get-Process -Id $pids.parentPid,$pids.childPid -ErrorAction SilentlyContinue) -and [DateTime]::UtcNow -lt $deadline) { Start-Sleep -Milliseconds 25 }
      $survivors = @(Get-Process -Id $pids.parentPid,$pids.childPid -ErrorAction SilentlyContinue)
      if ($survivors.Count -ne 0) { throw "Owned process survived job close: $($survivors.Id -join ',')" }
      "parent=$($pids.parentPid), child=$($pids.childPid), survivors=0"
    } finally {
      if ($job -ne [IntPtr]::Zero) { [DarkstarJobObject]::CloseHandle($job) | Out-Null }
      if ($parent -and -not $parent.HasExited) { $parent.Kill($true) }
    }
  }

  $checks += Invoke-Check "graceful-stop-owned-tree" {
    $treeRoot = Join-Path $probeRoot "graceful-tree"
    [System.IO.Directory]::CreateDirectory($treeRoot) | Out-Null
    $parentScript = Join-Path $scriptRoot "OwnedProcessTree.ps1"
    $job = [DarkstarJobObject]::CreateKillOnClose()
    $parent = $null
    try {
      $arguments = '-NoProfile -NonInteractive -File "{0}" -Root "{1}" -Shell "{2}"' -f $parentScript, $treeRoot, $shell
      $parent = Start-Process -FilePath $shell -ArgumentList $arguments -PassThru -WindowStyle Hidden
      if (-not [DarkstarJobObject]::AssignProcessToJobObject($job, $parent.Handle)) { throw "AssignProcessToJobObject failed" }
      [System.IO.File]::WriteAllText((Join-Path $treeRoot "gate"), "go")
      $pidsPath = Join-Path $treeRoot "pids.json"
      $deadline = [DateTime]::UtcNow.AddSeconds(10)
      while (-not (Test-Path -LiteralPath $pidsPath) -and [DateTime]::UtcNow -lt $deadline) { Start-Sleep -Milliseconds 20 }
      if (-not (Test-Path -LiteralPath $pidsPath)) { throw "Process tree did not start" }
      $pids = Get-Content -Raw -LiteralPath $pidsPath | ConvertFrom-Json
      [System.IO.File]::WriteAllText((Join-Path $treeRoot "stop"), "stop")
      $deadline = [DateTime]::UtcNow.AddSeconds(10)
      while ((Get-Process -Id $pids.parentPid,$pids.childPid -ErrorAction SilentlyContinue) -and [DateTime]::UtcNow -lt $deadline) { Start-Sleep -Milliseconds 25 }
      $survivors = @(Get-Process -Id $pids.parentPid,$pids.childPid -ErrorAction SilentlyContinue)
      if ($survivors.Count -ne 0) { throw "Owned process survived graceful stop: $($survivors.Id -join ',')" }
      "shutdown signal converged; survivors=0"
    } finally {
      [DarkstarJobObject]::CloseHandle($job) | Out-Null
      if ($parent -and -not $parent.HasExited) { $parent.Kill($true) }
    }
  }

  $checks += Invoke-Check "long-path" {
    $path = $probeRoot
    while ($path.Length -lt 300) { $path = Join-Path $path "segment-0123456789" }
    [System.IO.Directory]::CreateDirectory($path) | Out-Null
    $file = Join-Path $path "probe.txt"
    [System.IO.File]::WriteAllText($file, "ok")
    if ([System.IO.File]::ReadAllText($file) -ne "ok") { throw "Long path round trip failed" }
    "length=$($file.Length)"
  }

  $checks += Invoke-Check "executable-discovery" {
    $command = Get-Command pwsh -CommandType Application -ErrorAction Stop
    $canonical = [System.IO.Path]::GetFullPath($command.Source)
    if (-not [System.IO.Path]::IsPathFullyQualified($canonical) -or -not (Test-Path -LiteralPath $canonical)) { throw "Executable was not canonical" }
    "path=$canonical; version=$($command.Version)"
  }

  $result = [pscustomobject]@{
    schemaVersion = 1
    decision = "DS-009"
    platform = [Environment]::OSVersion.VersionString
    powershell = $PSVersionTable.PSVersion.ToString()
    passed = @($checks | Where-Object { -not $_.passed }).Count -eq 0
    checks = $checks
  }
  $json = $result | ConvertTo-Json -Depth 6
  if ($OutputPath) { [System.IO.File]::WriteAllText([System.IO.Path]::GetFullPath($OutputPath), $json) }
  $json
  if (-not $result.passed) { exit 1 }
} finally {
  if ([System.IO.Directory]::Exists($resolvedRoot) -and $resolvedRoot.StartsWith($tempRoot, [System.StringComparison]::OrdinalIgnoreCase)) {
    Remove-Item -LiteralPath $resolvedRoot -Recurse -Force -ErrorAction SilentlyContinue
  }
}
