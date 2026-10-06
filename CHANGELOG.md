# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Dedicated `HyperHandService` Windows service under `NT SERVICE\HyperHandService`, with Hyper-V rights assigned to the service account and an access-controlled local named pipe for specific VM operations and the guest socket tunnel.
- Host tray restart action: restarts the ordinary tray/MCP process without restarting the service or any VM; in-progress requests are interrupted.
- `vm_start` waits until the desktop is usable: the guest agent answers (up to 90 seconds) and the session is unlocked. A session Windows locked after signing in is unlocked with the password stored for the VM; otherwise the error says why the desktop is not usable.
- `vm_status` reports a VM's power state, the agent, the session lock state, a non-console session, an open UAC prompt and whether an unlock password is stored.
- `vm_unlock` unlocks a locked session with the stored password. The password is typed once, only after the agent confirms the session is locked, is the console session, shows the sign-in screen with keyboard input on its secure desktop and has no UAC prompt open.
- Host tray **Virtual machines** submenu listing the Hyper-V VMs and their state, with **Set unlock password...** and **Clear unlock password** per VM (stored in Windows Credential Manager for the current user).
- Tray **Open console** per VM opens Virtual Machine Connection without a UAC prompt, or brings an already open one to the front; **Open console when started** opens it whenever `vm_start` starts that VM. `install` registers the on-demand `HyperHand Console` task for this, which runs `vmconnect.exe` with the installing user's highest privileges; `uninstall` removes it.
- Agent op `session_state`.
- `vm_focus_window` and `vm_click` with a window name the locked session as the reason when they fail on a locked guest.
- `vm_windows` lists the guest's visible top-level windows with handle, title, class, process, position, enabled, foreground, owner and modal state.
- `vm_focus_window` accepts a window `handle` and returns the focused window's handle.
- `vm_click` accepts `window` or `handle`: coordinates are then relative to that window, and the click is refused unless it is the enabled foreground window and the point is inside it, on screen and not covered by another window.

### Changed

- **Breaking:** `vm_stop` is replaced by `vm_shutdown`, which shuts the guest down normally through the Hyper-V shutdown integration service and waits up to 3 minutes until the VM is off (failing, without turning it off, when a program blocks shutdown), and `vm_turn_off`, which turns the VM off immediately as `vm_stop` did.
- Host tray menu text is in English.
- Normal host tray startup no longer requests elevation. Installation, updates and uninstallation still require administrator approval.
- `hyperhand.exe install` installs both executables under `%ProgramFiles%\HyperHand`, records the owner under `%ProgramData%\HyperHand`, and migrates the `HyperHand` logon task from highest privileges to least privilege. Repeating `install` updates the installation.
- Host file transfers use the ordinary tray user's permissions. The local MCP endpoint remains unauthenticated; the broker pipe ACL is not MCP authentication.
- `scripts/restart-tray.ps1` now invokes installation/update of the built executables instead of terminating every process named `hyperhand.exe`.
- `install` and `uninstall` no longer run an embedded PowerShell script: the elevated `hyperhand.exe` does the work itself through the service control manager, Task Scheduler, local group and security APIs. Uninstalling from the installed `hyperhand.exe` leaves `%ProgramData%\HyperHand\uninstall-cleanup.exe` until the next restart.

### Fixed

- Reinstalling could stop with "Access is denied" after stopping the service, leaving the service stopped and no tray: setup tried to end the service's process, which runs as the service account. It now waits for that process to exit.
- The host tray icon no longer stays an empty placeholder without menu when the tray starts at logon before the taskbar is ready. The tray now uses its own notification icon code instead of `fyne.io/systray`: a failed add is retried every 5 seconds and whenever the taskbar is created, and every add carries the icon, tooltip and callback message and selects version 4 behaviour.
- VM start and stop wait up to 45 seconds for asynchronous Hyper-V jobs and report failures or timeouts without automatically resending the operation.
- `vm_focus_window` no longer puts ribbon programs such as AutoCAD into key-tip mode. When the window did not come to the front at once, the agent simulated an Alt press, which arrived after the switch in the focused window; it now injects a zero-distance mouse move instead.

## [0.1.2] - 2026-10-05

### Fixed

- Command cancellation and timeouts terminate descendants even after the original shell has exited, while successful commands can still launch background applications.
- Elevated commands include UAC consent in their timeout, cancel without a second elevation prompt, and reject workers that arrive after the request expires.
- Elevated CMD commands preserve UTF-8 text, Unicode filenames, percent expansion and exit codes.
- Requests cancelled while waiting for another agent call return promptly without disturbing the active connection.
- Saved and paused Hyper-V machines display the correct state.

## [0.1.1] - 2026-10-05

### Added

- `hyperhand.exe uninstall`: relaunches itself elevated if needed, stops the tray, deletes the `HyperHand` scheduled task, the Hyper-V socket service registration and `%LOCALAPPDATA%\HyperHand`, and shows what was removed. The exe and its folder are kept.
- `hyperhand-agent.exe uninstall`: without administrator rights, stops the running agent and deletes the `HyperHandAgent` Run value, `%LOCALAPPDATA%\HyperHand` and `C:\Users\Public\HyperHand`, and shows what was removed.

### Fixed

- File transfers use unique temporary files so existing files named `<destination>.hhpart` are preserved, including when a transfer fails.
- Uploads return an error promptly when the guest disk is full, without blocking subsequent agent requests.
- Commands no longer block the agent when completion and cancellation occur together.
- Guest uninstallation waits for the uninstall process to exit before deleting its executable, including when the result dialog stays open.

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
