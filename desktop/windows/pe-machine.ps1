Set-StrictMode -Version Latest

function Get-BondifyPEMachine {
    param(
        [Parameter(Mandatory = $true)]
        [string]$Path
    )

    $resolved = (Resolve-Path -LiteralPath $Path).Path
    if (-not (Test-Path -LiteralPath $resolved -PathType Leaf)) {
        throw "PE file not found: $Path"
    }

    $bytes = [System.IO.File]::ReadAllBytes($resolved)
    if ($bytes.Length -lt 64) {
        throw 'PE file is too small to contain a valid DOS header.'
    }
    if ($bytes[0] -ne 0x4d -or $bytes[1] -ne 0x5a) {
        throw 'PE file does not have the DOS MZ signature.'
    }

    $peOffset = [System.BitConverter]::ToInt32($bytes, 0x3c)
    if ($peOffset -lt 64 -or $peOffset -gt ($bytes.Length - 6)) {
        throw "PE header offset is outside the file: $peOffset"
    }

    if ($bytes[$peOffset] -ne 0x50 -or
        $bytes[$peOffset + 1] -ne 0x45 -or
        $bytes[$peOffset + 2] -ne 0x00 -or
        $bytes[$peOffset + 3] -ne 0x00) {
        throw 'PE file does not have the PE\0\0 signature at e_lfanew.'
    }

    return [System.BitConverter]::ToUInt16($bytes, $peOffset + 4)
}

function Assert-BondifyPEMachine {
    param(
        [Parameter(Mandatory = $true)]
        [string]$Path,

        [Parameter(Mandatory = $true)]
        [ValidateSet('amd64', 'arm64')]
        [string]$Architecture
    )

    $expected = switch ($Architecture) {
        'amd64' { [uint16]0x8664 }
        'arm64' { [uint16]0xaa64 }
        default { throw "Unsupported Windows architecture: $Architecture" }
    }

    $actual = Get-BondifyPEMachine -Path $Path
    if ($actual -ne $expected) {
        throw ('Windows PE machine mismatch: archive declares {0} (0x{1:x4}), executable is 0x{2:x4}.' -f $Architecture, $expected, $actual)
    }

    return $actual
}
