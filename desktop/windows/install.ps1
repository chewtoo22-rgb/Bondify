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

function Assert-RelayInputs {
    param(
        [Parameter(Mandatory = $true)][string]$Address,
        [Parameter(Mandatory = $true)][string]$PublicKey
    )

    if ($Address.Length -gt 255 -or $Address -match '[\x00-\x1F\x7F\s]') {
        throw "RelayAddr contains whitespace/control characters or exceeds 255 characters."
    }

    $host = $null
    $port = $null
    if ($Address -match '^\[(?<ipv6>[0-9A-Fa-f:]+)\]:(?<port>[0-9]{1,5})$') {
        $host = $Matches.ipv6
        $port = [int]$Matches.port
    } elseif ($Address -match '^(?<hostname>[A-Za-z0-9][A-Za-z0-9.-]*):(?<port>[0-9]{1,5})$') {
        $host = $Matches.hostname
        $port = [int]$Matches.port
    } else {
        throw "RelayAddr must be host:port or [IPv6]:port."
    }

    if ($port -lt 1 -or $port -gt 65535) {
        throw "RelayAddr port must be in 1..65535."
    }
    if ([string]::IsNullOrWhiteSpace($host) -or $host.StartsWith('.') -or $host.EndsWith('.')) {
        throw "RelayAddr host is malformed."
    }

    if ($PublicKey.Length -ne 44 -or $PublicKey -notmatch '^[A-Za-z0-9+/]{43}=$') {
        throw "RelayPubKey must be a canonical base64-encoded 32-byte public key."
    }
    try {
        $decoded = [Convert]::FromBase64String($PublicKey)
    } catch {
        throw "RelayPubKey is not valid base64."
    }
    if ($decoded.Length -ne 32) {
        throw "RelayPubKey must decode to exactly 32 bytes."
    }
}

Assert-RelayInputs -Address $RelayAddr -PublicKey $RelayPubKey

# --- Single UAC prompt: re-launch elevated exactly once, then stop. ---------------------
$isAdmin = ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltinRole]::Administrator)
if (-not $isAdmin) {
    $argList = @(
        "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", "`"$PSCommandPath`"",
        "-RelayAddr", "`"$RelayAddr`"", "-RelayPubKey", "`"$RelayPubKey`"", "-InstallDir", "`"$InstallDir`""
    )
    Start-Process -FilePath "powershell.exe" -ArgumentList $argList -Verb RunAs -Wait
    exit $LASTEXITCODE
}

# --- Install files -------------------------------------------------------------------------
# bondify.exe ships alongside this script in a release archive (built via
# `GOOS=windows GOARCH=amd64 go build ./desktop/cmd/bondify`); this installer places files,
# it does not build them -- Bondify has no packaged release yet (phase 9 scope).
$scriptDir = Split-Path -Parent $PSCommandPath
$exeSrc = Join-Path $scriptDir "bondify.exe"
if (-not (Test-Path $exeSrc)) {
    Write-Error "bondify.exe not found next to install.ps1 (expected at $exeSrc). Build it first: GOOS=windows GOARCH=amd64 go build -o bondify.exe ./desktop/cmd/bondify"
    exit 1
}

New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
Copy-Item -Force $exeSrc (Join-Path $InstallDir "bondify.exe")

$wintunDst = Join-Path $InstallDir "wintun.dll"
if (-not (Test-Path $wintunDst)) {
    # wireguard-go's Windows tun package (golang.zx2c4.com/wintun) LoadLibraryEx-searches
    # the application directory and System32 for wintun.dll, in that order -- it does not
    # bundle the DLL itself (it's a separate signed driver artifact from the WireGuard
    # project, not something to vendor into this repo). This downloads the official
    # redistributable zip and extracts the matching architecture's DLL; the fetch itself
    # (URL, extraction path inside the zip) has not been exercised in this sandbox --
    # no outbound network access here to confirm it end-to-end. If this step fails,
    # download wintun.dll manually from https://www.wintun.net and place it in $InstallDir.
    $wintunZipUrl = "https://www.wintun.net/builds/wintun-0.14.1.zip"
    $tmpZip = Join-Path $env:TEMP "wintun.zip"
    $tmpDir = Join-Path $env:TEMP "wintun-extract"
    try {
        Invoke-WebRequest -Uri $wintunZipUrl -OutFile $tmpZip -UseBasicParsing
        Expand-Archive -Force -Path $tmpZip -DestinationPath $tmpDir
        $arch = if ([Environment]::Is64BitOperatingSystem) { "amd64" } else { "x86" }
        Copy-Item -Force (Join-Path $tmpDir "wintun\bin\$arch\wintun.dll") $wintunDst
    } catch {
        Write-Warning "Could not download wintun.dll automatically ($_). Place it manually at $wintunDst before continuing."
    } finally {
        Remove-Item -Force -ErrorAction SilentlyContinue $tmpZip
        Remove-Item -Recurse -Force -ErrorAction SilentlyContinue $tmpDir
    }
}

# --- Autostart: a logon scheduled task running as the installing (admin) user. A Windows
# service would match ARCHITECTURE.md §3.2's "admin-elevated service + unelevated tray UI
# over a named pipe" design more closely, but that's a materially larger, harder-to-verify
# surface (service install/start/stop lifecycle, a named-pipe IPC protocol between two
# processes) for a phase whose actual gate is install time and UAC-prompt count, not process
# architecture -- tracked as a known deviation in ARCHITECTURE.md §9, to revisit if a real
# Windows test pass surfaces problems this simpler model can't solve. -----------------------
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
        if ($diag.paths.Count -ge 1) { $bonded = $true; break }
    } catch {}
    Start-Sleep -Milliseconds 1000
}

$elapsed = (Get-Date) - $start
if ($bonded) {
    Write-Host "Bondify installed and bonded in $([math]::Round($elapsed.TotalSeconds, 1))s."
} else {
    Write-Warning "Bondify installed but did not confirm a bonded session within 50s (elapsed $([math]::Round($elapsed.TotalSeconds, 1))s). Check the scheduled task 'Bondify' and its process output."
}
