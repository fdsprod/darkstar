# DS-009 Windows process-control probe

Run on a supported 64-bit Windows host with PowerShell 7:

```powershell
./probes/windows-process-control/Invoke-WindowsProcessProbe.ps1
./probes/windows-process-control/Test-WindowsProcessProbe.ps1
```

The probe uses a kill-on-close Job Object and temporary processes. Its child
sleeps for at most two minutes, but a passing run closes the job within seconds.
All generated files are isolated below the current user's temporary directory.
No service registration, firewall rule, persistent daemon state, or repository
outside the probe is modified.
