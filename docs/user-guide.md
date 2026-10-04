# User Guide

This guide covers building HyperHand, installing the host tray and the guest agent, connecting Claude Code, updating, uninstalling, and troubleshooting.

HyperHand has two parts:

- `hyperhand.exe` runs on the Hyper-V host. It is a tray program that serves MCP over Streamable HTTP at `http://127.0.0.1:8770/mcp` and controls VMs through Hyper-V WMI and PowerShell. Screenshots, mouse, keyboard, VM state and checkpoints work without anything installed in the guest.
- `hyperhand-agent.exe` runs inside the guest, in the logged-on user's desktop session. It answers requests from the host over a Hyper-V socket and provides command execution, file transfer, clipboard, window focus, waits and an in-guest screenshot.

## Requirements

- Host: Windows 10 or Windows 11 Pro or Enterprise with Hyper-V enabled. The tray must run elevated; it relaunches itself through UAC if started without elevation.
- Guest: a Windows VM with a user logged on to the desktop.
- Go 1.27 or later to build.
- VMConnect in basic session mode. In an enhanced session, the guest user session moves to RDP, and the host-side screenshot and input reach the console session, which shows the lock screen. Switch with View > Enhanced Session in VMConnect, or close VMConnect while HyperHand is working.

## 1. Build

From the repository root:

```
go build -ldflags "-H windowsgui" -o build\hyperhand.exe .\cmd\hyperhand
go build -ldflags "-H windowsgui" -o build\hyperhand-agent.exe .\cmd\hyperhand-agent
```

`-H windowsgui` builds both as GUI programs, so no console window appears.

Keep `hyperhand-agent.exe` in the same directory as `hyperhand.exe`. `vm_install_agent` and `vm_update_agent` take the agent from there.

## 2. Install the host tray

```
build\hyperhand.exe install
```

This shows one UAC prompt, then:

1. Creates (or replaces) a scheduled task named `HyperHand` that runs `hyperhand.exe` from its current location, at logon, with highest privileges. The task has no run time limit and is allowed to start and keep running on battery.
2. Starts the task immediately. A tray icon appears and a message box confirms the path and the MCP URL.

The task points at the exe where it is. If you move the exe, run `install` again from the new location.

On every start the tray registers the HyperHand Hyper-V socket service under `HKLM\SOFTWARE\Microsoft\Windows NT\CurrentVersion\Virtualization\GuestCommunicationServices\3ce544e1-2645-4383-b332-fedf8a18736b`. Only one tray instance runs at a time.

The tray menu shows the MCP URL, or the error if the port could not be opened.

### Using a different port

```
hyperhand.exe -port 8771
```

The server always listens on `127.0.0.1`. The scheduled task created by `install` runs without arguments; to use another port at logon, add `-port <n>` to the task's action arguments in Task Scheduler, and use the matching URL when adding the server to Claude Code.

## 3. Add HyperHand to Claude Code

```
claude mcp add --transport http hyperhand http://127.0.0.1:8770/mcp
```

Start a new Claude Code session afterwards. The tools are loaded when the session starts.

All tools take an optional `vm` argument (the VM name). If it is omitted, HyperHand uses the only running VM, or the only VM if none is running. With several running VMs, pass `vm` explicitly. `vm_list` shows names, states and IDs.

## 4. Install the guest agent

Before you start:

