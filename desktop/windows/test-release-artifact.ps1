param(
    [Parameter(Mandatory = $true)]
    [string]$ArchivePath,

    [Parameter(Mandatory = $true)]
    [string]$ChecksumPath
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

. (Join-Path $PSScriptRoot 'pe-machine.ps1')

$archive = (Resolve-Path -LiteralPath $ArchivePath).Path
$checksum = (Resolve-Path -LiteralPath $ChecksumPath).Path

if (-not (Test-Path -LiteralPath $archive -PathType Leaf)) {
    throw "Release archive not found: $ArchivePath"
}
if (-not (Test-Path -LiteralPath $checksum -PathType Leaf)) {
    throw "Release checksum not found: $ChecksumPath"
}

$expectedLine = (Get-Content -LiteralPath $checksum | Select-Object -First 1).Trim()
if ($expectedLine -notmatch '^([0-9a-fA-F]{64})\s+\*?(.+)$') {
    throw 'Release checksum file is not a valid SHA-256 manifest entry.'
}
$expectedHash = $Matches[1].ToLowerInvariant()
$manifestName = [System.IO.Path]::GetFileName($Matches[2].Trim())
$archiveName = [System.IO.Path]::GetFileName($archive)
if ($manifestName -ne $archiveName) {
    throw "Checksum manifest targets '$manifestName', expected '$archiveName'."
}
if ($archiveName -notmatch '^bondify-windows-(amd64|arm64)\.tar\.gz$') {
    throw "Windows release archive name does not declare a supported architecture: $archiveName"
}
$declaredArchitecture = $Matches[1].ToLowerInvariant()

$actualHash = (Get-FileHash -LiteralPath $archive -Algorithm SHA256).Hash.ToLowerInvariant()
if ($actualHash -ne $expectedHash) {
    throw "Release archive SHA-256 mismatch: expected $expectedHash, got $actualHash."
}

$entries = @(& tar -tzf $archive)
if ($LASTEXITCODE -ne 0) {
    throw "Failed to list release archive; tar exited $LASTEXITCODE."
}
if ($entries.Count -eq 0) {
    throw 'Release archive is empty.'
}

foreach ($rawEntry in $entries) {
    $entry = ([string]$rawEntry).Trim()
    if ([string]::IsNullOrWhiteSpace($entry)) {
        throw 'Release archive contains a blank entry name.'
    }

    $normalized = $entry.Replace('\', '/')
    if ($normalized.StartsWith('/') -or $normalized -match '^[A-Za-z]:' -or $normalized -match '(^|/)\.\.(/|$)') {
        throw "Release archive contains unsafe path entry: $entry"
    }

    $segments = @($normalized.Split('/') | Where-Object { $_ -ne '' -and $_ -ne '.' })
    if ($segments.Count -eq 0) {
        throw "Release archive contains an invalid path entry: $entry"
    }
}

$tempRoot = Join-Path ([System.IO.Path]::GetTempPath()) ("bondify-release-" + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Force -Path $tempRoot | Out-Null
try {
    & tar -xzf $archive -C $tempRoot
    if ($LASTEXITCODE -ne 0) {
        throw "Failed to extract release archive; tar exited $LASTEXITCODE."
    }

    $roots = @(Get-ChildItem -LiteralPath $tempRoot -Directory)
    if ($roots.Count -ne 1) {
        throw "Release archive must contain exactly one top-level directory; found $($roots.Count)."
    }

    $expectedRootName = $archiveName.Substring(0, $archiveName.Length - '.tar.gz'.Length)
    if ($roots[0].Name -ne $expectedRootName) {
        throw "Release archive root '$($roots[0].Name)' does not match archive identity '$expectedRootName'."
    }

    $root = $roots[0].FullName
    $exe = Join-Path $root 'bondify.exe'
    $installer = Join-Path $root 'install.ps1'
    foreach ($required in @($exe, $installer)) {
        if (-not (Test-Path -LiteralPath $required -PathType Leaf)) {
            throw "Release archive is missing required file: $required"
        }
    }

    $unexpected = @(Get-ChildItem -LiteralPath $root -File | Where-Object { $_.Name -notin @('bondify.exe', 'install.ps1') })
    if ($unexpected.Count -gt 0) {
        throw "Release archive contains unexpected top-level files: $($unexpected.Name -join ', ')"
    }

    $machine = Assert-BondifyPEMachine -Path $exe -Architecture $declaredArchitecture

    $help = & $exe -h 2>&1 | Out-String
    if ($LASTEXITCODE -ne 0) {
        throw "Packaged bondify.exe -h exited $LASTEXITCODE."
    }
    foreach ($requiredOption in @('-relay', '-local-addrs')) {
        if ($help -notmatch [regex]::Escape($requiredOption)) {
            throw "Packaged CLI help is missing required option $requiredOption."
        }
    }

    & (Join-Path $PSScriptRoot 'test-install-contract.ps1') -InstallerPath $installer
    if ($LASTEXITCODE -ne 0) {
        throw "Packaged installer contract failed with exit code $LASTEXITCODE."
    }

    Write-Host ('Windows release artifact acceptance: PASS ({0}, arch={1}, machine=0x{2:x4}, sha256={3})' -f $archiveName, $declaredArchitecture, $machine, $actualHash)
}
finally {
    Remove-Item -LiteralPath $tempRoot -Recurse -Force -ErrorAction SilentlyContinue
}
