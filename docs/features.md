# HyperHand Features

This document describes the behaviour of HyperHand as implemented: the host tray program, the MCP tools it exposes, the guest agent and the protocol between them.

## 1. Architecture

HyperHand consists of two Windows executables.

- `hyperhand.exe` (host) runs as an ordinary user tray program and serves MCP over Streamable HTTP. The same executable runs separately as `HyperHandService`, under the dedicated virtual account `NT SERVICE\HyperHandService`, to perform WMI (`root\virtualization\v2`) and PowerShell Hyper-V operations.
- `hyperhand-agent.exe` (guest agent) runs inside the VM in the logged-on user's desktop session, shows a tray icon and answers requests from the host over a Hyper-V socket.

### 1.1 Host side

- The MCP endpoint is `http://127.0.0.1:<port>/mcp`, default port 8770 (see 9.1). It listens on the loopback interface only.
- One MCP server instance (name `hyperhand`, version stamped at build time, `dev` for source builds) serves all HTTP sessions.
- Screen capture, mouse, keyboard, VM state and checkpoints use Hyper-V through the service and work without the guest agent (see 3, 4).
- Commands, files, clipboard, window focus and waiting go through the guest agent (see 5, 6, 7).
- The tray requests specific broker operations over a local named pipe whose ACL permits the configured owner, SYSTEM, administrators and the service account. There is no arbitrary host command execution broker operation. Host file reads and writes stay in the ordinary tray process.
- The MCP HTTP endpoint remains unauthenticated. Its callers can use all exposed tools through the tray; the pipe ACL is not MCP authentication.

### 1.2 Hyper-V socket

- The agent listens on the Hyper-V socket service GUID `3ce544e1-2645-4383-b332-fedf8a18736b`, accepting connections from the parent partition.
- The installer registers this GUID under `HKLM\SOFTWARE\Microsoft\Windows NT\CurrentVersion\Virtualization\GuestCommunicationServices\<GUID>` with `ElementName` = `HyperHand`. Ordinary tray startup does not write this registration.
- The service dials the VM by its VM ID (`Msvm_ComputerSystem.Name`) and tunnels the connection to the tray. The guest service GUID is fixed; the broker does not accept an arbitrary guest socket service ID.

### 1.3 Connections and request ordering

- The host keeps one agent client per VM ID, holding at most one connection.
- Each client sends one request at a time and waits for its response; concurrent tool calls for the same VM queue on the client.
- A queued call can be cancelled or reach its context deadline without waiting for the active request to finish. It is not sent and does not interrupt the active request.
- Before reusing an idle connection the client probes it with a 1 ms read. A timeout means the connection is alive; EOF, any other error, or unexpected data marks it dead, and the client redials.
- If a request could not be sent, the client reconnects and sends it once more. The resend happens only if the request payload can be rewound (no payload, or a seekable source). A request that was sent is never resent, so a command cannot run twice.
- The client for a VM is closed and discarded after `vm_start`, `vm_shutdown`, `vm_turn_off` and `vm_restore` (see 3).

### 1.4 Cancellation

- When an MCP call is cancelled, the host forces the connection's deadline, which aborts the pending read or write, and drops the connection.
- The agent reads frames on a separate goroutine. The host sends nothing while it waits for a response, so a read error while a request runs means the host went away; the agent then cancels that request's context.
- Cancellation stops `exec` (the process tree is killed, see 5.4), `hash_files` and `wait`. The agent does not send a response after the host disconnected.

### 1.5 Agent service loop

- The agent serves one host connection at a time. When it ends, the agent waits for the next one.
- If the Hyper-V socket listener cannot be created, or accepting fails, the agent retries after 1 s.
- The agent tray menu shows `等待宿主机连接` (waiting for the host) or `宿主机已连接` (host connected), and a `退出` (quit) item.

## 2. VM Selection

Every tool except `vm_list` takes an optional `vm` argument.

- A non-empty `vm` is matched case-insensitively against VM names (`ElementName`).
  - No match: `VM "<name>" not found`.
  - Several matches: `several VMs are named "<name>"`.
