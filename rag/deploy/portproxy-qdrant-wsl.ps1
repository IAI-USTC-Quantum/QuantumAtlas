# Run this on the Windows host (the one running WSL2 + Qdrant) as
# Administrator (PowerShell).
# Adds portproxy rules so EasyTier mesh peers can reach Qdrant
# (running in WSL2 Ubuntu, in non-mirrored networking mode).
#
# Pass the mesh IP this host should listen on, e.g.:
#   .\portproxy-qdrant-wsl.ps1 -MeshIp 10.0.0.10
#
# After this:
#   curl http://<mesh-ip>:6333/readyz   from any mesh peer should work.
#
# Re-run after a WSL reboot if the WSL IP changes (the script picks up the
# current WSL IP automatically).

param([Parameter(Mandatory)][string]$MeshIp)

$ErrorActionPreference = 'Stop'

# Look up the *current* WSL distro IP (first IP from eth0).
# Adjust the distro name if Ubuntu-24.04 isn't the default.
$wslDistro = 'Ubuntu-24.04'
$wslIp = (wsl.exe -d $wslDistro hostname -I).Trim().Split(' ')[0]
if (-not $wslIp) { throw "Failed to read WSL IP from distro '$wslDistro'." }
Write-Host "WSL distro IP detected: $wslIp"

$meshIp = $MeshIp

foreach ($port in 6333, 6334) {
    Write-Host "Adding portproxy: $meshIp`:$port -> $wslIp`:$port"
    # Remove any stale rule on this listenport first (idempotent re-run).
    netsh interface portproxy delete v4tov4 listenaddress=$meshIp listenport=$port 2>$null
    netsh interface portproxy add    v4tov4 listenaddress=$meshIp listenport=$port `
        connectaddress=$wslIp connectport=$port
}

Write-Host "`n=== current portproxy table ==="
netsh interface portproxy show v4tov4

Write-Host "`nVerify from a mesh peer with:"
Write-Host "  curl -fsS http://$meshIp`:6333/readyz   # expect: HTTP 200 or just 'ok'"
