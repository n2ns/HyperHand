# User Guide

This guide covers downloading or building HyperHand, installing the host tray and the guest agent, connecting an MCP client (Claude Code, Codex or another client), updating, releasing, uninstalling, and troubleshooting.

HyperHand has two executables and three roles:

- `hyperhand.exe` runs as an ordinary user tray program on the Hyper-V host. It serves MCP over Streamable HTTP at `http://127.0.0.1:8770/mcp` and reads and writes host files using that user's permissions.
- `HyperHandService` runs the same executable as a Windows service under `NT SERVICE\HyperHandService`. The tray requests specific Hyper-V operations over an access-controlled local named pipe. Screenshots, mouse, keyboard, VM state and checkpoints work without anything installed in the guest; the service also tunnels the guest agent connection.
- `hyperhand-agent.exe` runs inside the guest, in the logged-on user's desktop session. It answers requests from the host over a Hyper-V socket and provides command execution, file transfer, clipboard, window focus, waits and an in-guest screenshot.

## Requirements

- Host: Windows 10 or Windows 11 Pro or Enterprise with Hyper-V enabled. Installation, updates and uninstallation require administrator approval; normal tray startup and restart do not.
- Guest: a Windows VM with a user logged on to the desktop.
- Go 1.27 or later, only to build from source.
- VMConnect in basic session mode. In an enhanced session, the guest user session moves to RDP, and the host-side screenshot and input reach the console session, which shows the lock screen. Switch with View > Enhanced Session in VMConnect, or close VMConnect while HyperHand is working.

## 1. Download or build

