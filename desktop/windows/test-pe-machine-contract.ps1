$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

. (Join-Path $PSScriptRoot 'pe-machine.ps1')

$tempRoot = Join-Path ([System.IO.Path]::GetTempPath()) ("bondify-pe-contract-" + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Force -Path $tempRoot | Out-Null

function New-SyntheticPE {
    param(
        [Parameter(Mandatory = $true)]
        [string]$Path,
        [Parameter(Mandatory = $true)]
        [uint16]$Machine,
        [int]$PeOffset = 128,
        [switch]$BadSignature,
        [switch]$BadOffset
    )

    $bytes = New-Object byte[] 256
    $bytes[0] = 0x4d
    $bytes[1] = 0x5a
    $offsetToWrite = if ($BadOffset) { 4096 } else { $PeOffset }
    [System.BitConverter]::GetBytes([int]$offsetToWrite).CopyTo($bytes, 0x3c)

    if (-not $BadOffset) {
        if ($BadSignature) {
            $bytes[$PeOffset] = 0x42
            $bytes[$PeOffset + 1] = 0x41
            $bytes[$PeOffset + 2] = 0x44
            $bytes[$PeOffset + 3] = 0x21
        }
        else {
            $bytes[$PeOffset] = 0x50
            $bytes[$PeOffset + 1] = 0x45
            $bytes[$PeOffset + 2] = 0x00
            $bytes[$PeOffset + 3] = 0x00
        }
        [System.BitConverter]::GetBytes([uint16]$Machine).CopyTo($bytes, $PeOffset + 4)
    }

    [System.IO.File]::WriteAllBytes($Path, $bytes)
}

function Expect-Pass {
    param([scriptblock]$Action, [string]$Name)
    & $Action
    Write-Host "PASS: $Name"
}

function Expect-Fail {
    param([scriptblock]$Action, [string]$Name)
    $failed = $false
    try {
        & $Action
    }
    catch {
        $failed = $true
    }
    if (-not $failed) {
        throw "Expected failure: $Name"
    }
    Write-Host "PASS: $Name rejected"
}

try {
    $amd64 = Join-Path $tempRoot 'amd64.exe'
    $arm64 = Join-Path $tempRoot 'arm64.exe'
    $wrong = Join-Path $tempRoot 'wrong.exe'
    $badSignature = Join-Path $tempRoot 'bad-signature.exe'
    $badOffset = Join-Path $tempRoot 'bad-offset.exe'
    $tiny = Join-Path $tempRoot 'tiny.exe'

    New-SyntheticPE -Path $amd64 -Machine 0x8664
    New-SyntheticPE -Path $arm64 -Machine 0xaa64
    New-SyntheticPE -Path $wrong -Machine 0x014c
    New-SyntheticPE -Path $badSignature -Machine 0x8664 -BadSignature
    New-SyntheticPE -Path $badOffset -Machine 0x8664 -BadOffset
    [System.IO.File]::WriteAllBytes($tiny, [byte[]](0x4d, 0x5a))

    Expect-Pass { [void](Assert-BondifyPEMachine -Path $amd64 -Architecture amd64) } 'amd64 machine accepted'
    Expect-Pass { [void](Assert-BondifyPEMachine -Path $arm64 -Architecture arm64) } 'arm64 machine accepted'
    Expect-Fail { [void](Assert-BondifyPEMachine -Path $arm64 -Architecture amd64) } 'archive/PE architecture mismatch'
    Expect-Fail { [void](Assert-BondifyPEMachine -Path $wrong -Architecture amd64) } 'unsupported x86 machine under amd64 label'
    Expect-Fail { [void](Get-BondifyPEMachine -Path $badSignature) } 'invalid PE signature'
    Expect-Fail { [void](Get-BondifyPEMachine -Path $badOffset) } 'out-of-range e_lfanew'
    Expect-Fail { [void](Get-BondifyPEMachine -Path $tiny) } 'truncated DOS header'

    Write-Host 'Windows PE machine contract: PASS'
}
finally {
    Remove-Item -LiteralPath $tempRoot -Recurse -Force -ErrorAction SilentlyContinue
}
