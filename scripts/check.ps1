$ErrorActionPreference = "Stop"

. "$PSScriptRoot/native.ps1"

$root = Split-Path -Parent $PSScriptRoot
Set-Location $root

$goCache = Join-Path $root ".gocache"
$goTemp = Join-Path $root ".gotmp"
New-Item -ItemType Directory -Force -Path $goCache, $goTemp | Out-Null
if (-not $env:GOCACHE) {
    $env:GOCACHE = $goCache
}
if (-not $env:GOTMPDIR) {
    $env:GOTMPDIR = $goTemp
}

Invoke-NativeCommand -FilePath "docker" -ArgumentList @("compose", "up", "-d", "--wait", "postgres")
if (-not $env:TEST_DATABASE_URL) {
    $env:TEST_DATABASE_URL = "postgres://agent_chat:agent_chat_password@127.0.0.1:5433/agent_chat?sslmode=disable"
}

$unformatted = @(Invoke-NativeCommand -FilePath "gofmt" -ArgumentList @("-l", "cmd", "internal", "migrations"))
if ($unformatted) {
    throw "Unformatted Go files:`n$($unformatted -join "`n")"
}

Invoke-NativeCommand -FilePath "go" -ArgumentList @("test", "-count=1", "./...")
Invoke-NativeCommand -FilePath "go" -ArgumentList @("vet", "./...")
& "$PSScriptRoot/build.ps1"
Invoke-NativeCommand -FilePath "docker" -ArgumentList @("compose", "config", "--quiet")
