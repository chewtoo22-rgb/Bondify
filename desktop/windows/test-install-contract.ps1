param(
    [string]$InstallerPath = (Join-Path $PSScriptRoot 'install.ps1')
)

$ErrorActionPreference = 'Stop'

if (-not (Test-Path -LiteralPath $InstallerPath -PathType Leaf)) {
    throw "Installer not found: $InstallerPath"
}

$tokens = $null
$errors = $null
$ast = [System.Management.Automation.Language.Parser]::ParseFile(
    (Resolve-Path -LiteralPath $InstallerPath),
    [ref]$tokens,
    [ref]$errors
)
if ($errors.Count -gt 0) {
    $messages = ($errors | ForEach-Object Message) -join '; '
    throw "Installer failed PowerShell parser validation: $messages"
}

$source = Get-Content -Raw -LiteralPath $InstallerPath

function Require-Pattern {
    param([string]$Pattern, [string]$Description)
    if ($source -notmatch $Pattern) {
        throw "Installer contract missing: $Description"
    }
}

# Elevation must propagate the elevated process result instead of reading this shell's stale
# LASTEXITCODE value after Start-Process.
Require-Pattern '-PassThru' 'elevated process handle'
Require-Pattern 'exit\s+\$elevated\.ExitCode' 'elevated installer exit-code propagation'

# Required runtime prerequisites must fail before any scheduled task is registered.
Require-Pattern 'Required wintun\.dll is missing after provisioning; refusing to register or start Bondify' 'post-provision Wintun guard'
$wintunGuard = $source.IndexOf('Required wintun.dll is missing after provisioning')
$taskRegistration = $source.IndexOf('Register-ScheduledTask')
if ($wintunGuard -lt 0 -or $taskRegistration -lt 0 -or $wintunGuard -gt $taskRegistration) {
    throw 'Wintun fail-closed guard must execute before scheduled-task registration.'
}

# A connectivity failure must remove privileged autostart and return failure to automation.
Require-Pattern 'if\s*\(-not\s+\$bonded\)' 'explicit unbonded failure branch'
Require-Pattern 'Stop-ScheduledTask' 'failed-session task stop'
Require-Pattern 'Unregister-ScheduledTask' 'failed-session autostart removal'
Require-Pattern 'throw\s+"Bondify did not confirm a bonded session within 50s' 'non-zero install failure on unbonded session'

# Success remains explicit and only occurs after the failure branch.
$failureBranch = $source.IndexOf('if (-not $bonded)')
$successMessage = $source.IndexOf('Bondify installed and bonded in')
$successExit = $source.LastIndexOf('exit 0')
if ($failureBranch -lt 0 -or $successMessage -lt $failureBranch -or $successExit -lt $successMessage) {
    throw 'Installer success path is not ordered after the bonded-session failure gate.'
}

# Guard against accidental introduction of an additional elevation prompt.
$runAsCount = [regex]::Matches($source, '-Verb\s+RunAs').Count
if ($runAsCount -ne 1) {
    throw "Installer must contain exactly one RunAs elevation boundary; found $runAsCount."
}

Write-Host 'Windows installer fail-closed contract: PASS'