Download `hyperhand-X.Y.Z-windows-amd64.zip` from [Releases](https://github.com/n2ns/hyper-hand/releases). It contains `hyperhand.exe`, `hyperhand-agent.exe` (both Windows amd64, with the version stamped in), `README.md` and `CHANGELOG.md`. Extract both executables to the same folder. The installer copies them to `%ProgramFiles%\HyperHand`; the logon task uses that installed copy.

To build from source instead, from the repository root:

```
go build -ldflags "-H windowsgui" -o build\hyperhand.exe .\cmd\hyperhand
go build -ldflags "-H windowsgui" -o build\hyperhand-agent.exe .\cmd\hyperhand-agent
```

`-H windowsgui` builds both as GUI programs, so no console window appears. A source build reports its version as `dev`.

Keep `hyperhand-agent.exe` in the same directory as `hyperhand.exe` when installing or updating. Guest installation and update use the installed agent executable.

## 2. Install the host service and tray

From the folder with `hyperhand.exe` (`build` for a source build):

```
hyperhand.exe install
```

This shows one UAC prompt, then:

1. Copies both executables to `%ProgramFiles%\HyperHand` and records the installing user's SID in `%ProgramData%\HyperHand\config.json`.
2. Installs the automatic Windows service `HyperHandService`, running as `NT SERVICE\HyperHandService`. Only this dedicated account is added to Hyper-V Administrators. It does not add your user account or run the service as LocalSystem.
3. Registers the HyperHand Hyper-V socket service under `HKLM\SOFTWARE\Microsoft\Windows NT\CurrentVersion\Virtualization\GuestCommunicationServices\3ce544e1-2645-4383-b332-fedf8a18736b`.
4. Creates or replaces the `HyperHand` logon task for the installing user, using the installed executable with least privilege. This replaces the old highest-privilege task.
5. Creates or replaces the `HyperHand Console` task for the installing user: it has no trigger, runs `vmconnect.exe localhost "<VM>"` with that user's highest privileges, and is run by the tray to open a VM's console (see [Watch a VM](#watch-a-vm)).
5. Starts the service and ordinary tray.

Run `install` again from a new release or build to update the installed copies. Keep the service installation separate from normal tray startup: launching the installed executable without `install` does not request UAC or change machine configuration.

Only one tray instance runs at a time. Its connection to the service uses a local named pipe restricted to the configured owner, SYSTEM, administrators and the service account. This does not add authentication to the local MCP HTTP endpoint.

Click the HyperHand tray icon, or right-click it and choose **Settings...**, to open the settings window. It shows the MCP URL (with **Copy**), or the error if the port could not be opened, and whether the background service is running.

### Restarting the tray

Use the tray's restart action to restart the tray and MCP listener as the ordinary user, without a UAC prompt. It preserves the selected port. This disconnects MCP clients and interrupts in-progress requests; wait for the tray to return, then reconnect or retry from your MCP client. It does not restart the Windows service, guest agent or any VM. Use `install` to deploy updated binaries instead.

### Using a different port

In the settings window, check **Change port**, enter a port from 1024 to 65535 and click **Apply and restart**. The port is saved for your user and also used at logon. Use the matching URL when adding the server to your MCP client.

For a single run, start the tray with a port instead; it then ignores the saved port:

```
hyperhand.exe -port 8771
```

The server always listens on `127.0.0.1`.

## 3. Add HyperHand to an MCP client

Claude Code:

```
claude mcp add --transport http hyperhand http://127.0.0.1:8770/mcp
```

Codex:

```powershell
codex mcp add hyperhand --url http://127.0.0.1:8770/mcp
```

or in `~/.codex/config.toml`:

```toml
[mcp_servers.hyperhand]
url = "http://127.0.0.1:8770/mcp"
```

Other clients: add a Streamable HTTP server with the URL `http://127.0.0.1:8770/mcp`. No token or other authentication is needed.

Start a new session in the client afterwards. The tools are loaded when the session starts.

All tools take an optional `vm` argument (the VM name). If it is omitted, HyperHand uses the only running VM, or the only VM if none is running. With several running VMs, pass `vm` explicitly. `vm_list` shows names, states and IDs.

## 4. Install the guest agent

Before you start:

- The VM is running and a user is logged on to the desktop.
- The guest keyboard layout and IME are in English mode. The install command is typed on the keyboard, and a non-English IME can swallow or alter the keystrokes. See [Set the guest default input method to English](#set-the-guest-default-input-method-to-english), or [install the agent manually](#install-the-guest-agent-manually).

Ask the AI to call `vm_install_agent`. It:

1. Enables the Guest Service Interface integration service if it is off (required by `Copy-VMFile`; no guest password is needed).
2. Copies `hyperhand-agent.exe` to `C:\Users\Public\HyperHand\hyperhand-agent.exe` in the guest.
3. Opens the Run dialog (`win+r`) and types `C:\Users\Public\HyperHand\hyperhand-agent.exe install`, then Enter.
4. Waits up to 30 seconds for the agent to answer, and reports its version, the guest host name and the user it runs as.

The agent installer, running as the logged-on user:

- Copies itself to `%LOCALAPPDATA%\HyperHand\hyperhand-agent.exe`.
- Adds the value `HyperHandAgent` under `HKCU\Software\Microsoft\Windows\CurrentVersion\Run` so it starts at that user's logon. No administrator rights are needed.
- Starts the agent. A tray icon appears in the guest; its menu shows whether the host is connected.

If the install fails, call `vm_screenshot` to see what the guest shows, fix the cause (for example the IME mode or a dialog in the way) and run `vm_install_agent` again.

Tools that need the agent: `vm_exec`, `vm_push`, `vm_pull`, `vm_clipboard_get`, `vm_clipboard_set`, `vm_windows`, `vm_focus_window`, `vm_wait`, `vm_unlock`, `vm_update_agent`, `vm_screenshot` with `source: agent`, and `vm_click` with `window` or `handle`. `vm_type` uses the agent when it is available. `vm_start` starts the VM without the agent but reports that the desktop is not usable until the agent answers.

### Install the guest agent manually

Instead of `vm_install_agent`, you can install the agent yourself. This does not type anything on the keyboard, so the guest input method does not matter.

1. Copy `hyperhand-agent.exe` into the guest by any means: drag and drop in VMConnect, a shared folder, an ISO.
2. In the guest, as the logged-on user, run `hyperhand-agent.exe install`. No administrator rights and no UAC prompt are needed.

The installer does the same as above: it copies itself to `%LOCALAPPDATA%\HyperHand\`, adds the `HyperHandAgent` value under `HKCU\Software\Microsoft\Windows\CurrentVersion\Run` and starts the agent. Running `hyperhand-agent.exe` without `install` starts the agent for this session only, without autostart.

## 5. Optional guest setup

### Get a usable desktop after `vm_start`

`vm_start` waits until the agent answers and the session is unlocked. The agent starts when the user signs in, so the guest must reach a signed-in session on its own:

- **Automatic sign-in** (recommended): configure the guest to sign the user in at boot, for example with [Sysinternals Autologon](https://learn.microsoft.com/en-us/sysinternals/downloads/autologon). HyperHand cannot sign a user in at the sign-in screen.
- **Unlock password**: Windows can sign the user in and then lock the session, for example after an update. To let `vm_start` and `vm_unlock` unlock it, open the HyperHand settings window from the tray icon, select the VM under **Virtual machines** and click **Set unlock password...**, then enter the password or PIN the guest lock screen asks for (ASCII only). It is stored in Windows Credential Manager for your user as `HyperHand:<VM name>`; **Clear unlock password** removes it. HyperHand types it on the VM's keyboard only after the agent confirms that the sign-in screen's password box has the input, and types it once per call.

Use `vm_status` to see whether the agent answers, whether the session is locked and whether a password is stored.

### Watch a VM

HyperHand works on the VM console without any window open, so `vm_start` starts VMs in the background. To watch what the AI does, open the HyperHand settings window from the tray icon, select the VM under **Virtual machines** and click **Open console**. Check **Open console when started** to have Virtual Machine Connection open whenever `vm_start` starts that VM; the setting is per VM and per user (`%LOCALAPPDATA%\HyperHand\settings.json`). If a console for the VM is already open, it is brought to the front instead of opening a second one, which would ask to take over the connection.

- Virtual Machine Connection needs Hyper-V rights that your everyday (non-elevated) account does not have. The tray opens it through the `HyperHand Console` task, which `install` registered with your highest privileges, so there is no UAC prompt. Your account, its groups and the VM's permissions are not changed.
- Use a basic session. If the host allows enhanced session mode, the settings window warns you: an enhanced session moves the guest session away from the console, and HyperHand's screenshots and input reach the lock screen instead. Turn it off with **View > Enhanced Session** in Virtual Machine Connection, or turn off **Allow enhanced session mode** in the Hyper-V host settings.
- Typing or clicking in the console window while the AI works mixes your input with the AI's.

### Allow `admin` exec without a prompt

`vm_exec` with `admin: true` starts the command elevated through the `runas` verb. If UAC prompts administrators for consent, the command waits for that prompt in the guest until it times out. To elevate without a prompt, set the following in the guest (from an elevated PowerShell):

```
Set-ItemProperty -Path HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\Policies\System -Name ConsentPromptBehaviorAdmin -Value 0
```

The logged-on user must be a member of the Administrators group.

The change itself needs elevation, and it can be made through HyperHand once the agent is installed:

1. Request the change elevated; this opens a UAC prompt in the guest. Give the call a timeout long enough to answer the prompt, since the command waits for it:

   ```
   vm_exec  command: Start-Process reg.exe -Verb RunAs -Wait -ArgumentList 'add HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Policies\System /v ConsentPromptBehaviorAdmin /t REG_DWORD /d 0 /f'
            timeout_ms: 60000
   ```

2. While it waits, answer the prompt from the host: `vm_screenshot` shows it on the secure desktop; `vm_key shift+tab` moves the focus from No to Yes, then `vm_key enter` confirms. Host-side input reaches the secure desktop, unlike the agent.
3. Check: `vm_exec` with `(Get-ItemProperty HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\Policies\System).ConsentPromptBehaviorAdmin` returns `0`.

See [UAC](#uac) for every place a prompt can appear.

### Set the guest default input method to English

If the guest uses a non-English input method by default, add the en-US language and make the US keyboard the default input method (in the guest, as the logged-on user):

```
$list = Get-WinUserLanguageList
$list.Add("en-US")
Set-WinUserLanguageList $list -Force
Set-WinDefaultInputMethodOverride -InputTip "0409:00000409"
```

Sign out and back in for the change to apply.

## UAC

Where UAC prompts appear, and where they do not:

| When | Where | Prompt |
|---|---|---|
| `hyperhand.exe install` (including updates) | Host | Once per install/update, for the service, protected files and logon task. |
| `hyperhand.exe uninstall` | Host | Once; it relaunches itself elevated. |
| Starting the installed `hyperhand.exe`, at logon or by hand | Host | None. The tray runs as the ordinary user. |
| Restarting from the tray menu | Host | None. Only the tray/MCP process restarts; active requests are interrupted. |
| Updating with `scripts\restart-tray.ps1` | Host | Once per update; the script invokes the installer. This is not the tray-only restart action. |
| `vm_install_agent`, `hyperhand-agent.exe install`, `hyperhand-agent.exe uninstall`, `vm_update_agent`, the agent at logon | Guest | None. The agent runs as the logged-on user, not elevated. |
| `vm_exec` with `admin: true` | Guest | None if `ConsentPromptBehaviorAdmin` is `0` (see [Allow `admin` exec without a prompt](#allow-admin-exec-without-a-prompt)); otherwise a prompt that the command waits for. |
| A program started in the guest that asks for elevation | Guest | As configured in the guest; answer it from the host with `vm_screenshot` and `vm_key` (or `vm_click`). |

Host-side screenshots and input work on the guest's secure desktop, so a guest UAC prompt can always be answered through HyperHand. The agent cannot see or answer it.

## Using the tools

- `vm_screenshot` returns a PNG and its size. Its pixel coordinates are the coordinates for `vm_click`, `vm_drag` and `vm_scroll`. The default `source: host` reads the Hyper-V console and works without the agent; `source: agent` captures inside the guest.
- `vm_type` pastes text through the guest clipboard (`clipboard_set` followed by `ctrl+v`), so an IME cannot alter it. Without the agent, it falls back to typing ASCII text on the keyboard; non-ASCII text then fails.
- `vm_key` takes a key or a combination such as `enter`, `ctrl+v`, `win+r`, `alt+f4`. Use `plus` for the `+`/`=` key, for example `ctrl+plus`.
- `vm_exec` runs as the logged-on user with `powershell` (default) or `cmd`. The default timeout is 60 seconds (`timeout_ms`). The result has the exit code, stdout, stderr and whether it timed out.
- `vm_push` and `vm_pull` copy a file or a directory recursively. A single file pushed to a guest path ending in `\` goes into that directory under its own name; a single file pulled to an existing host directory, or to a path ending in `\`, goes into it under the guest file's name. `vm_push` skips files whose SHA-256 already matches the guest copy unless `force` is true. Files are written to unique `.hyperhand-*.hhpart` temporary files in the destination directory and renamed when complete.
- `vm_start` returns once the desktop is usable; if it fails, the VM may still be running, and the error says why (no agent answer, locked without a stored password, wrong password). `vm_status` reports the state without changing anything; `vm_unlock` unlocks a session that was locked later.
- `vm_windows` lists the visible windows with their handles and positions. Use a handle with `vm_focus_window` or `vm_click` when several windows share a title.
- `vm_click` with `window` or `handle` takes coordinates relative to that window and clicks only if the window is the enabled foreground window and the point is inside it, on screen and not covered by another window. Otherwise it fails and names the foreground window, so a click never lands on whatever happens to be in front.
- `vm_wait` waits for `process_exit` or `process_running` (with `name`, for example `notepad`) or `file_exists` (with `path`, for example `C:\temp\app\done.txt`). The default timeout is 60 seconds.
- `vm_restore` takes the exact checkpoint name (case-sensitive, no wildcards). If the VM is not running after the restore, it is started unless `start` is false.

## Updating

### From a release

1. Download the new zip from [Releases](https://github.com/n2ns/hyper-hand/releases) and extract both executables together.
2. Finish active VM tool requests, then run the extracted `hyperhand.exe install` and approve UAC. The installer updates the protected installation and restarts the host components. Existing highest-privilege tray tasks are migrated to the ordinary logon task.
3. Reconnect the MCP client after the tray returns.
4. Ask the AI to call `vm_update_agent` for each VM. It sends the installed `hyperhand-agent.exe` to the running agent, which replaces itself and restarts.

### From source

1. Rebuild both executables in `build`. The running service and tray use the installed copies under `%ProgramFiles%\HyperHand`:

   ```
   go build -ldflags "-H windowsgui" -o build\hyperhand.exe .\cmd\hyperhand
   go build -ldflags "-H windowsgui" -o build\hyperhand-agent.exe .\cmd\hyperhand-agent
   ```

2. Run the update wrapper and approve the installer's UAC prompt:

   ```
   scripts\restart-tray.ps1
   ```

   The script invokes `install` for the built executables. Despite its historical name, it deploys a build; use the tray's restart action when you only need to reconnect MCP without changing files. It does not terminate all processes by executable name.

3. Ask the AI to call `vm_update_agent`. The host sends the `hyperhand-agent.exe` next to `hyperhand.exe` to the running agent, which replaces itself, restarts, and is pinged until it answers (up to 30 seconds). Repeat for each VM.

`vm_update_agent` needs a running agent. If the agent does not answer, use `vm_install_agent` instead.

## Releasing

For maintainers. Add a section for the version to `CHANGELOG.md`, commit, and push a tag:

```
git tag v0.1.0
git push origin v0.1.0
```

Pushing a `vX.Y.Z` tag runs a GitHub Actions workflow that runs the tests, builds `hyperhand.exe` and `hyperhand-agent.exe` for Windows amd64 with the version stamped in, and publishes a GitHub Release with `hyperhand-X.Y.Z-windows-amd64.zip` (both exes, `README.md` and `CHANGELOG.md`). The release notes are the version's section of `CHANGELOG.md`.

## Uninstalling

Uninstall the guest agent first, then the host.

In each guest, as the user the agent was installed for, run:

```
%LOCALAPPDATA%\HyperHand\hyperhand-agent.exe uninstall
```

Run it in the guest itself: by hand, or from the host with `vm_key` `win+r`, `vm_type` for the command and `vm_key` `enter`. Do not run it through `vm_exec`: the uninstall stops the other running agent instances, including the one executing the command. It needs no elevation. It:

- stops the other running agent instances;
- deletes the `HyperHandAgent` value under `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`;
- deletes `%LOCALAPPDATA%\HyperHand` and `C:\Users\Public\HyperHand` (a folder it is running from is removed right after it exits);
- shows a message box listing what was removed.

On the host:

1. Run:

   ```
   hyperhand.exe uninstall
   ```

   It requests elevation if needed, stops the installed tray and service, removes `HyperHandService` and its Hyper-V Administrators membership, the `HyperHand` logon task, the `HyperHand Console` task and the Hyper-V socket registration. A protected cleanup helper waits for the installed executable to exit and removes the installed host and agent executables only if their hashes still match. The helper, `%ProgramData%\HyperHand\uninstall-cleanup.exe`, cannot delete itself; it is deleted at the next restart.

   User logs under `%LOCALAPPDATA%\HyperHand`, files inside guests and nonempty `%ProgramData%\HyperHand\service-data` are preserved. When service data remains, `config.json` is retained as the owner marker for reinstallation. Otherwise it removes the owner configuration and empty installation/data directories. It does not claim to remove directories containing other files.
2. Delete the folder with `hyperhand.exe` (the extracted release or the build directory).
3. Remove the server from your MCP client: `claude mcp remove hyperhand` (Claude Code) or `codex mcp remove hyperhand` (Codex).

## Troubleshooting

### The agent does not answer

Tools that need the agent fail, or `vm_install_agent` reports that the agent did not answer within 30 seconds.

- Check that a user is logged on in the guest and that the HyperHand tray icon is present there. The agent runs only in a logged-on desktop session.
- Check that the host tray and `HyperHandService` are running. Socket registration happens during installation, not normal tray startup; rerun `hyperhand.exe install` to repair an incomplete installation.
- If the VM was restored to a checkpoint taken before the agent was installed, install it again with `vm_install_agent`.
- Call `vm_screenshot` to see whether the install command reached the Run dialog intact.

### Typed text is garbled or missing

The host keyboard sends key strokes, and a non-English IME in the guest can swallow or convert them. This affects `vm_install_agent` and `vm_type` without the agent. Switch the guest IME to English mode, or set English as the default input method (see above). [Installing the agent manually](#install-the-guest-agent-manually) avoids the problem for the install. With the agent installed, `vm_type` pastes through the clipboard and is not affected.

### The screenshot is black or shows the lock screen

- The VM must be running. A stopped or saved VM has no video output.
- VMConnect must be in basic session mode. In an enhanced session, the host-side screenshot and input go to the console session, which is locked.
- If the guest screen is locked, run `vm_unlock` with an unlock password stored in the tray (see [Get a usable desktop after `vm_start`](#get-a-usable-desktop-after-vm_start)), or unlock it in VMConnect. If the display is off, wake it with `vm_click` or `vm_key`.
- `vm_screenshot` with `source: agent` captures inside the guest user's session.

### Port 8770 is already in use

The settings window shows the error as the server status, the tray icon's tooltip says that the MCP server is not running, and the log records it. Choose another port as described in [Using a different port](#using-a-different-port), then re-add the server in your MCP client with the new URL.

### `admin` exec hangs or times out

The elevation is waiting for a UAC consent prompt in the guest. Set `ConsentPromptBehaviorAdmin` to `0` as described in [Allow `admin` exec without a prompt](#allow-admin-exec-without-a-prompt). Commands still pending at the timeout are terminated.

### Several VMs are running

Tools without `vm` fail when more than one VM is running. Pass the VM name from `vm_list`.

### The host service is unavailable

Check `HyperHandService` in Windows Services. Normal tray startup does not repair or elevate the service. Rerun `hyperhand.exe install` with administrator approval to repair the installation. Use the configured owner's account for the tray; another account is not automatically granted broker access. Do not add the human user to Hyper-V Administrators as a workaround.

### Log file

The host tray writes its log to `%LOCALAPPDATA%\HyperHand\hyperhand.log` for the user it runs as. It records the listening address and startup, connection and install errors.

For service startup or broker failures, inspect `%ProgramData%\HyperHand\service-data\broker.log` with administrator access. The service limits it to approximately 1 MiB. Errors may also appear in Event Viewer under **Windows Logs > Application**, with source `HyperHandService`; use the file log if that source is unavailable.
