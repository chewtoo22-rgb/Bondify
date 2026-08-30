<#
.SYNOPSIS
    Installs Bondify's Windows desktop client and connects it, in one step.

.DESCRIPTION
    Targets the phase 6 gate in ARCHITECTURE.md: "Windows install-to-bonded < 60s, one UAC
    prompt". The single UAC prompt comes from the self-elevation block below (a non-admin
    invocation re-launches itself elevated exactly once, rather than shelling out to further
    elevated sub-steps that would each prompt again); "install-to-bonded" covers copying the
    binary + wintun.dll into place, registering autostart, and actually starting the tunnel,
    timed and reported at the end.

    This script has not been run on a real Windows machine (see ARCHITECTURE.md §9 and
    README's "Verified so far (Phase 6)" section) -- it is included as real, intended-to-work
    installation logic, not as a verified one.

.PARAMETER RelayAddr
    Relay address, host:port. Required.

.PARAMETER RelayPubKey
    Relay's base64 public key. Required.

.PARAMETER InstallDir
    Where to install. Defaults to Program Files\Bondify.
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string]$RelayAddr,
    [Parameter(Mandatory = $true)][string]$RelayPubKey,
    [string]$InstallDir = "$env:ProgramFiles\Bondify"
)

$ErrorActionPreference = "Stop"
$start = Get-Date

# --- Single UAC prompt: re-launch elevated exactly once, then stop. ---------------------
$isAdmin = ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltinRole]::Administrator)
if (-not $isAdmin) {
    $argList = @(
        "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", "`"$PSCommandPath`"",
        "-RelayAddr", "`"$RelayAddr`"", "-RelayPubKey", "`"$RelayPubKey`"", "-InstallDir", "`"$InstallDir`""
    )
    $elevated = Start-Process -FilePath "powershell.exe" -ArgumentList $argList -Verb RunAs -Wait -PassThru
    exit $elevated.ExitCode
}

# --- Install files -------------------------------------------------------------------------
# bondify.exe ships alongside this script in a release archive (built via
# `GOOS=windows GOARCH=amd64 go build ./desktop/cmd/bondify`); this installer places files,
# it does not build them.
$scriptDir = Split-Path -Parent $PSCommandPath
$exeSrc = Join-Path $scriptDir "bondify.exe"
if (-not (Test-Path -LiteralPath $exeSrc -PathType Leaf)) {
    throw "bondify.exe not found next to install.ps1 (expected at $exeSrc)."
}

New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
Copy-Item -Force -LiteralPath $exeSrc -Destination (Join-Path $InstallDir "bondify.exe")

$wintunDst = Join-Path $InstallDir "wintun.dll"
if (-not (Test-Path -LiteralPath $wintunDst -PathType Leaf)) {
    # Prefer a release-bundled DLL when present. This keeps installs deterministic/offline
    # once packaging begins shipping the signed Wintun redistributable alongside Bondify.
    $wintunBundled = Join-Path $scriptDir "wintun.dll"
    if (Test-Path -LiteralPath $wintunBundled -PathType Leaf) {
        Copy-Item -Force -LiteralPath $wintunBundled -Destination $wintunDst
    } else {
        # Fallback for current release archives, which do not yet bundle Wintun.
        $wintunZipUrl = "https://www.wintun.net/builds/wintun-0.14.1.zip"
        $tmpRoot = Join-Path $env:TEMP ("bondify-wintun-" + [Guid]::NewGuid().ToString("N"))
        $tmpZip = Join-Path $tmpRoot "wintun.zip"
        $tmpDir = Join-Path $tmpRoot "extract"
        try {
            New-Item -ItemType Directory -Force -Path $tmpRoot | Out-Null
            Invoke-WebRequest -Uri $wintunZipUrl -OutFile $tmpZip -UseBasicParsing
            Expand-Archive -Force -LiteralPath $tmpZip -DestinationPath $tmpDir
            $arch = if ([Environment]::Is64BitOperatingSystem) { "amd64" } else { "x86" }
            $downloadedDll = Join-Path $tmpDir "wintun\bin\$arch\wintun.dll"
            if (-not (Test-Path -LiteralPath $downloadedDll -PathType Leaf)) {
                throw "Downloaded Wintun archive did not contain the expected $arch DLL."
            }
            Copy-Item -Force -LiteralPath $downloadedDll -Destination $wintunDst
        } catch {
            throw "Unable to provision required wintun.dll: $($_.Exception.Message)"
        } finally {
            Remove-Item -Recurse -Force -ErrorAction SilentlyContinue $tmpRoot
        }
    }
}

if (-not (Test-Path -LiteralPath $wintunDst -PathType Leaf)) {
    throw "Required wintun.dll is missing after provisioning; refusing to register or start Bondify."
}

# --- Autostart: a logon scheduled task running as the installing (admin) user. A Windows
# service would match ARCHITECTURE.md §3.2's "admin-elevated service + unelevated tray UI
# over a named pipe" design more closely, but that's a materially larger, harder-to-verify
# surface for this phase. -------------------------------------------------------------------
$taskName = "Bondify"
$action = New-ScheduledTaskAction -Execute (Join-Path $InstallDir "bondify.exe") `
    -Argument "-relay `"$RelayAddr`" -relay-pubkey `"$RelayPubKey`" -default-route"
$trigger = New-ScheduledTaskTrigger -AtLogOn
$principal = New-ScheduledTaskPrincipal -UserId $env:USERNAME -RunLevel Highest
Register-ScheduledTask -TaskName $taskName -Action $action -Trigger $trigger -Principal $principal -Force | Out-Null

# --- Connect now (this run), not just at next logon -----------------------------------------
Start-ScheduledTask -TaskName $taskName

# --- Confirm bonded: poll the diagnostics endpoint the same way the CLI's own -diag-addr
# flag exposes it, rather than trusting the process merely started. -------------------------
$bonded = $false
for ($i = 0; $i -lt 50; $i++) {
    try {
        $diag = Invoke-RestMethod -Uri "http://127.0.0.1:9090/api/v1/diagnostics" -TimeoutSec 1
        if ($null -ne $diag.paths -and $diag.paths.Count -ge 1) {
            $bonded = $true
            break
        }
    } catch {}
    Start-Sleep -Milliseconds 1000
}

$elapsed = (Get-Date) - $start
if (-not $bonded) {
    # A failed install must not leave a privileged autostart task repeatedly launching a
    # client that never reached the release gate. Best-effort cleanup, then fail the caller.
    try { Stop-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue } catch {}
    try { Unregister-ScheduledTask -TaskName $taskName -Confirm:$false -ErrorAction SilentlyContinue } catch {}
    throw "Bondify did not confirm a bonded session within 50s (elapsed $([math]::Round($elapsed.TotalSeconds, 1))s); scheduled-task autostart was removed."
}

Write-Host "Bondify installed and bonded in $([math]::Round($elapsed.TotalSeconds, 1))s."
exit 0
