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

New-Item -ItemType Directory -Force -Path "bin" | Out-Null
Invoke-NativeCommand -FilePath "go" -ArgumentList @("build", "-o", "bin/api.exe", "./cmd/api")
Invoke-NativeCommand -FilePath "go" -ArgumentList @("build", "-o", "bin/worker.exe", "./cmd/worker")
