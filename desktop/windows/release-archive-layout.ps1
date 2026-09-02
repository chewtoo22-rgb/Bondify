Set-StrictMode -Version Latest

function Assert-BondifyReleaseArchiveEntries {
    param(
        [Parameter(Mandatory = $true)]
        [string[]]$Entries,

        [Parameter(Mandatory = $true)]
        [string]$ExpectedRootName
    )

    if ($Entries.Count -eq 0) {
        throw 'Release archive is empty.'
    }
    if ([string]::IsNullOrWhiteSpace($ExpectedRootName) -or $ExpectedRootName -match '[\\/:]' -or $ExpectedRootName -in @('.', '..')) {
        throw "Invalid expected release root name: $ExpectedRootName"
    }

    $allowedFiles = @(
        "$ExpectedRootName/bondify.exe",
        "$ExpectedRootName/install.ps1"
    )
    $seen = @{}

    foreach ($rawEntry in $Entries) {
        $entry = ([string]$rawEntry).Trim()
        if ([string]::IsNullOrWhiteSpace($entry)) {
            throw 'Release archive contains a blank entry name.'
        }

        $normalized = $entry.Replace('\\', '/')
        if ($normalized.StartsWith('/') -or $normalized -match '^[A-Za-z]:' -or $normalized -match '(^|/)\.\.(/|$)') {
            throw "Release archive contains unsafe path entry: $entry"
        }

        while ($normalized.EndsWith('/')) {
            $normalized = $normalized.Substring(0, $normalized.Length - 1)
        }
        if ([string]::IsNullOrWhiteSpace($normalized)) {
            throw "Release archive contains an invalid path entry: $entry"
        }

        if ($seen.ContainsKey($normalized)) {
            throw "Release archive contains duplicate entry: $normalized"
        }
        $seen[$normalized] = $true

        if ($normalized -eq $ExpectedRootName) {
            continue
        }
        if ($normalized -notin $allowedFiles) {
            throw "Release archive contains unexpected entry: $normalized"
        }
    }

    foreach ($required in $allowedFiles) {
        if (-not $seen.ContainsKey($required)) {
            throw "Release archive is missing required entry: $required"
        }
    }
}