- An empty `vm` selects the only running VM (`EnabledState` = 2).
  - If no VM is running and the host has exactly one VM, that VM is selected (so `vm_start` works without a name).
  - If no VM is running and there are several VMs: `no running VM`.
  - If several VMs are running: `several VMs are running; specify one by name`.
- Only `Msvm_ComputerSystem` objects whose `Name` is GUID-shaped are treated as VMs; the host computer itself is excluded.
- State names are `Running`, `Off`, `Saved` and `Paused`; any other state is reported as its numeric `EnabledState`.

## 3. VM and Checkpoint Tools

These tools use Hyper-V on the host and do not need the agent, except for the readiness and unlock steps in 3.7. Tools without a specific result return `ok`.

### 3.1 vm_list

`vm_list` lists all VMs, one per line, as `name<TAB>state<TAB>id`. With no VMs it returns `no VMs`. The `vm` argument is ignored.

### 3.2 vm_start, vm_shutdown and vm_turn_off

- `vm_start` requests state Running (`RequestStateChange` 2) unless the VM is already Running, then waits until the desktop is usable (see 3.7).
- `vm_shutdown` asks the guest to shut down through the Hyper-V shutdown integration service (`Msvm_ShutdownComponent.InitiateShutdown` with `Force` false), then checks the VM state every 2 seconds for up to 3 minutes until it is Off. Because shutdown is not forced, a program with unsaved work can keep Windows from shutting down; the tool then fails and the VM keeps running. A VM that is already off returns `VM <name> is already off`. If the integration service refuses the request (not running, guest not booted, disabled in the VM settings), the tool fails. It never turns the VM off itself. MCP clients and scripts must allow calls of more than 3 minutes for the wait to finish.
- `vm_turn_off` requests state Off (`RequestStateChange` 3), which turns the VM off at once like pulling the plug; unsaved guest work is lost.
- All three close the VM's agent client afterwards (see 1.3).
- For `vm_start` and `vm_turn_off`, WMI return value 0 means completed. When WMI returns 4096 (job started), the tool waits up to 45 seconds for the asynchronous job and checks its result; a job failure or timeout is reported rather than returning success on acceptance. The operation is not resent automatically.

### 3.3 vm_checkpoints

`vm_checkpoints` lists the VM's checkpoints sorted by creation time, one per line, as `name<TAB>yyyy-MM-dd HH:mm:ss`. With none it returns `no checkpoints`.

### 3.4 vm_checkpoint

`vm_checkpoint` creates a checkpoint named `name` with `Checkpoint-VM`.

### 3.5 vm_restore

`vm_restore` applies the checkpoint whose name equals `name` exactly (case-sensitive, no wildcards; the first match is used).

- An unknown name fails with `checkpoint not found: <name>`.
- The VM is resolved before the restore, so an empty `vm` still refers to the same VM after it stops running.
- After the restore the agent client is discarded.
- `start` (default `true`): if the VM is not `Running` after the restore, it is started. A production checkpoint restores to Off and a standard one to Saved; both are started. With `start` = `false` the VM is left as restored.

### 3.6 PowerShell-based operations

`vm_checkpoints`, `vm_checkpoint`, `vm_restore` and the file copy of `vm_install_agent` (see 8.1) run a hidden, non-interactive Windows PowerShell on the host with `$ErrorActionPreference = 'Stop'`, the VM looked up by ID and UTF-8 output. A failure is reported as `powershell: <error>: <stderr and stdout>`.

### 3.7 Session readiness and unlock: vm_start, vm_status, vm_unlock

