# HyperHand: Hyper-V VM Control for AI Agents

Let an AI agent such as Claude Code or Codex see and operate a Windows virtual machine on Hyper-V: take screenshots, click, type, run commands, move files and roll back to checkpoints, without a network connection to the guest and without a guest password. Built for Windows 10/11 hosts running Hyper-V with Windows guests; works with any MCP client that supports Streamable HTTP.

HyperHand has two executables and three roles:

- **`hyperhand.exe`**, an ordinary user tray program on the host. It serves MCP at `http://127.0.0.1:8770/mcp` and handles host file access.
- **`HyperHandService`**, the same host executable running as a Windows service under its dedicated account, `NT SERVICE\HyperHandService`. The tray delegates Hyper-V operations through an access-controlled local named pipe; the service connects to the guest over a Hyper-V socket.
- **`hyperhand-agent.exe`**, a tray program inside the guest. It runs commands, transfers files and handles the clipboard and windows in the logged-on user's session, talking to the host over a Hyper-V socket.

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/images/architecture-dark.svg">
  <img alt="HyperHand architecture: an MCP client talks to the ordinary host tray, which delegates Hyper-V control and the guest socket connection to HyperHandService" src="docs/images/architecture.svg">
</picture>

## Features

- **Screen, mouse and keyboard from the host**: screenshots at the guest's resolution, clicks, drag, wheel, typing and key combinations. Works on the login screen and UAC prompts too, since it does not depend on anything running in the guest.
- **Commands in the guest**: run PowerShell or cmd in the user's desktop session and get the exit code, stdout and stderr back; optionally elevated.
- **File transfer**: copy files or whole directories in either direction, streamed at hundreds of MB/s. Uploads skip files whose SHA-256 already matches the guest copy.
- **Checkpoints**: list, create and restore; a restored VM is started again automatically.
- **Clipboard, windows and waiting**: read and write the guest clipboard, bring a window to the front, wait until a process exits or a file appears.
- **Start to a usable desktop**: `vm_start` waits until the agent answers and the session is unlocked. If Windows locked the session after signing in, it types the unlock password you stored in the tray; `vm_status` reports power, agent and lock state.
- **No guest network; guest password optional**: host and agent talk over a Hyper-V socket; the agent is copied in with Hyper-V's guest file copy and installed from the keyboard. A guest password is needed only if you want HyperHand to unlock a locked session; it stays in Windows Credential Manager on the host.
- **Cancellation**: when the MCP client cancels a long command, the agent kills it and is ready for the next request immediately.
- **No UAC during normal host use**: install or update the service once with administrator approval, then start or restart the tray as an ordinary user. Restarting the tray reconnects MCP without restarting the service or any VM; in-progress requests are interrupted.

## Requirements

- Windows 10/11 Pro or Enterprise host with Hyper-V, and administrator approval for installation, updates and uninstallation. Normal tray use does not require elevation.
- A Windows guest with a logged-on user. Sign the user in automatically, or store an unlock password in the tray so that `vm_start` can unlock a session Windows locked after signing in.
- VMConnect in basic session mode. Enhanced session moves the user's session to remote desktop, so host-side screenshots and input would reach the console lock screen instead.
- Go 1.27 or later, only to build from source.
- An MCP client that supports Streamable HTTP: Claude Code, Codex, Cursor and others.

## Install

