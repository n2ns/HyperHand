# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.1.0] - 2026-10-05

Initial release.

### Added

- Host tray program `hyperhand.exe` serving MCP over Streamable HTTP at `http://127.0.0.1:8770/mcp`; `-port` selects another port. Relaunches itself elevated, runs as a single instance, and logs to `%LOCALAPPDATA%\HyperHand\hyperhand.log`.
- `hyperhand.exe install`: registers a scheduled task that starts the exe from its current location at logon with highest privileges, and starts it.
- Registration of the HyperHand Hyper-V socket service on the host at startup.
- VM control through Hyper-V WMI, without a guest agent: `vm_list`, `vm_start`, `vm_stop`, `vm_screenshot` (PNG at the guest resolution), `vm_click`, `vm_drag`, `vm_scroll`, `vm_key`.
- Checkpoints: `vm_checkpoints`, `vm_checkpoint`, `vm_restore` (exact name; starts the VM after restoring unless `start` is false).
- Optional `vm` argument on every tool; defaults to the only running VM, or the only VM if none is running.
- Guest agent `hyperhand-agent.exe`: tray app in the user's session that serves host requests over a Hyper-V socket; `install` subcommand copies it to `%LOCALAPPDATA%\HyperHand` and starts it at logon through the `HKCU` Run key, without administrator rights.
- `vm_install_agent`: copies the agent into the guest with `Copy-VMFile` (enabling the Guest Service Interface if needed), runs its installer through the keyboard, and waits for it to answer.
- `vm_update_agent`: replaces the running agent with the build next to `hyperhand.exe` and waits for it to restart.
- `vm_exec`: runs a PowerShell or cmd command as the logged-on user with a timeout and returns exit code, stdout and stderr; `admin: true` runs it elevated.
- `vm_push` and `vm_pull`: copy files or directories recursively between host and guest, streamed through `.hhpart` temporary files; `vm_push` skips files whose SHA-256 matches the guest copy unless `force` is true.
- `vm_type`: pastes text through the guest clipboard when the agent is available, so an IME cannot alter it; falls back to keyboard input for ASCII text.
- `vm_clipboard_get`, `vm_clipboard_set`, `vm_focus_window`, `vm_wait` (process exit, process running, file exists), and `vm_screenshot` with `source: agent`.
- `scripts/restart-tray.ps1`: swaps in a rebuilt `build\hyperhand.exe.new` and restarts the tray.
- Release builds through GitHub Actions: pushing a `vX.Y.Z` tag runs the tests, builds `hyperhand.exe` and `hyperhand-agent.exe` for Windows amd64, and publishes `hyperhand-X.Y.Z-windows-amd64.zip` on a GitHub Release with the version's changelog section as notes.
- Version stamped at build time (`-X hyperhand/internal/proto.Version`); reported by the MCP server and the agent ping, `dev` for builds without it.
