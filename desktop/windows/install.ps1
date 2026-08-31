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
    Relay's canonical base64-encoded 32-byte public key. Required.

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

function Assert-RelayAddress {
    param([Parameter(Mandatory = $true)][string]$Value)

    if ($Value.Length -eq 0 -or $Value.Length -gt 300) {
        throw "RelayAddr must be a bounded host:port value."
    }

    $match = [regex]::Match(
        $Value,
        '^(?<host>\[[0-9A-Fa-f:.]+\]|[A-Za-z0-9](?:[A-Za-z0-9.-]{0,251}[A-Za-z0-9])?):(?<port>[0-9]{1,5})$'
    )
    if (-not $match.Success) {
        throw "RelayAddr must be host:port with no whitespace or command-line metacharacters."
    }

    $port = 0
    if (-not [int]::TryParse($match.Groups['port'].Value, [ref]$port) -or $port -lt 1 -or $port -gt 65535) {
        throw "RelayAddr port must be in the range 1..65535."
    }

    # PowerShell variable names are case-insensitive and $Host is a built-in read-only
    # automatic variable. Use a non-reserved name so the pure admission function behaves
    # identically under the Windows runner and when dot-sourced by contract tests.
    $relayHost = $match.Groups['host'].Value
    if ($relayHost.StartsWith('[')) {
        $ip = $null
        if (-not [System.Net.IPAddress]::TryParse($relayHost.Substring(1, $relayHost.Length - 2), [ref]$ip) -or $ip.AddressFamily -ne [System.Net.Sockets.AddressFamily]::InterNetworkV6) {
            throw "RelayAddr bracketed host must be a valid IPv6 literal."
        }
        return
    }

    if ($relayHost -match '^[0-9.]+$') {
        $ip = $null
        if (-not [System.Net.IPAddress]::TryParse($relayHost, [ref]$ip) -or $ip.AddressFamily -ne [System.Net.Sockets.AddressFamily]::InterNetwork) {
            throw "RelayAddr numeric host must be a valid IPv4 literal."
        }
        return
    }

    foreach ($label in $relayHost.Split('.')) {
        if ($label.Length -lt 1 -or $label.Length -gt 63 -or $label -notmatch '^[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?$') {
            throw "RelayAddr contains an invalid DNS label."
        }
    }
}

function Assert-RelayPublicKey {
    param([Parameter(Mandatory = $true)][string]$Value)

    if ($Value.Length -eq 0 -or $Value.Length -gt 64 -or $Value -match '\s') {
        throw "RelayPubKey must be a bounded canonical base64 value with no whitespace."
    }

    try {
        $decoded = [Convert]::FromBase64String($Value)
    } catch {
        throw "RelayPubKey must be valid base64."
    }

    if ($decoded.Length -ne 32) {
        throw "RelayPubKey must decode to exactly 32 bytes."
    }
    if ([Convert]::ToBase64String($decoded) -cne $Value) {
        throw "RelayPubKey must use canonical base64 encoding."
    }
}

# Validate every value later embedded in an elevated process or scheduled-task command line
# before requesting elevation. This keeps malformed or command-like input out of the
# privileged boundary rather than relying on quoting after the fact.
Assert-RelayAddress -Value $RelayAddr
Assert-RelayPublicKey -Value $RelayPubKey

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
