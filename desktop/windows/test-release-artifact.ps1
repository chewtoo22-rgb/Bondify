param(
    [Parameter(Mandatory = $true)]
    [string]$ArchivePath,

    [Parameter(Mandatory = $true)]
    [string]$ChecksumPath
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

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

$actualHash = (Get-FileHash -LiteralPath $archive -Algorithm SHA256).Hash.ToLowerInvariant()
if ($actualHash -ne $expectedHash) {
    throw "Release archive SHA-256 mismatch: expected $expectedHash, got $actualHash."
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

    $header = [System.IO.File]::ReadAllBytes($exe)
    if ($header.Length -lt 2 -or $header[0] -ne 0x4d -or $header[1] -ne 0x5a) {
        throw 'bondify.exe does not have a valid Windows PE MZ header.'
    }

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

    Write-Host "Windows release artifact acceptance: PASS ($archiveName, sha256=$actualHash)"
}
finally {
    Remove-Item -LiteralPath $tempRoot -Recurse -Force -ErrorAction SilentlyContinue
}
