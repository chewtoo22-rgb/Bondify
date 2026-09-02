$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

. (Join-Path $PSScriptRoot 'release-archive-layout.ps1')

function Assert-Accepts {
    param([string[]]$Entries)
    Assert-BondifyReleaseArchiveEntries -Entries $Entries -ExpectedRootName 'bondify-windows-amd64'
}

function Assert-Rejects {
    param([string[]]$Entries, [string]$Case)
    try {
        Assert-BondifyReleaseArchiveEntries -Entries $Entries -ExpectedRootName 'bondify-windows-amd64'
    }
    catch {
        return
    }
    throw "Expected archive layout rejection: $Case"
}

$valid = @(
    'bondify-windows-amd64/',
    'bondify-windows-amd64/bondify.exe',
    'bondify-windows-amd64/install.ps1'
)
Assert-Accepts $valid

Assert-Rejects @(
    $valid + 'bondify-windows-amd64/debug.log'
) 'unexpected top-level file'

Assert-Rejects @(
    $valid + 'bondify-windows-amd64/nested/notes.txt'
) 'unexpected nested payload'

Assert-Rejects @(
    $valid + 'second-root/extra.txt'
) 'second top-level root'

Assert-Rejects @(
    'bondify-windows-amd64/',
    'bondify-windows-amd64/bondify.exe',
    'bondify-windows-amd64/bondify.exe',
    'bondify-windows-amd64/install.ps1'
) 'duplicate member'

Assert-Rejects @(
    'bondify-windows-amd64/',
    'bondify-windows-amd64/bondify.exe'
) 'missing installer'

Assert-Rejects @(
    'bondify-windows-amd64/',
    'bondify-windows-amd64/bondify.exe',
    '../install.ps1'
) 'path traversal'

Write-Host 'Windows release archive layout contract: PASS'
