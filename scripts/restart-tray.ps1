# Replaces build\hyperhand.exe with build\hyperhand.exe.new (if present) and restarts the tray. Run elevated.
$b = Join-Path (Split-Path $PSScriptRoot -Parent) 'build'
Get-Process hyperhand -ErrorAction SilentlyContinue | Stop-Process -Force
while (Get-Process hyperhand -ErrorAction SilentlyContinue) { Start-Sleep -Milliseconds 200 }
if (Test-Path "$b\hyperhand.exe.new") {
    for ($i = 0; $i -lt 25; $i++) {
        try { Move-Item "$b\hyperhand.exe.new" "$b\hyperhand.exe" -Force -ErrorAction Stop; break } catch { Start-Sleep -Milliseconds 200 }
    }
}
schtasks /Run /TN HyperHand | Out-Null