- The VM is running and a user is logged on to the desktop.
- The guest keyboard layout and IME are in English mode. The install command is typed on the keyboard, and a non-English IME can swallow or alter the keystrokes. See [Set the guest default input method to English](#set-the-guest-default-input-method-to-english).

Ask Claude to call `vm_install_agent`. It:

1. Enables the Guest Service Interface integration service if it is off (required by `Copy-VMFile`; no guest password is needed).
2. Copies `hyperhand-agent.exe` to `C:\Users\Public\HyperHand\hyperhand-agent.exe` in the guest.
3. Opens the Run dialog (`win+r`) and types `C:\Users\Public\HyperHand\hyperhand-agent.exe install`, then Enter.
4. Waits up to 30 seconds for the agent to answer, and reports its version, the guest host name and the user it runs as.

The agent installer, running as the logged-on user:

- Copies itself to `%LOCALAPPDATA%\HyperHand\hyperhand-agent.exe`.
- Adds the value `HyperHandAgent` under `HKCU\Software\Microsoft\Windows\CurrentVersion\Run` so it starts at that user's logon. No administrator rights are needed.
- Starts the agent. A tray icon appears in the guest; its menu shows whether the host is connected.

If the install fails, call `vm_screenshot` to see what the guest shows, fix the cause (for example the IME mode or a dialog in the way) and run `vm_install_agent` again.

Tools that need the agent: `vm_exec`, `vm_push`, `vm_pull`, `vm_clipboard_get`, `vm_clipboard_set`, `vm_focus_window`, `vm_wait`, `vm_update_agent`, and `vm_screenshot` with `source: agent`. `vm_type` uses the agent when it is available.

## 5. Optional guest setup

### Allow `admin` exec without a prompt

`vm_exec` with `admin: true` starts the command elevated through the `runas` verb. If UAC prompts administrators for consent, the command waits for that prompt in the guest until it times out. To elevate without a prompt, set the following in the guest (from an elevated PowerShell):

```
Set-ItemProperty -Path HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\Policies\System -Name ConsentPromptBehaviorAdmin -Value 0
```

The logged-on user must be a member of the Administrators group.

### Set the guest default input method to English

If the guest uses a non-English input method by default, add the en-US language and make the US keyboard the default input method (in the guest, as the logged-on user):

```
$list = Get-WinUserLanguageList
$list.Add("en-US")
Set-WinUserLanguageList $list -Force
Set-WinDefaultInputMethodOverride -InputTip "0409:00000409"
```

Sign out and back in for the change to apply.

## Using the tools

- `vm_screenshot` returns a PNG and its size. Its pixel coordinates are the coordinates for `vm_click`, `vm_drag` and `vm_scroll`. The default `source: host` reads the Hyper-V console and works without the agent; `source: agent` captures inside the guest.
- `vm_type` pastes text through the guest clipboard (`clipboard_set` followed by `ctrl+v`), so an IME cannot alter it. Without the agent, it falls back to typing ASCII text on the keyboard; non-ASCII text then fails.
- `vm_key` takes a key or a combination such as `enter`, `ctrl+v`, `win+r`, `alt+f4`. Use `plus` for the `+`/`=` key, for example `ctrl+plus`.
- `vm_exec` runs as the logged-on user with `powershell` (default) or `cmd`. The default timeout is 60 seconds (`timeout_ms`). The result has the exit code, stdout, stderr and whether it timed out.
- `vm_push` and `vm_pull` copy a file or a directory recursively. A single file pushed to a guest path ending in `\` goes into that directory under its own name; a single file pulled to an existing host directory, or to a path ending in `\`, goes into it under the guest file's name. `vm_push` skips files whose SHA-256 already matches the guest copy unless `force` is true. Files are written to `<path>.hhpart` and renamed when complete.
- `vm_wait` waits for `process_exit` or `process_running` (with `name`, for example `notepad`) or `file_exists` (with `path`, for example `C:\temp\app\done.txt`). The default timeout is 60 seconds.
- `vm_restore` takes the exact checkpoint name (case-sensitive, no wildcards). If the VM is not running after the restore, it is started unless `start` is false.

## Updating

1. Rebuild. While the tray is running, `build\hyperhand.exe` is locked, so build the host to `build\hyperhand.exe.new`:

   ```
   go build -ldflags "-H windowsgui" -o build\hyperhand.exe.new .\cmd\hyperhand
   go build -ldflags "-H windowsgui" -o build\hyperhand-agent.exe .\cmd\hyperhand-agent
   ```

2. From an elevated PowerShell, run:

   ```
   scripts\restart-tray.ps1
   ```

   The script stops the running tray, moves `build\hyperhand.exe.new` over `build\hyperhand.exe` if it exists, and starts the `HyperHand` scheduled task again. It assumes the task points at `build\hyperhand.exe` in the repository.

3. Ask Claude to call `vm_update_agent`. The host sends the `hyperhand-agent.exe` next to `hyperhand.exe` to the running agent, which replaces itself, restarts, and is pinged until it answers (up to 30 seconds). Repeat for each VM.

`vm_update_agent` needs a running agent. If the agent does not answer, use `vm_install_agent` instead.

## Uninstalling

On the host, from an elevated prompt:

1. Quit the tray from its menu.
2. Delete the scheduled task:

   ```
   schtasks /Delete /TN HyperHand /F
   ```

3. Delete the Hyper-V socket service registration:

   ```
   reg delete "HKLM\SOFTWARE\Microsoft\Windows NT\CurrentVersion\Virtualization\GuestCommunicationServices\3ce544e1-2645-4383-b332-fedf8a18736b" /f
   ```

4. Remove the server from Claude Code: `claude mcp remove hyperhand`.
5. Optionally delete `%LOCALAPPDATA%\HyperHand` (the log) and the build directory.

In each guest, as the user the agent was installed for:

1. Quit the agent from its tray menu, or run `taskkill /F /IM hyperhand-agent.exe`.
2. Delete the Run value:

   ```
   reg delete "HKCU\Software\Microsoft\Windows\CurrentVersion\Run" /v HyperHandAgent /f
   ```

3. Delete `%LOCALAPPDATA%\HyperHand` and `C:\Users\Public\HyperHand`.

## Troubleshooting

### The agent does not answer

Tools that need the agent fail, or `vm_install_agent` reports that the agent did not answer within 30 seconds.

- Check that a user is logged on in the guest and that the HyperHand tray icon is present there. The agent runs only in a logged-on desktop session.
- Check that the host tray is running. It registers the Hyper-V socket service at startup; without the registration the guest cannot accept the host's connections.
- If the VM was restored to a checkpoint taken before the agent was installed, install it again with `vm_install_agent`.
- Call `vm_screenshot` to see whether the install command reached the Run dialog intact.

### Typed text is garbled or missing

The host keyboard sends key strokes, and a non-English IME in the guest can swallow or convert them. This affects `vm_install_agent` and `vm_type` without the agent. Switch the guest IME to English mode, or set English as the default input method (see above). With the agent installed, `vm_type` pastes through the clipboard and is not affected.

### The screenshot is black or shows the lock screen

- The VM must be running. A stopped or saved VM has no video output.
- VMConnect must be in basic session mode. In an enhanced session, the host-side screenshot and input go to the console session, which is locked.
- If the guest screen is locked or the display is off, unlock it or wake it with `vm_click` or `vm_key`.
- `vm_screenshot` with `source: agent` captures inside the guest user's session.

### Port 8770 is already in use

The tray menu shows the error instead of the MCP URL, and the log records it. Start the tray with `-port <n>` (and change the scheduled task arguments as described in [Using a different port](#using-a-different-port)), then re-add the server in Claude Code with the new URL.

### `admin` exec hangs or times out

The elevation is waiting for a UAC consent prompt in the guest. Set `ConsentPromptBehaviorAdmin` to `0` as described in [Allow `admin` exec without a prompt](#allow-admin-exec-without-a-prompt). Commands still pending at the timeout are terminated.

### Several VMs are running

Tools without `vm` fail when more than one VM is running. Pass the VM name from `vm_list`.

### Log file

The host tray writes its log to `%LOCALAPPDATA%\HyperHand\hyperhand.log` for the user it runs as. It records the listening address, port errors, service registration errors and install errors.
