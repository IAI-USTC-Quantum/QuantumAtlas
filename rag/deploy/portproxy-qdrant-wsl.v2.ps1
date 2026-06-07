#requires -RunAsAdministrator
<#
.SYNOPSIS
  Add Windows-host netsh portproxy rules so EasyTier mesh peers can reach
  Qdrant running in WSL2 (non-mirrored networking).

.DESCRIPTION
  Invoked with elevated permissions via:
      Start-Process pwsh -Verb RunAs -ArgumentList ...
  Writes progress to C:\Temp\qatlas-rag\result.txt; touches
  C:\Temp\qatlas-rag\done.txt as the completion sentinel so the WSL parent
  process can poll for it (WSL cannot read the elevated child's stdout
  directly — separate user context). See ${references/windows.md} in the
  `software` skill.

.PARAMETER WslIp
  The current WSL distro IP (eth0 inet, e.g. 172.28.94.43). REQUIRED;
  cannot be auto-detected from this script because invoking `wsl.exe`
  inside the elevated parent that was itself launched from WSL deadlocks
  the namespace.  Pass it from the WSL side via:
      wsl_ip=$(hostname -I | awk '{print $1}')

.EXAMPLE
  pwsh -NoProfile -File qatlas-rag-portproxy.ps1 -WslIp 172.28.94.43
#>

[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$WslIp,
    [Parameter(Mandatory)][string]$MeshIp,
    [int[]]$Ports = @(6333, 6334)
)

$ErrorActionPreference = 'Stop'

$stateDir  = 'C:\Temp\qatlas-rag'
$logFile   = Join-Path $stateDir 'result.txt'
$doneFile  = Join-Path $stateDir 'done.txt'

New-Item -ItemType Directory -Path $stateDir -Force | Out-Null
# Pre-clean the sentinel so the WSL poller doesn't see a stale OK.
Remove-Item -Path $doneFile -ErrorAction SilentlyContinue

Start-Transcript -Path $logFile -Force | Out-Null

try {
    Write-Output "qatlas-rag portproxy add"
    Write-Output "  mesh IP : $MeshIp"
    Write-Output "  WSL IP  : $WslIp"
    Write-Output "  ports   : $($Ports -join ',')"
    Write-Output ""

    foreach ($port in $Ports) {
        Write-Output "[$port] delete any stale rule"
        & netsh interface portproxy delete v4tov4 listenaddress=$MeshIp listenport=$port 2>&1 | Out-String -Stream

        Write-Output "[$port] add $MeshIp`:$port -> $WslIp`:$port"
        & netsh interface portproxy add v4tov4 listenaddress=$MeshIp listenport=$port connectaddress=$WslIp connectport=$port 2>&1 | Out-String -Stream
        if ($LASTEXITCODE -ne 0) { throw "netsh add (port $port) returned $LASTEXITCODE" }
    }

    Write-Output ""
    Write-Output "=== current portproxy table (may be GBK-encoded by netsh on Chinese Windows; consumers should iconv) ==="
    & netsh interface portproxy show all 2>&1 | Out-String -Stream

    Write-Output ""
    Write-Output "DONE OK"
    'OK' | Out-File -FilePath $doneFile -Encoding utf8 -NoNewline
}
catch {
    $msg = "ERROR: $($_.Exception.Message)"
    Write-Output $msg
    $msg | Out-File -FilePath $doneFile -Encoding utf8 -NoNewline
    exit 1
}
finally {
    Stop-Transcript | Out-Null
}