Download `hyperhand-X.Y.Z-windows-amd64.zip` from [Releases](https://github.com/n2ns/hyper-hand/releases) and extract both executables to the same folder. Then run:

```powershell
hyperhand.exe install
```

`install` asks for UAC once, copies both executables to `%ProgramFiles%\HyperHand`, installs the automatic `HyperHandService` service and starts the ordinary tray. Only the dedicated service account is added to Hyper-V Administrators; your user account is not. The `HyperHand` logon task runs the installed tray with least privilege, replacing an older highest-privilege task. Run `install` again from a new release to update the installation.

To build from source instead (the version then reads `dev`):

```powershell
go build -ldflags "-H windowsgui" -o build\hyperhand.exe .\cmd\hyperhand
go build -ldflags "-H windowsgui" -o build\hyperhand-agent.exe .\cmd\hyperhand-agent
build\hyperhand.exe install
```

## Quick start

1. Add HyperHand to your MCP client and start a new session:

   Claude Code:

   ```powershell
   claude mcp add --transport http hyperhand http://127.0.0.1:8770/mcp
   ```

   Codex:

   ```powershell
   codex mcp add hyperhand --url http://127.0.0.1:8770/mcp
   ```

   Other clients: add a Streamable HTTP server with the URL `http://127.0.0.1:8770/mcp`.

2. Start the VM and log on in the guest. Switch the guest keyboard to English: the agent's install command is typed on the keyboard, and an input method in Chinese mode would garble it.
3. Ask the AI to install the agent (`vm_install_agent`). It copies the agent into the guest, runs its installer and waits until it answers. The agent then starts at every logon. Alternatively, copy `hyperhand-agent.exe` into the guest yourself and run `hyperhand-agent.exe install` there; see [Install the guest agent manually](docs/user-guide.md#install-the-guest-agent-manually).
4. Ask the AI to work in the VM: take a screenshot, open an application, run a command, copy a build in, restore a checkpoint.

## Tools

Every tool takes an optional `vm` (VM name). Without it, the only running VM is used; if none is running and exactly one exists, that one.

| Tool | What it does |
|---|---|
| `vm_list`, `vm_start`, `vm_stop` | List VMs with their state; start and wait until the desktop is usable (unlocking it with the stored password); turn off |
| `vm_status`, `vm_unlock` | Report power state, agent, session lock state and whether an unlock password is stored; unlock a locked session with the stored password |
| `vm_checkpoints`, `vm_checkpoint`, `vm_restore` | List, create and restore checkpoints (exact names); restore starts the VM unless `start` is false |
| `vm_screenshot` | PNG of the VM screen; `source`: `host` (default) or `agent` |
| `vm_click`, `vm_drag`, `vm_scroll` | Mouse at screenshot pixel coordinates; `vm_click` with `window` or `handle` clicks inside that window only if it is the enabled foreground window |
| `vm_type`, `vm_key` | Type text (pasted through the guest clipboard); press keys such as `enter`, `ctrl+v`, `win+r` |
| `vm_exec` | Run a command in the guest; `shell`, `cwd`, `timeout_ms`, `admin` |
| `vm_push`, `vm_pull` | Copy files or directories host to guest and back; `vm_push` skips unchanged files unless `force` is true |
| `vm_clipboard_get`, `vm_clipboard_set` | Read or write the guest clipboard |
| `vm_windows` | List visible windows: handle, title, class, process, position, enabled, foreground, owner, modal |
| `vm_focus_window` | Bring a window to the front by title or handle |
| `vm_wait` | Wait until a process exits or runs, or a file exists |
| `vm_install_agent`, `vm_update_agent` | Install or replace the guest agent |

Screen, mouse, keyboard, VM and checkpoint tools work without the agent; the others need it. Without the agent, `vm_start` still starts the VM but reports that the desktop is not usable.

## Known limitations

- One AI client per VM at a time: several clients can connect, but requests to a VM's agent are handled one after another and their mouse and keyboard actions would interleave.
- `vm_install_agent` types its command on the keyboard; the guest input method must be in English mode. Installing the agent manually avoids this.
- `admin` commands elevate without a prompt only if the guest's UAC is set to elevate administrators without prompting; otherwise the UAC wait counts toward the command timeout. A late approval cannot execute a cancelled or expired request.
- Host-side screenshots and input act on the VM console; they do not reach a remote desktop or enhanced session.
- Checkpoints cannot be deleted from HyperHand.
- HyperHand unlocks only a session that is signed in and locked, with its agent running: it cannot sign a user in at the sign-in screen after a cold boot. Use automatic sign-in for that. The unlock password must be ASCII (the Hyper-V keyboard types ASCII only); store the PIN instead if the lock screen asks for one.

## Privacy

HyperHand sends no telemetry and makes no network connections beyond the local MCP endpoint on `127.0.0.1`. Host and guest talk over Hyper-V sockets. The MCP endpoint has no authentication: any local process that can reach `127.0.0.1:8770` can control the VMs, including unlocking a VM with a stored unlock password (the password itself is never returned). Unlock passwords are stored in Windows Credential Manager for your user. See [docs/privacy.md](docs/privacy.md).

## Uninstall

Guest first, then host:

1. Guest: run `%LOCALAPPDATA%\HyperHand\hyperhand-agent.exe uninstall` in the guest itself, by hand or from the host with `vm_key` `win+r` and `vm_type`. Not through `vm_exec`: the uninstall stops the agent that would be running it. It deletes the `HyperHandAgent` Run value and the folders `%LOCALAPPDATA%\HyperHand` and `C:\Users\Public\HyperHand`.
2. Host: run `hyperhand.exe uninstall` (one UAC prompt). It removes the installed service, its Hyper-V group membership, the `HyperHand` logon task and the Hyper-V socket registration, then removes the installed executables if their hashes still match. User logs, guest files and nonempty service working data are preserved.
3. Remove the server from your MCP client. See the user guide for installed files and logs.

Details in the [user guide](docs/user-guide.md#uninstalling).

## Documentation

- [User guide](docs/user-guide.md): setup step by step, updating, releasing, troubleshooting.
- [Features](docs/features.md): detailed behaviour of every tool.
- [Privacy](docs/privacy.md): what is stored and what goes over the wire.
- [Changelog](CHANGELOG.md)

## Disclaimer

HyperHand is an independent project, not affiliated with or endorsed by Microsoft or Anthropic. Hyper-V is a trademark of Microsoft. HyperHand gives an AI full control of your virtual machines; use it with VMs you can afford to lose and keep checkpoints.
