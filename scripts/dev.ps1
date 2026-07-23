$ErrorActionPreference = "Stop"

. "$PSScriptRoot/native.ps1"

$root = Split-Path -Parent $PSScriptRoot
Set-Location $root

Invoke-NativeCommand -FilePath "docker" -ArgumentList @("compose", "up", "-d", "--wait", "postgres")
& "$PSScriptRoot/build.ps1"

$worker = Start-Process `
    -FilePath "$root/bin/worker.exe" `
    -WorkingDirectory $root `
    -NoNewWindow `
    -PassThru

try {
    Invoke-NativeCommand -FilePath "$root/bin/api.exe"
}
finally {
    if (-not $worker.HasExited) {
        Stop-Process -Id $worker.Id
    }
}
