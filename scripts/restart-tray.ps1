# Install a rebuilt host and restart its user tray. The installer requests UAC.
$ErrorActionPreference = 'Stop'
$buildDir = Join-Path (Split-Path $PSScriptRoot -Parent) 'build'
$candidate = Join-Path $buildDir 'hyperhand.exe'
if (-not (Test-Path -LiteralPath $candidate -PathType Leaf)) {
    throw 'Build build\hyperhand.exe before installing.'
}
# The installer owns service replacement and migration; do not kill processes by name.
Start-Process -FilePath $candidate -ArgumentList 'install' -WindowStyle Hidden