These use the agent's `session_state` (see 10.2), which reports for the agent's own session: the lock state from `WTSQuerySessionInformation` (`WTSSessionInfoEx` SessionFlags), whether it is the console session (`WTSGetActiveConsoleSessionId`), whether keyboard input goes to a secure desktop the user cannot open (`OpenInputDesktop` fails with access denied: the sign-in screen's password box or a UAC prompt), and whether `LogonUI.exe` and `consent.exe` run in it (`WTSEnumerateProcesses`).

- When `vm_start` started a VM that was not running and **Open console when started** is checked for it in the tray (see 9.1), the tray opens its console in the background; `vm_start` does not wait for it.
- After the VM runs, `vm_start` pings the agent every 2 seconds for up to 90 seconds. If the session is not locked it checks again 3 seconds later, because Windows can lock a session right after an automatic sign-in. A locked session is unlocked as below. The result names the agent and the session state; an error says why the desktop is not usable (no agent answer, locked without a stored password, unlock failed, agent too old). The VM keeps running in every case.
- `vm_status` reports the power state, whether an unlock password is stored and, for a running VM, the agent's version and user (5-second ping), the session lock state, a non-console session and an open UAC prompt. It waits for nothing and changes nothing.
- `vm_unlock` unlocks a running VM's locked session; an unlocked session returns `the session is not locked`.
- Unlocking reads the VM's unlock password from Windows Credential Manager (generic credential `HyperHand:<VM name>`, stored from the tray, see 9.1). It refuses before any input when no password is stored, the password is not ASCII, or the session is not the console session. It presses `ctrl` to dismiss the lock screen curtain and waits 1.5 seconds. Then, holding the input lock (see 4.5), it presses `ctrl+a`, checks once more that the session is locked, is the console session, runs `LogonUI.exe`, has keyboard input on the secure desktop and shows no UAC prompt, and only then types the password and `enter`. It waits up to 15 seconds for the session to report unlocked. The password is typed once per call: a wrong password is reported, not retried, so that the account is not locked out. The password never appears in results, errors or logs.
- An agent too old to know `session_state` is reported with a request to run `vm_update_agent`, and nothing is typed.
- When `vm_focus_window` or `vm_click` with `window` or `handle` fails and the agent reports the session locked, the error ends with `(the guest session is locked: run vm_unlock)`.

## 4. Screen and Input

### 4.1 vm_screenshot

`vm_screenshot` returns a PNG image and a text item `<width>x<height>`.

- `source` = `host` (default): the image is taken from the Hyper-V console via WMI `GetVirtualSystemThumbnailImage`, at the current resolution of the VM's first video head. It works without the agent. Hyper-V delivers RGB565, which is converted to 8-bit RGBA, so colours are quantised.
  - A VM without a video head fails with `no video head (VM not running?)`.
- `source` = `agent`: the agent captures display 0 inside the guest. The agent process is DPI-aware, so the image is in physical pixels.
- Any other `source` fails with `unknown source "<value>"`.

Pixel coordinates of the host screenshot are the coordinates used by `vm_click`, `vm_drag` and `vm_scroll` (see 4.2).

### 4.2 Mouse: vm_click, vm_drag, vm_scroll

Mouse input goes through the VM's synthetic mouse (`Msvm_SyntheticMouse`) with absolute positions. If the device is not found the call fails with `Msvm_SyntheticMouse not found (VM not running?)`.

- `vm_click` moves to (`x`, `y`), waits 100 ms and clicks.
  - `button`: `left` (default), `right` or `middle`; anything else fails with `unknown button "<value>"`.
  - `double` = `true` clicks twice.
  - `window` (case-insensitive title substring) or `handle` (from `vm_windows`) makes (`x`, `y`) relative to the top-left corner of that window's visible frame. The agent lists the windows first (see 7.3), and the click is refused, with nothing clicked, if:
    - no window matches, or `window` matches more than one (the error lists their handles);
    - the window is not the foreground window (the error names the foreground window);
    - the window is disabled, as an owner window is while its modal dialog runs;
    - the point is outside the window;
    - the point is off screen, or another window (for example an always-on-top one) covers it: the agent checks which top-level window is at that screen point.
  - The check and the click are two steps, so a window that appears in between can still receive the click.
- `vm_drag` moves to (`x1`, `y1`), waits 100 ms, presses the left button, moves to (`x2`, `y2`) in 8 equal steps 50 ms apart, waits 100 ms and releases the button. The button is released even if a move fails.
- `vm_scroll` moves to (`x`, `y`), waits 100 ms and scrolls `delta` wheel notches (120 units each). A positive `delta` scrolls up, a negative one down.

### 4.3 Keyboard: vm_key

`vm_key` presses a key or a `+`-separated combination through the VM's synthetic keyboard (`Msvm_Keyboard`).

- Names are case-insensitive and surrounding spaces are ignored.
- Supported names:
  - Modifiers: `ctrl` / `control`, `shift`, `alt`, `win`.
  - Keys: `enter` / `return`, `esc` / `escape`, `tab`, `space`, `backspace`, `delete` / `del`, `insert`, `home`, `end`, `pageup`, `pagedown`, `up`, `down`, `left`, `right`, `f1` to `f12`.
  - Single letters `a`-`z` and digits `0`-`9`.
  - Punctuation keys `;` `=` `,` `-` `.` `/` `` ` `` `[` `\` `]` `'`, and `plus` for the `+`/`=` key (`+` itself is the separator).
- An unknown name fails with `unknown key "<name>" in "<keys>"` before any key is sent.
- A single key is sent as one key press. A combination presses the keys in order and releases them in reverse order; keys already pressed are released even if a later press fails.

### 4.4 Text: vm_type

`vm_type` enters `text` into the focused guest window.

- With the agent: the text is placed on the guest clipboard (`clipboard_set`) and pasted with `ctrl+v`. This bypasses the guest IME and supports any Unicode text. The previous clipboard content is replaced.
- Without the agent (the clipboard call fails for any reason):
  - ASCII text is typed through the synthetic keyboard (`TypeText`). An IME in Chinese mode may swallow it.
  - Text containing any non-ASCII character fails with `non-ASCII text needs the agent: <agent error>`.
- The clipboard step and the paste of one `vm_type` call are kept together; concurrent `vm_type` calls do not interleave.

### 4.5 Input serialisation

All mouse and keyboard operations in the host process are serialised by one lock, so concurrent tool calls never interleave their input events. This includes the keyboard steps of `vm_type` and `vm_install_agent`. The MCP process holds a second input lock around each input tool call and around the final check and password typing of an unlock (see 3.7), so no other HyperHand tool sends input in between.

## 5. Commands

### 5.1 vm_exec

`vm_exec` runs `command` in the guest as the logged-on user (the agent's user) and returns:

```
exit_code: <n>
timed_out: <true|false>
stdout:
<text>
stderr:
<text>
```

A non-zero exit code is not a tool error. Errors from the agent (unknown shell, start failure, cancellation) are tool errors.

### 5.2 Shells

- `shell` = `powershell` (default, also for an empty value): `powershell.exe -NoProfile -NonInteractive -ExecutionPolicy Bypass -Command` with `[Console]::OutputEncoding` set to UTF-8 before the command.
- `shell` = `cmd`: the command line is exactly `cmd.exe /d /s /c "<command>"` (AutoRun disabled, outer quotes stripped by `/s`).
- Any other value fails with `unknown shell "<value>"`.
- The process is started hidden, without a console window.

### 5.3 Working directory

`cwd` sets the working directory. When empty, the command inherits the agent's working directory.

### 5.4 Timeout

- `timeout_ms` defaults to 60000; zero or a negative value also means 60 s.
- For non-elevated commands, the shell is started suspended, assigned to a Windows Job Object and then resumed. On timeout the job's process tree is terminated, even if the shell has already exited, and the result has `timed_out: true`, with the exit code and whatever output was captured. Elevated commands use the separate path in 5.5.
- If the host disconnects while the command runs (see 1.4), the process tree is killed in the same way and no result is returned.
- After the process exits, the agent waits at most 5 s for its output pipes to close (for example when a child process still holds them).

### 5.5 Elevated execution (admin)

`admin` = `true` runs the command elevated. UAC may ask for consent or administrator credentials; waiting for it counts toward `timeout_ms`.

- A hidden, ordinary launcher process requests elevation of a one-shot worker. The launcher can be stopped even while UAC is waiting, leaving the agent available for the next request.
- The worker receives the command over a random, single-use local named pipe. It verifies the pipe server's PID and the original agent process's creation time. A late UAC approval after cancellation or timeout cannot retrieve the expired command.
- `cwd` defaults to the agent's working directory. `cmd` uses a temporary `%TEMP%\hh-admin-*.cmd` wrapper to select UTF-8 before a nested `cmd /d /s /c` parses the command. PowerShell uses a temporary `%TEMP%\hh-admin-*.ps1` file with a UTF-8 BOM, preserving support for long scripts. Both files are deleted afterwards. Explicit `exit N` is preserved; for PowerShell, a failed last statement otherwise returns exit code 1.
- The worker captures stdout/stderr and manages the command tree with a Windows Job Object. Pipe disconnection cancels the command; the original absolute deadline also terminates it. No second elevation prompt is needed to stop the command.
- A timeout before execution returns `timed_out: true`, exit code `-1` and empty output. Once execution starts, the agent allows up to 5 seconds for job cleanup and the captured result to return. Cancellation is reported as a tool error.

### 5.6 Output decoding

stdout and stderr are decoded independently.

- A leading UTF-8 BOM is removed.
- Valid UTF-8 is kept as is.
- Otherwise the bytes are decoded from the guest's OEM code page (the default for `cmd` output).

## 6. Files

`vm_push` and `vm_pull` need the agent. File contents are streamed in 1 MB buffers on both sides and are not held in memory.

### 6.1 vm_push

`vm_push` copies `host_path` (a file, or a directory recursively) into the guest at `guest_path`.

- Single file:
  - If `guest_path` ends in `\` or `/`, the file is written into that directory under its own name.
  - Otherwise `guest_path` is the destination file path.
- Directory: every file below `host_path` is written to `guest_path\<relative path>`. Only files are transferred; empty directories are not created. The host walk does not descend into symbolic links to directories.
- Parent directories in the guest are created as needed.
- The result is `<n> files copied, <m> unchanged skipped (<bytes> bytes sent)`.
- On failure the error names the guest path and ends with `(<n> files copied)`; files copied before the failure remain.

### 6.2 Unchanged-file skipping

Unless `force` is `true` (default `false`):

- The host asks the agent for the SHA-256 of every destination path (`hash_files`, in batches of 1000 paths). The agent reports an empty hash for a missing, directory or unreadable path.
- For each destination with a non-empty guest hash, the host computes the local file's SHA-256; if they match, the file is skipped.
- An agent that does not support `hash_files` (`unknown op`) receives every file.

With `force` = `true` no hashes are requested and every file is uploaded.

### 6.3 vm_pull

`vm_pull` copies `guest_path` (a file, or a directory recursively) to `host_path` on the host.

- The host first lists `guest_path` as a directory; if the listing fails, `guest_path` is treated as a file.
- Single file:
  - If `host_path` is an existing directory, or ends in `\` or `/`, the file is written into it under the guest file's name.
  - Otherwise `host_path` is the destination file path.
  - Parent directories on the host are created as needed.
- Directory: `host_path` is created and the tree is copied recursively. Empty guest directories are created on the host.
- Symbolic links, junctions (for example `Application Data`) and other irregular entries in guest directories are skipped.
- Reading a guest directory as a file fails with `<path> is a directory`.
- The result is `<n> files (<bytes> bytes) written to <host_path>`.
- On failure the error names the guest path and ends with `(<n> files copied)`.

### 6.4 Temporary .hhpart files

Both directions write each file to a unique `.hyperhand-*.hhpart` temporary file in the destination directory and rename it over the destination only after the whole file has been received, replacing an existing file. On any error only that transfer's temporary file is deleted and the destination is left unchanged. In the guest, a short or failed transfer is never renamed into place.

## 7. Clipboard, Windows and Waiting

These tools need the agent.

### 7.1 vm_clipboard_get and vm_clipboard_set

- `vm_clipboard_get` returns the guest clipboard as Unicode text; an empty string when the clipboard holds no text.
- `vm_clipboard_set` empties the clipboard and puts `text` on it as Unicode text.
- Opening the clipboard is retried 20 times, 50 ms apart; if it stays locked by another program the call fails with `clipboard busy`.

### 7.2 vm_focus_window

`vm_focus_window` brings to the foreground the first visible top-level window, in enumeration order, whose title contains `title` (case-insensitive substring). With `handle` (from `vm_windows`) it brings that window instead and ignores `title`; a handle that is not a visible top-level window fails with `no visible top-level window has handle <handle>`. Passing neither fails with `pass title or handle`. The host lists the windows before a focus by handle, so an agent too old to know handles is rejected instead of focusing an arbitrary window.

- A minimised window is restored first.
- If `SetForegroundWindow` alone does not work, the agent attaches to the foreground thread's input, injects a zero-distance mouse move (so the agent sent the last input) and retries. It injects no key, so the focused window receives no keystroke.
- No match fails with `no visible window title contains "<title>"`; a window that still does not reach the foreground fails with `could not bring "<title>" to the foreground`.
- The result is `focused: <full window title>` and, on a line of its own, `handle: <handle>`. An older agent returns only the title.

### 7.3 vm_windows

`vm_windows` lists the visible top-level windows, from the top of the Z order down, as a JSON array. Windows hidden by DWM (cloaked, such as suspended store apps) are left out. Each entry has:

- `handle`, `title`, `class`, `pid` and `process` (executable name; empty if the agent cannot query the process);
- `rect`: `left`, `top`, `right`, `bottom` of the visible frame (without invisible resize borders), in the pixel coordinates of `vm_screenshot` and `vm_click`;
- `enabled`, `foreground` and `minimized`;
- `owner`: the owner window's handle, omitted when there is none;
- `modal`: the owner window is disabled, as it is while a modal dialog runs.

An agent without this operation fails with `the guest agent is too old to list windows; run vm_update_agent`.

### 7.4 vm_wait

`vm_wait` polls the guest every 300 ms until a condition holds or `timeout_ms` (default 60000) expires, and returns `satisfied: true` or `satisfied: false`. A timeout is not an error.

- `kind` = `process_running`: a process named `name` exists.
- `kind` = `process_exit`: no process named `name` exists (true immediately if none was running).
- Process names are compared case-insensitively on the executable name; a directory part is ignored and `.exe` is added if missing, so `notepad`, `Notepad.exe` and `C:\Windows\notepad.exe` are equivalent.
- `kind` = `file_exists`: `path` exists (a file or a directory).
- An empty `name` or `path` for its kind, or an unknown `kind`, is an error.
- If the host disconnects, the wait stops (see 1.4).

## 8. Guest Agent Installation and Update

### 8.1 vm_install_agent

`vm_install_agent` installs the agent without a guest password. It needs a user logged on to the guest desktop and the guest IME in English mode.

1. The ordinary tray reads `hyperhand-agent.exe` next to its own executable, normally under `%ProgramFiles%\HyperHand`, and streams it to the service.
2. The service stages the contents in its working directory, enables the VM's Guest Service Interface if it is disabled (then waits 3 s) and copies the file with `Copy-VMFile` to `C:\Users\Public\HyperHand\hyperhand-agent.exe`, creating the path and overwriting. The service does not open a caller-supplied host source path.
3. It presses `win+r`, waits 1.5 s, types `C:\Users\Public\HyperHand\hyperhand-agent.exe install` on the synthetic keyboard and presses `enter`.
4. It pings the agent (see 8.4).

Because step 3 types blindly, a failure there is visible only on screen; the tool description advises checking with `vm_screenshot`.

### 8.2 Agent install command

`hyperhand-agent.exe install` runs in the user's session and needs no administrator rights. `vm_install_agent` runs it through the Run dialog; it can also be run by hand in the guest. Started without `install`, the agent runs for the current session only, without autostart.

- It force-terminates every other running `hyperhand-agent.exe` and waits 500 ms.
- Unless it already runs from there, it copies itself to `%LOCALAPPDATA%\HyperHand\hyperhand-agent.exe`, retrying up to 20 times 250 ms apart while the old image is still locked.
- It sets the `HKCU\Software\Microsoft\Windows\CurrentVersion\Run` value `HyperHandAgent` to the quoted installed path, so the agent starts at logon.
- It starts the installed copy.
- On error it shows a message box and exits with code 1.

`hyperhand-agent.exe uninstall` reverses the install. It runs as the logged-on user without administrator rights. Run it in the guest directly, not through `vm_exec`, because it stops the agent that would be executing it.

- It stops every other running `hyperhand-agent.exe`.
- It deletes the `HyperHandAgent` value under `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`.
- It deletes `%LOCALAPPDATA%\HyperHand` and `C:\Users\Public\HyperHand`; a folder it is running from is removed right after it exits.
- It shows a message box listing what was removed.

### 8.3 Single instance and startup

- The agent uses the mutex `HyperHandAgent`; a second instance exits immediately.
- At startup it marks itself DPI-aware and deletes a leftover `hyperhand-agent.exe.old` next to itself, retrying for up to 10 s while the previous version is still exiting.

### 8.4 Agent readiness check

`vm_install_agent` and `vm_update_agent` ping the agent every 2 s for up to 30 s, each ping limited to 5 s.

- Success returns `agent <version> running on <hostname> as <user>`.
- Otherwise the call fails with `agent did not answer within 30 s` together with the last error.

### 8.5 vm_update_agent

`vm_update_agent` replaces the running agent with the installed `hyperhand-agent.exe` next to the host executable. The agent must already be running.

1. The host sends the whole executable as the `update_agent` payload.
2. The agent writes `<exe>.new`, deletes any `<exe>.old`, renames the running `<exe>` to `<exe>.old` and `<exe>.new` to `<exe>`. If the last rename fails, the old file is renamed back and the update fails. An empty payload fails with `empty payload`.
3. After a successful update the agent sends its response, releases its mutex, starts the new executable with the same arguments and exits. It restarts even if the response could not be sent.
4. The host closes its connection, waits 2 s and pings the new agent (see 8.4).

The update replaces the file the agent is running from; the HKCU Run entry is unchanged.

## 9. Host Tray

### 9.1 Running

`hyperhand.exe [-port <n>]` starts the tray and the MCP server; `-port` defaults to 8770.

- The tray starts without requesting elevation. Hyper-V operations require the separately installed and running `HyperHandService`.
- A second instance exits immediately (mutex `Local\HyperHandTray`).
- It listens on `127.0.0.1:<port>`; protected machine configuration is performed only by the installer.
- The tray menu shows the MCP URL or listener error, a **Virtual machines** submenu, restart and quit actions. The submenu lists the Hyper-V VMs with their state (running, off, saved, paused), refreshed every 5 seconds, and marks VMs with a stored unlock password. Each VM's submenu has **Open console**, which brings an open Virtual Machine Connection window for that VM to the front (a visible window of `<system directory>\vmconnect.exe` whose title, such as `Win10 on localhost - Virtual Machine Connection` or a localized `localhost 上的 Win10 - 虚拟机连接`, names exactly that VM; a title in another layout is not matched, and a new console is opened) or else runs the `HyperHand Console` task with the VM name (see 9.2), after checking that the name is an existing VM's exact name without quotes, line breaks or a trailing backslash; **Open console when started**, a per-VM check box saved in `%LOCALAPPDATA%\HyperHand\settings.json` (see 3.7); a warning appears for both when the host allows enhanced session mode (`EnhancedMode` under `HKLM\SOFTWARE\Microsoft\Windows NT\CurrentVersion\Virtualization`); **Set unlock password...**, which asks in the Windows credential dialog for the password or PIN the guest lock screen asks for and stores it in Windows Credential Manager for the current user (non-ASCII passwords are refused), and **Clear unlock password**. The user name in the dialog is only a note. Restart replaces only this ordinary tray/MCP process, without UAC, preserving the selected port. Active MCP connections and requests are interrupted; the service, guest agent and VMs are not restarted.

### 9.2 install

`hyperhand.exe install` installs or updates the host service and ordinary tray.

- If not elevated, it relaunches itself elevated with `install`.
- It installs `hyperhand.exe` and `hyperhand-agent.exe` under `%ProgramFiles%\HyperHand`.
- It records the installing user's SID in the protected `%ProgramData%\HyperHand\config.json` and provides a service-writable `service-data` directory beneath it.
- It configures automatic Windows service `HyperHandService` under `NT SERVICE\HyperHandService` and adds that service account to Hyper-V Administrators. It does not add the human user or use LocalSystem.
- It creates or replaces the `HyperHand` logon task for the installing user with least privilege and the installed executable. Reinstalling migrates the older highest-privilege task.
- It creates or replaces the `HyperHand Console` task for the installing user: no trigger, highest privileges, one action `<system directory>\vmconnect.exe localhost "$(Arg0)"` (the system directory from `GetSystemDirectory`, not an environment variable). An existing task of that name must belong to the installing user and run `vmconnect.exe`, or installation stops.
- It registers the fixed guest socket service (see 1.2) and starts the host components. Repeating `install` deploys an update; normal tray restart does not update binaries.
- On error it logs the error and shows it in a message box.

`hyperhand.exe uninstall` reverses the install.

- If not elevated, it relaunches itself elevated with `uninstall` (one UAC prompt).
- It stops the installed tray and service and removes `HyperHandService`, its Hyper-V Administrators membership, the `HyperHand` logon task, the `HyperHand Console` task and the fixed guest socket registration.
- A protected cleanup helper waits for the installed executable to exit, then removes the installed host and agent executables only if their hashes still match. Only empty directories are removed.
- User logs, guest files and nonempty service working data are preserved. If working data remains, the owner configuration is retained for reinstallation; otherwise the configuration and empty data directory are removed.
- It does not uninstall guest agents or change host UAC policy.

### 9.3 Log

The tray appends its log to `%LOCALAPPDATA%\HyperHand\hyperhand.log` (the directory is created if needed). The service writes errors to `%ProgramData%\HyperHand\service-data\broker.log`, with an approximately 1 MiB size limit. It also attempts to report errors to the Windows Application event log under `HyperHandService`; the file log remains available if that event source is unavailable.

## 10. Wire Protocol

The host and agent exchange frames over the Hyper-V socket, tunneled through the local broker pipe. This section describes the guest protocol, not the broker's control messages.

### 10.1 Frame format

```
uint32 header length | uint64 payload length | header JSON | payload bytes
```

- Both lengths are big-endian.
- A request header is `{"op": "<op>", "args": {...}}` (`args` omitted when empty).
- A response header is `{"error": "<message>", "result": {...}}`; both fields are omitted when empty. A response with an error carries no payload.
- Payloads carry file contents, screenshots and the agent executable; they are empty otherwise. File payloads are streamed rather than buffered.
- If a header is not valid JSON, the receiver skips its payload so the stream stays in sync.

### 10.2 Operations

| Op | Args | Result / payload |
|---|---|---|
| `ping` | none | `{version, hostname, user}` |
| `exec` | `{command, shell, cwd, timeout_ms, admin}` | `{exit_code, stdout, stderr, timed_out}` |
| `write_file` | `{path}` + payload (file contents) | none |
| `read_file` | `{path}` | payload (file contents) |
| `list_dir` | `{path}` | `{entries: [{name, is_dir}]}`, links skipped |
| `hash_files` | `{paths}` | `{hashes}`, lowercase hex SHA-256 in order, `""` when unavailable |
| `screenshot` | none | payload (PNG) |
| `clipboard_get` | none | `{text}` |
| `clipboard_set` | `{text}` | none |
| `focus_window` | `{title, handle}` | `{text, handle}`, the focused window's title and handle |
| `window_at` | `{x, y}` | `{handle}`, the top-level window a click at that screen point reaches; 0 off screen |
| `list_windows` | none | `{windows: [{handle, title, class, pid, process, rect, enabled, foreground, minimized, owner, modal}]}` |
| `wait` | `{kind, name, path, timeout_ms}` | `{satisfied}` |
| `session_state` | none | `{locked, console, secure_desktop, logonui, consent}` for the agent's session (see 3.7) |
| `update_agent` | payload (new executable) | none; the agent then restarts (see 8.5) |

### 10.3 Error behaviour

- An unknown op returns the error `unknown op "<op>"`.
- A handler that panics returns the error `<op> panicked: <value>`; the agent keeps running.
- For `write_file`, the agent always consumes the full payload, so a failed write (for example an invalid path) is reported as an error without breaking the connection. Only a failed read from the socket ends the connection.
