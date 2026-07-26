function Import-DotEnv {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory = $true)]
        [string]$Path
    )

    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) {
        return
    }

    $loadedCount = 0
    foreach ($line in Get-Content -LiteralPath $Path -Encoding UTF8) {
        if ($line -notmatch '^\s*([A-Za-z_][A-Za-z0-9_]*)\s*=(.*)$') {
            continue
        }

        $name = $Matches[1]
        if ($null -ne [Environment]::GetEnvironmentVariable($name, "Process")) {
            continue
        }

        $value = $Matches[2].Trim()
        if (
            $value.Length -ge 2 -and
            (
                ($value.StartsWith('"') -and $value.EndsWith('"')) -or
                ($value.StartsWith("'") -and $value.EndsWith("'"))
            )
        ) {
            $value = $value.Substring(1, $value.Length - 2)
        }

        [Environment]::SetEnvironmentVariable($name, $value, "Process")
        $loadedCount++
    }

    Write-Host "Loaded $loadedCount local environment variables from $Path"
}
