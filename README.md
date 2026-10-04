# HyperHand: Hyper-V VM Control for Claude Code

Let Claude Code see and operate a Windows virtual machine on Hyper-V: take screenshots, click, type, run commands, move files and roll back to checkpoints, without a network connection to the guest and without a guest password. Built for Windows 10/11 hosts running Hyper-V with Windows guests.

HyperHand has two parts:

- **`hyperhand.exe`**, a tray program on the host. It serves MCP at `http://127.0.0.1:8770/mcp` and drives the VM's screen, mouse and keyboard through Hyper-V.
- **`hyperhand-agent.exe`**, a tray program inside the guest. It runs commands, transfers files and handles the clipboard and windows in the logged-on user's session, talking to the host over a Hyper-V socket.

## Features

- **Screen, mouse and keyboard from the host**: screenshots at the guest's resolution, clicks, drag, wheel, typing and key combinations. Works on the login screen and UAC prompts too, since it does not depend on anything running in the guest.
- **Commands in the guest**: run PowerShell or cmd in the user's desktop session and get the exit code, stdout and stderr back; optionally elevated.
- **File transfer**: copy files or whole directories in either direction, streamed at hundreds of MB/s. Uploads skip files whose SHA-256 already matches the guest copy.
- **Checkpoints**: list, create and restore; a restored VM is started again automatically.
- **Clipboard, windows and waiting**: read and write the guest clipboard, bring a window to the front, wait until a process exits or a file appears.
- **No guest network, no guest password**: host and agent talk over a Hyper-V socket; the agent is copied in with Hyper-V's guest file copy and installed from the keyboard.
- **Cancellation**: when Claude Code cancels a long command, the agent kills it and is ready for the next request immediately.

## Requirements

- Windows 10/11 Pro or Enterprise host with Hyper-V, and an administrator account on it.
- A Windows guest with a logged-on user.
- VMConnect in basic session mode. Enhanced session moves the user's session to remote desktop, so host-side screenshots and input would reach the console lock screen instead.
- Go 1.27 or later to build.
- Claude Code (any MCP client that supports Streamable HTTP works).

## Install

```powershell
go build -ldflags "-H windowsgui" -o build\hyperhand.exe .\cmd\hyperhand
go build -ldflags "-H windowsgui" -o build\hyperhand-agent.exe .\cmd\hyperhand-agent
build\hyperhand.exe install
```

`install` asks for UAC once, registers a scheduled task that starts `hyperhand.exe` elevated at logon (from where it is now) and starts it. Keep `hyperhand-agent.exe` in the same folder.

## Quick start

1. Add HyperHand to Claude Code and start a new session:

   ```powershell
   claude mcp add --transport http hyperhand http://127.0.0.1:8770/mcp
   ```

2. Start the VM and log on in the guest. Switch the guest keyboard to English: the agent's install command is typed on the keyboard, and an input method in Chinese mode would garble it.
3. Ask Claude to install the agent (`vm_install_agent`). It copies the agent into the guest, runs its installer and waits until it answers. The agent then starts at every logon.
4. Ask Claude to work in the VM: take a screenshot, open an application, run a command, copy a build in, restore a checkpoint.

## Tools

Every tool takes an optional `vm` (VM name). Without it, the only running VM is used; if none is running and exactly one exists, that one.

| Tool | What it does |
|---|---|
| `vm_list`, `vm_start`, `vm_stop` | List VMs with their state; start; turn off |
| `vm_checkpoints`, `vm_checkpoint`, `vm_restore` | List, create and restore checkpoints (exact names); restore starts the VM unless `start` is false |
| `vm_screenshot` | PNG of the VM screen; `source`: `host` (default) or `agent` |
| `vm_click`, `vm_drag`, `vm_scroll` | Mouse at screenshot pixel coordinates |
| `vm_type`, `vm_key` | Type text (pasted through the guest clipboard); press keys such as `enter`, `ctrl+v`, `win+r` |
| `vm_exec` | Run a command in the guest; `shell`, `cwd`, `timeout_ms`, `admin` |
| `vm_push`, `vm_pull` | Copy files or directories host to guest and back; `vm_push` skips unchanged files unless `force` is true |
| `vm_clipboard_get`, `vm_clipboard_set` | Read or write the guest clipboard |
| `vm_focus_window` | Bring a window to the front by title |
| `vm_wait` | Wait until a process exits or runs, or a file exists |
| `vm_install_agent`, `vm_update_agent` | Install or replace the guest agent |

Screen, mouse, keyboard, VM and checkpoint tools work without the agent; the others need it.

## Known limitations

- One AI client at a time: requests to a VM's agent are handled one after another.
- `vm_install_agent` types its command on the keyboard; the guest input method must be in English mode.
- `admin` commands elevate without a prompt only if the guest's UAC is set to elevate administrators without prompting; otherwise they wait at the UAC prompt.
- Host-side screenshots and input act on the VM console; they do not reach a remote desktop or enhanced session.
- Checkpoints cannot be deleted from HyperHand.

## Privacy

HyperHand sends no telemetry and makes no network connections beyond the local MCP endpoint on `127.0.0.1`. Host and guest talk over Hyper-V sockets. The MCP endpoint has no authentication: any local process that can reach `127.0.0.1:8770` can control the VMs. See [docs/privacy.md](docs/privacy.md).

## Uninstall

1. Host: delete the scheduled task `HyperHand`, quit the tray, delete the build folder.
2. Guest: quit the agent from its tray menu, delete the `HyperHandAgent` value under `HKCU\Software\Microsoft\Windows\CurrentVersion\Run` and the folders `%LOCALAPPDATA%\HyperHand` and `C:\Users\Public\HyperHand`.

Details in the [user guide](docs/user-guide.md#uninstalling).

## Documentation

- [User guide](docs/user-guide.md): setup step by step, updating, troubleshooting.
- [Features](docs/features.md): detailed behaviour of every tool.
- [Privacy](docs/privacy.md): what is stored and what goes over the wire.
- [Changelog](CHANGELOG.md)

## Disclaimer

HyperHand is an independent project, not affiliated with or endorsed by Microsoft or Anthropic. Hyper-V is a trademark of Microsoft. HyperHand gives an AI full control of your virtual machines; use it with VMs you can afford to lose and keep checkpoints.
