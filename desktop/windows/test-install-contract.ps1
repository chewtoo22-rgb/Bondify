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

# Relay configuration is embedded in both the elevated process argument list and the
# privileged scheduled-task command line. It must be validated before either boundary.
Require-Pattern 'function\s+Assert-RelayAddress' 'relay address admission function'
Require-Pattern 'function\s+Assert-RelayPublicKey' 'relay public-key admission function'
Require-Pattern 'FromBase64String' 'relay public-key base64 decoding'
Require-Pattern 'decoded\.Length\s+-ne\s+32' '32-byte relay public-key requirement'
Require-Pattern 'ToBase64String\(\$decoded\)\s+-cne\s+\$Value' 'canonical relay public-key requirement'

$addressAdmission = $source.IndexOf('Assert-RelayAddress -Value $RelayAddr')
$keyAdmission = $source.IndexOf('Assert-RelayPublicKey -Value $RelayPubKey')
$elevation = $source.IndexOf('Start-Process -FilePath "powershell.exe"')
$taskAction = $source.IndexOf('New-ScheduledTaskAction')
if ($addressAdmission -lt 0 -or $keyAdmission -lt 0 -or $elevation -lt 0 -or $taskAction -lt 0) {
    throw 'Unable to locate installer input-admission and privileged command boundaries.'
}
if ($addressAdmission -gt $elevation -or $keyAdmission -gt $elevation -or $addressAdmission -gt $taskAction -or $keyAdmission -gt $taskAction) {
    throw 'Relay input admission must run before elevation and scheduled-task command construction.'
}

# Execute only the pure admission functions from the parsed installer. This provides
# behavioral regression coverage without triggering UAC, file copies, downloads, or tasks.
$functionAsts = $ast.FindAll({
    param($node)
    $node -is [System.Management.Automation.Language.FunctionDefinitionAst] -and
        $node.Name -in @('Assert-RelayAddress', 'Assert-RelayPublicKey')
}, $true)
if ($functionAsts.Count -ne 2) {
    throw "Expected exactly two relay admission functions; found $($functionAsts.Count)."
}
foreach ($functionAst in $functionAsts) {
    Invoke-Expression $functionAst.Extent.Text
}

function Assert-Passes {
    param([scriptblock]$Action, [string]$Description)
    try {
        & $Action
    } catch {
        throw "Expected admission success for ${Description}: $($_.Exception.Message)"
    }
}

function Assert-Fails {
    param([scriptblock]$Action, [string]$Description)
    $failed = $false
    try {
        & $Action
    } catch {
        $failed = $true
    }
    if (-not $failed) {
        throw "Expected admission failure for ${Description}."
    }
}

$validKey = [Convert]::ToBase64String([byte[]]::new(32))
Assert-Passes { Assert-RelayAddress -Value 'relay.example.com:443' } 'DNS relay'
Assert-Passes { Assert-RelayAddress -Value '192.0.2.10:51820' } 'IPv4 relay'
Assert-Passes { Assert-RelayAddress -Value '[2001:db8::1]:443' } 'IPv6 relay'
Assert-Passes { Assert-RelayPublicKey -Value $validKey } 'canonical 32-byte key'

Assert-Fails { Assert-RelayAddress -Value 'relay.example.com:443" -default-route "false' } 'quoted argument injection'
Assert-Fails { Assert-RelayAddress -Value 'relay.example.com:443;whoami' } 'command separator injection'
Assert-Fails { Assert-RelayAddress -Value 'relay.example.com:0' } 'zero port'
Assert-Fails { Assert-RelayAddress -Value 'relay.example.com:65536' } 'overflow port'
Assert-Fails { Assert-RelayAddress -Value '999.1.1.1:443' } 'invalid IPv4 literal'
Assert-Fails { Assert-RelayAddress -Value 'bad..host:443' } 'empty DNS label'
Assert-Fails { Assert-RelayPublicKey -Value 'not-base64' } 'malformed base64 key'
Assert-Fails { Assert-RelayPublicKey -Value ([Convert]::ToBase64String([byte[]]::new(31))) } '31-byte key'
Assert-Fails { Assert-RelayPublicKey -Value ($validKey + "`n") } 'whitespace-bearing key'

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
