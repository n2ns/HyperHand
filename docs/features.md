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
- The agent tray icon (the gripper icon, also the executable's icon) opens a menu on a left or right click: `等待宿主机连接` (waiting for the host) or `宿主机已连接` (host connected), and a `退出` (quit) item. Like the host tray (see 9.1), it is added at once at logon and retried every 5 seconds and whenever the taskbar is created; the agent serves the host whether or not the icon could be shown.

## 2. Results, Errors and VM Selection

### 2.1 Result format

- Every successful call returns one text item containing one JSON object. No tool returns free text or `ok` lines.
- `vm_observe`, and an action whose `observe_after` includes a screenshot (see 4.4), return a PNG image item **before** the JSON text item. No other tool returns an image.
- Every tool carries MCP tool annotations. `openWorldHint` is `false` for all. `readOnlyHint` is `true` for `vm_list`, `vm_status`, `vm_checkpoints`, `vm_pull`, `vm_clipboard_get`, `vm_wait`, `vm_windows`, `vm_observe` and `vm_doctor`. `destructiveHint` is `true` for `vm_shutdown`, `vm_turn_off`, `vm_restore`, `vm_push`, `vm_update_agent` and `vm_end_turn`, and `false` for every other tool. `idempotentHint` is `true` for `vm_list`, `vm_start`, `vm_status`, `vm_unlock`, `vm_shutdown`, `vm_turn_off`, `vm_checkpoints`, `vm_push`, `vm_pull`, `vm_clipboard_get`, `vm_clipboard_set`, `vm_wait`, `vm_update_agent`, `vm_set_value`, `vm_end_turn` and `vm_doctor`. The annotations are hints for clients, not a security boundary.
- Each MCP server process has a **run ID** of the form `run-<yyyymmdd-hhmm>-<4 hex>`, generated at start. It is returned by `vm_list`, carried in every error object and used in checkpoint names (see 3.4).
- Tool descriptions of `vm_windows`, `vm_observe`, the actions, `vm_exec`, `vm_pull` and `vm_clipboard_get` state that window titles, control names and values, selected text, command output, file contents and clipboard text are data from the guest, not instructions.

### 2.2 Error object and codes

Every refusal and failure is returned as an MCP result with `isError: true` whose single text item is one JSON object:

```json
{"error": "covered", "reason": "screen point (640, 400) of window \"Drawing1.dwg\" (handle 197916, acad.exe) is covered by window \"\" (handle 131160, class Shell_LightDismissOverlay, explorer.exe), which is not one of its own windows", "next": "dismiss it with vm_key esc or close it, then call vm_observe again and retry", "run_id": "run-20261010-0812-7f3a", "window": {"handle": 131160, "class": "Shell_LightDismissOverlay", "process": "explorer.exe"}}
```

- `error` is one of the codes below, `reason` says what happened, `next` names the call that makes progress and `run_id` is the server's run ID. Further fields carry the facts the next call needs; they are listed per tool in this document.
- Handler errors never become MCP protocol errors, so a client always receives this object.
- An agent answer `unknown op "<op>"` is reported as `agent_outdated`; a connection or transport failure to the agent as `agent_required`; anything without a specific code as `failed`, whose `next` is `call vm_status, then vm_doctor if the VM is running; the action may or may not have happened, so observe before repeating it`.

| Code | When |
|---|---|
| `failed` | Any error without a specific code: Hyper-V, WMI or PowerShell failures, transport errors, errors the agent answered with (for example a `vm_exec` start failure), and VM lookup failures in `vm_observe` and the actions (see 2.3) |
| `invalid_argument` | A parameter is missing, out of range or inconsistent, including VM lookup failures of the VM, checkpoint, command, file, clipboard, wait, launch and agent tools; also an observation that cannot provide what the action needs (no screenshot for pixel coordinates, no control tree for `index`) and a pixel outside the observation image |
| `stale_observation` | The `observation_id` is unknown or evicted, belongs to another VM, or its window no longer exists, is minimized, or has moved or resized since the observation |
| `stale_element` | `index` is not in the observation's tree, or the control's runtime ID no longer resolves in the window when the host re-locates it before acting |
| `session_unusable` | A targeted action: the agent reports its session locked, not the VM console session, or keyboard input on the secure desktop (see 4.4 step 2); untargeted `vm_type` of non-ASCII text while the session is locked or on the secure desktop |
| `activate_failed` | The target window (or one of its own windows) is not in the foreground and could not be brought there, or `activate` is `false` |
| `target_disabled` | The target window, or the window of its group that would receive input, is disabled or minimized (typically a modal dialog is open) |
| `covered` | The screen point of a pointer action reaches a top-level window outside the target's group |
| `integrity_mismatch` | The target window's process runs at a higher integrity level than the agent, so Windows would drop the input (UIPI) |
| `target_not_responding` | A UI Automation action on the window did not finish within the host's 12 s, or the agent's own UI Automation helper timed out (10 s) |
| `unsupported_pattern` | The control does not support the requested pattern action; `supported` lists the patterns it does support |
| `partial_input` | `vm_type` or `vm_key` stopped after some input was injected; `applied_chars` / `total_chars` or `applied` / `total` say how much. A failure before anything was injected keeps the underlying code (`failed` with `applied_chars: 0`, or the refusal of the first combination) |
| `agent_required` | The operation needs the guest agent, which did not answer or is not installed |
| `agent_outdated` | The agent's protocol is older than the host's (`protocol` 2): every new agent connection is pinged first and every op except `ping` and `update_agent` is then refused (see 8.6); also an agent that answered `unknown op`. `next` is `call vm_update_agent` |
| `ambiguous_target` | `pid` alone selects a process with several visible windows; `handles` lists them |
| `no_window` | No visible window has the given `handle` or `pid`, no window is in the foreground when one is needed, or `vm_launch` saw no window in time |

### 2.3 VM selection

Every tool except `vm_list` takes an optional `vm` argument.

- A non-empty `vm` is matched case-insensitively against VM names (`ElementName`).
  - No match: `VM "<name>" not found`.
  - Several matches: `several VMs are named "<name>"`.
- An empty `vm` selects the only running VM (`EnabledState` = 2).
  - If no VM is running and the host has exactly one VM, that VM is selected (so `vm_start` works without a name).
  - If no VM is running and there are several VMs: `no running VM`.
  - If several VMs are running: `several VMs are running; specify one by name`.
- The VM, checkpoint, command, file, clipboard, wait, launch and agent tools report these as `invalid_argument` with `next` `call vm_list and pass one of its names as vm` (or `call vm_start with the VM's name, or pass vm` when no VM runs, `pass vm` when several run). `vm_observe` reports them as `failed` with `next` `call vm_list and pass vm`; the actions as `failed` with the default `next` of 2.2.
- Only `Msvm_ComputerSystem` objects whose `Name` is GUID-shaped are treated as VMs; the host computer itself is excluded.
- State names are `Running`, `Off`, `Saved` and `Paused`; any other state is reported as its numeric `EnabledState`. `vm_list` reports them as Hyper-V names them; `vm_status`, `vm_start`, `vm_shutdown`, `vm_turn_off` and `vm_restore` report lowercase (`running`, `off`, `saved`, `paused`).

## 3. VM and Checkpoint Tools

These tools use Hyper-V on the host and do not need the agent, except for the readiness and unlock steps in 3.7.

### 3.1 vm_list

`vm_list` lists all VMs and the server's run ID. The `vm` argument is ignored.

```json
{"vms": [{"name": "Win10", "id": "2F0A9B3C-...", "state": "Running"}], "run_id": "run-20261010-0812-7f3a"}
```

`vms` is `[]` when the host has no VMs.

### 3.2 vm_start, vm_shutdown and vm_turn_off

- `vm_start` requests state Running (`RequestStateChange` 2) unless the VM is already Running, then waits until the desktop is usable (see 3.7). Result: `{"vm": "Win10", "state": "running", "desktop": "usable", "agent": {"version": "0.3.0", "hostname": "WIN10", "user": "WIN10\\tester", "protocol": 2}, "unlocked": false}`; `unlocked` is `true` when the session was locked and `vm_start` unlocked it. When the desktop is not usable the refusal's `reason` starts with `VM <name> is running, but its desktop is not usable:` and its code is `agent_required` (no agent answer within 90 s), `agent_outdated` (fields `agent_protocol`, `host_protocol`) or `session_unusable` (field `locked: true`; the reason says why the unlock failed, see 3.7).
- `vm_shutdown` asks the guest to shut down through the Hyper-V shutdown integration service (`Msvm_ShutdownComponent.InitiateShutdown` with `Force` false), then checks the VM state every 2 seconds for up to 3 minutes until it is Off. Because shutdown is not forced, a program with unsaved work can keep Windows from shutting down; the tool then fails (`failed`, the reason names `vm_observe` and `vm_turn_off`) and the VM keeps running. If the integration service refuses the request (not running, guest not booted, disabled in the VM settings), the tool fails. It never turns the VM off itself. MCP clients and scripts must allow calls of more than 3 minutes for the wait to finish. Result, also for a VM that is already off: `{"vm": "Win10", "state": "off"}`.
- `vm_turn_off` requests state Off (`RequestStateChange` 3), which turns the VM off at once like pulling the plug; unsaved guest work is lost. Result: `{"vm": "Win10", "state": "off"}`.
- All three close the VM's agent client afterwards (see 1.3).
- For `vm_start` and `vm_turn_off`, WMI return value 0 means completed. When WMI returns 4096 (job started), the tool waits up to 45 seconds for the asynchronous job and checks its result; a job failure or timeout is reported rather than returning success on acceptance. The operation is not resent automatically.

### 3.3 vm_checkpoints

`vm_checkpoints` lists the VM's checkpoints sorted by creation time, oldest first:

```json
{"checkpoints": [{"name": "run-20261010-0812-7f3a-temp-step3", "created_at": "2026-10-10 16:12:03", "run_id": "run-20261010-0812-7f3a", "type": "temp", "label": "step3", "id": "8B1D...", "parent": "clean install"}]}
```

- `created_at` is `yyyy-MM-dd HH:mm:ss` in the host's local time; `id` is the checkpoint's Hyper-V ID; `parent` is the parent checkpoint's name (`""` for a root checkpoint).
- `run_id`, `type` and `label` are parsed from the name (see 3.4): `type` is `temp` or `keep` for names HyperHand created, and `manual` (with empty `run_id` and `label`) for every other name.
- `checkpoints` is `[]` when the VM has none.

### 3.4 vm_checkpoint

`vm_checkpoint` creates a checkpoint with `Checkpoint-VM`. It takes `label` (default: the current time as `hhmmss`) and `keep` (default `false`), and names the checkpoint `<run_id>-temp-<label>`, or `<run_id>-keep-<label>` with `keep: true`. Names match `^(run-\d{8}-\d{4}-[0-9a-f]{4})-(temp|keep)-(.+)$`.

- Result: `{"name": "run-20261010-0812-7f3a-temp-step3", "type": "temp", "created_at": "2026-10-10T08:12:03Z"}` (`created_at` is the host's UTC time in RFC 3339). Pass `name` to `vm_restore`.
- A `temp` checkpoint is remembered by the server and deleted by `vm_end_turn` (see 7.4); a `keep` checkpoint stays until deleted by hand. HyperHand never deletes checkpoints whose names it did not create.

### 3.5 vm_restore

`vm_restore` applies the checkpoint whose name equals `name` exactly (case-sensitive, no wildcards; the first match is used).

- A missing `name` is `invalid_argument` (`call vm_checkpoints and pass a name`); an unknown name is `invalid_argument` with reason `checkpoint not found: <name>`.
- The VM is resolved before the restore, so an empty `vm` still refers to the same VM after it stops running.
- After the restore the agent client is discarded.
- `start` (default `true`): if the VM is not `Running` after the restore, HyperHand starts it. With `start` = `false`, HyperHand skips this additional start operation and leaves the state produced by Hyper-V; it does not force Off or Saved. A running standard checkpoint can restore directly to Running. Standard checkpoints preserve memory state, whereas production checkpoints do not; see [Microsoft's checkpoint guide](https://learn.microsoft.com/en-us/windows-server/virtualization/hyper-v/checkpoints).
- Result: `{"vm": "Win10", "restored": "run-20261010-0812-7f3a-keep-clean", "state": "running"}`.
- Restore does not wait for an unlocked desktop or invoke the automatic console-opening hook. Check `vm_status` and call `vm_start` if desktop readiness is required.

### 3.6 PowerShell-based operations

`vm_checkpoints`, `vm_checkpoint`, `vm_restore`, the checkpoint deletion of `vm_end_turn` and the file copy of `vm_install_agent` (see 8.1) run a hidden, non-interactive Windows PowerShell on the host with `$ErrorActionPreference = 'Stop'`, the VM looked up by ID and UTF-8 output. A failure is reported as `failed` with reason `powershell: <error>: <stderr and stdout>`.

### 3.7 Session readiness and unlock: vm_start, vm_status, vm_unlock

These use the agent's `session_state` (see 10.2), which reports for the agent's own session: the lock state from `WTSQuerySessionInformation` (`WTSSessionInfoEx` SessionFlags), whether it is the console session (`WTSGetActiveConsoleSessionId`), whether keyboard input goes to a secure desktop the user cannot open (`OpenInputDesktop` fails with access denied: the sign-in screen's password box or a UAC prompt), and whether `LogonUI.exe` and `consent.exe` run in it (`WTSEnumerateProcesses`).

- When `vm_start` started a VM that was not running and **Open console when started** is checked for it in the tray (see 9.1), the tray opens its console in the background; `vm_start` does not wait for it.
- After the VM runs, `vm_start` pings the agent every 2 seconds for up to 90 seconds, refuses an agent whose protocol is older than the host's (`agent_outdated`, see 8.6; the same check guards every agent connection), and if the session is not locked checks again 3 seconds later, because Windows can lock a session right after an automatic sign-in. A locked session is unlocked as below. The VM keeps running in every case.
- `vm_status` reports the power state, whether an unlock password is stored and, for a running VM, the agent (5-second ping) and its session:

  ```json
  {"vm": "Win10", "power": "running", "unlock_password": "stored", "agent": {"state": "ok", "version": "0.3.0", "hostname": "WIN10", "user": "WIN10\\tester", "protocol": 2}, "session": {"locked": false, "console": true, "uac_prompt": false}}
  ```

  `unlock_password` is `stored`, `not stored` or `unknown` (Credential Manager could not be read). `agent.state` is `ok`, `busy` or `not_answering`; `busy` means this host already has a request in progress on the agent connection and the probe did not queue behind it (the session is then not queried); `not_answering` carries the error in `agent.error` and does not prove the agent is offline. `agent` and `session` are absent for a VM that is not running; `session_error` replaces `session` when the session query failed. Status changes nothing.
- `vm_unlock` unlocks a running VM's locked session. Result: `{"vm": "Win10", "state": "unlocked"}`, or `"state": "not_locked"` when nothing had to be typed. A VM that is not running is refused (`failed`, fields `vm` and `state`, `next` `call vm_start`).
- Unlocking reads the VM's unlock password from Windows Credential Manager (generic credential `HyperHand:<VM name>`, stored from the tray, see 9.1). It refuses before any input when no password is stored, the password is not ASCII, or the session is not the console session (all `failed`, with the reason saying so). It presses `ctrl` to dismiss the lock screen curtain and waits 1.5 seconds. Then, holding the input lock (see 4.5), it presses `ctrl+a`, checks once more that the session is locked, is the console session, runs `LogonUI.exe`, has keyboard input on the secure desktop and shows no UAC prompt, and only then types the password and `enter`. It waits up to 15 seconds for the session to report unlocked. The password is typed once per call: a wrong password is reported, not retried, so that the account is not locked out. The password never appears in results, errors or logs.
- An agent too old to know `session_state` is reported (`vm_unlock`: `failed` with a request to run `vm_update_agent`; `vm_start`: `agent_outdated`), and nothing is typed.
- Targeted actions refuse a locked session with `session_unusable` and `next` `call vm_unlock` (see 4.4); raw screen input is not checked, so the lock screen can be operated.

## 4. Observation and Input

### 4.1 Observations

An observation is what the host remembers about one `vm_observe` result so that later actions can be checked against it and mapped back to the screen.

- Each `vm_observe` call stores: the VM, the observed window (its handle, PID and `rect` at capture time; none for a whole-screen observation), the window list at capture time, the screenshot geometry (the captured screen region and the output scale per axis), the control tree nodes when one was read, and the **tree window** the nodes belong to (the observed window, or the foreground window whose tree a whole-screen observation read). It returns the observation's `observation_id`, `o-` followed by 8 hex digits.
- The host keeps the 8 most recent observations per VM. An `observation_id` that is unknown or evicted is refused with `stale_observation` (`call vm_observe again and use its observation_id`).
- **Coordinate mapping.** Host screenshots are taken at the guest's screen resolution, so screenshot pixels are guest screen pixels. For an image pixel `(u, v)` of an observation with crop origin `(x, y)`, crop size `w x h` and per-axis scales `scale_x`, `scale_y` (output pixels per screen pixel; integer rounding of the output size can make them differ slightly, and the result's `screenshot.scale` reports `scale_x`), the host computes the screen point `x + min(w - 1, floor((u + 0.5) / scale_x))`, `y + min(h - 1, floor((v + 0.5) / scale_y))`. Callers pass image pixels and never convert. A pixel outside the output image, or coordinates for an observation taken with `screenshot: false`, is `invalid_argument`.
- **Index resolution.** A control `index` names a node of the observation's tree. Every index action asks the agent to re-find the element by its `runtime_id` in the tree window (`control_action` `Locate`, which performs nothing and returns the element's current `rect` and value): pointer actions then click the centre of the **current** rectangle (`(left + right) / 2`, `(top + bottom) / 2`), so a control that moved since the observation (a scrolled list, a re-laid-out dialog) is hit where it is now; `vm_set_value` and `vm_invoke` act on the re-found element. A node without a runtime ID keeps the rectangle of the observation. A vanished element is `stale_element`; an index outside the tree is `stale_element`; an observation without a tree is `invalid_argument` (`call vm_observe with controls: true`).
- **Freshness.** Before an action, the host lists the windows again. For a window observation, the action is refused with `stale_observation` when the window no longer exists, is minimized, or its `rect` differs from the one at capture time (the field `rect` then carries the current rect). An observation of another VM is `stale_observation` too. Whole-screen observations are always fresh: their pixels are screen pixels. A whole-screen observation clicked by coordinates has no window checks at all; an index action on it targets the tree window with the full check chain (see 4.4).
- The `screenshot` object of the result (`origin_x`, `origin_y`, `scale`) documents the mapping for auditing; actions do not need it.

### 4.2 vm_windows

`vm_windows` lists the visible top-level windows, from the top of the Z order down. It needs the agent (`agent_required` otherwise). Windows hidden by DWM (cloaked, such as suspended store apps) are left out.

```json
{"windows": [{"handle": 197916, "title": "AutoCAD 2015 - Drawing1.dwg", "class": "Afx:400000:...", "pid": 4120, "process": "acad.exe", "rect": {"left": 0, "top": 0, "right": 1920, "bottom": 1040}, "enabled": true, "foreground": false, "minimized": false, "modal": false, "group_root": 197916, "integrity": "medium"},
             {"handle": 2818992, "title": "", "class": "HwndWrapper[...]", "pid": 4120, "process": "acad.exe", "rect": {"left": 0, "top": 980, "right": 1920, "bottom": 1040}, "enabled": true, "foreground": true, "minimized": false, "owner": 197916, "modal": false, "group_root": 197916, "integrity": "medium"}],
 "foreground": 2818992,
 "focused_control": {"window": 2818992, "name": "", "control_type": "Edit", "class_name": "Edit", "runtime_id": "42.2818992.4", "rect": {"left": 0, "top": 980, "right": 1920, "bottom": 1040}},
 "session": {"locked": false, "console": true, "secure_desktop": false, "logonui": false, "consent": false}}
```

- Each window has `handle`, `title`, `class`, `pid`, `process` (executable name; empty if the agent cannot query the process), `rect` (`left`, `top`, `right`, `bottom` of the visible frame without invisible resize borders, in physical screen pixels), `enabled`, `foreground`, `minimized`, `owner` (the owner window's handle, omitted when there is none), `modal` (the owner window is disabled, as it is while a modal dialog runs), `group_root` and `integrity`.
- `group_root` is the handle reached by following `owner` links through listed windows of the same process, at most 8 links; the window's own handle when it has no such owner. Windows with the same `group_root` form a **group** (the "own windows" of the root): in AutoCAD 2015 the untitled command line is owned by the main window and its command history popup by the command line, so all three share the main window's `group_root`. The actions treat a group as one target (see 4.4). Windows of other processes are never in a group.
- `integrity` is the process token's integrity level, `low`, `medium`, `high` or `system`, omitted when it cannot be read (for example a protected process).
- `foreground` is the foreground window's handle, `0` when none.
- `focused_control` describes the UI Automation element with keyboard focus (`IUIAutomation::GetFocusedElement`): `window` (the top-level window containing it), `name` (empty for password controls), `control_type` (the UIA control type name), `automation_id` and `class_name` (omitted when empty), `runtime_id` and `rect`. It is `null` when nothing has focus or the lookup did not answer within 2 s. Only `vm_windows` and `vm_observe` ask the agent for it (`list_windows` with `focused: true`, which starts a UI Automation helper process); the window lists the actions and `vm_launch` fetch do not.
- `session` is the agent's `session_state` (see 3.7), `null` when it could not be read.
- `windows` is `[]` when no window is visible.

### 4.3 vm_observe

`vm_observe` is the observation entry point: one call returns a screenshot, the observed window, the focused control and, on request, the window's control tree as indexed text. It stores the observation (see 4.1) and returns a PNG image item followed by the JSON text item.

| Parameter | Default | Meaning |
|---|---|---|
| `vm` | the only running VM | see 2.3 |
| `handle`, `pid` | none | observe this window (`handle` from `vm_windows`, a previous observation, `vm_launch` or an action result; `pid` restricts it, or alone selects the process's only visible window). Without both, the whole screen |
| `screenshot` | `true` | include the PNG |
| `controls` | `false` | include the control tree |
| `max_depth` | 4 | tree depth, 1 to 10 (0 means the default) |
| `max_nodes` | 200 | tree size, 1 to 1000 (0 means the default) |
| `max_size` | 0 | longest side of the output image in pixels; 0 keeps the original size; never upscales |
| `diff_from` | none | a previous `observation_id` of the same window: `controls_diff` replaces `controls` |

```json
{"observation_id": "o-7f3a9c21", "vm": "Win10", "captured_at": "2026-10-10T08:12:03Z",
 "window": {"handle": 197916, "pid": 4120, "process": "acad.exe", "title": "AutoCAD 2015 - Drawing1.dwg", "class": "Afx:400000:...", "rect": {"left": 0, "top": 0, "right": 1920, "bottom": 1040}, "foreground": true, "enabled": true, "group_root": 197916, "integrity": "medium"},
 "screenshot": {"width": 1280, "height": 693, "origin_x": 0, "origin_y": 0, "scale": 0.6666666666666666},
 "focused": {"index": 12, "name": "", "control_type": "Edit", "class_name": "Edit", "rect": {"left": 0, "top": 980, "right": 1920, "bottom": 1040}},
 "selected_text": "",
 "controls": "[0] Window \"AutoCAD 2015 - Drawing1.dwg\" (0,0 1920x1040)\n  [1] Pane \"Ribbon\" (0,52 1920x150)\n    [2] Button \"Line\" id=ID_Line (12,80 48x48)\n  [12] Edit \"\" id=cmdline (0,980 1920x60) focused value=\"LINE\"\n",
 "controls_truncated": false}
```

Behaviour, in order:

1. Parameter checks: `max_depth` outside 0 to 10, `max_nodes` outside 0 to 1000 or a negative `max_size` is `invalid_argument`.
2. The VM is resolved once (see 2.3).
3. The host asks the agent for the window list with the focused control (`list_windows` with `focused: true`). When the agent cannot be reached: with `handle` or `pid` the call is refused with `agent_required`; without them it continues with the screenshot only and the result has `"agent": "offline"` and no `window`, `focused` or `controls`.
4. With `handle` or `pid` the window is selected as in 4.4 step 4; `no_window` and `ambiguous_target` refusals carry `candidates`, the windows the selector could have meant (`[{handle, pid, process, title, class}]`, those of `pid` when given, else all). Without them and with `controls`, the tree is read from the foreground window, which `window` then names and which becomes the observation's tree window (index actions target it; coordinate actions on the whole-screen observation stay unchecked, see 4.1); with no foreground window the tree is not read and `stale_risk` says `no foreground window: control tree not read`.
5. Screenshot (`screenshot: true`): the image is taken from the Hyper-V console via WMI `GetVirtualSystemThumbnailImage`, at the current resolution of the VM's first video head, without the agent, so it also shows the lock screen and UAC prompts. Hyper-V delivers RGB565, which is converted to 8-bit RGBA, so colours are quantised. For a window observation the image is cropped to the window's `rect` intersected with the screen; a window that is entirely off screen or minimized is `invalid_argument` (`restore the window first (vm_key, or act on it by index), or observe without handle`). `max_size` shrinks with nearest-neighbour sampling at pixel centres, preserving aspect ratio subject to integer rounding and a minimum of one pixel per axis; an uncropped, unscaled image keeps the original PNG bytes. A capture failure is `failed` (`call vm_status and make sure the VM is running`).
6. Control tree (`controls: true` or `diff_from` set, and a target window): the agent reads the UI Automation control-view tree rooted at the window (`list_controls`) in a disposable helper process with a 10-second timeout. A timeout or interrupted helper is not an error: the result has no `controls` and `stale_risk` says `target not responding: control tree not read`, and the screenshot is still current. An agent that stops answering at this point is `agent_required` for a window observation and `"agent": "offline"` otherwise. Other agent errors are `failed`.
7. The observation is stored and its ID returned.

Result fields (absent when empty):

- `window`: `handle`, `pid`, `process`, `title`, `class`, `rect`, `foreground`, `enabled`, `group_root`, `integrity` of the observed window, or of the foreground window whose tree a whole-screen observation read.
- `screenshot`: `width` and `height` of the returned image, `origin_x` and `origin_y` (its top-left corner in guest screen pixels) and `scale` (output pixels per screen pixel; 1 when not scaled). See 4.1.
- `focused`: the control with keyboard focus. When the tree was read and contains it, `index` is its index there; otherwise `index` is `-1` and the data comes from `list_windows`, only when the focused control belongs to the observed window's group (or for a whole-screen observation). `control_type` is a name (`Edit`, `Button`, ...).
- `selected_text`: the first selected `TextPattern` range of the window, or of the focused element when it belongs to the same process; at most 512 UTF-16 units; only when the tree was read.
- `controls`: the tree as text, one node per line, indented two spaces per depth: `[index] ControlType "name" id=automation_id (x,y wxh)`, then the state words `disabled`, `offscreen` and `focused` when they apply, then `value="..."` when the control supports `ValuePattern`. `id=` appears only when the control has an automation ID; `x,y` and `wxh` are the control's `rect` in physical screen pixels. The root has index 0 and depth 0. Names, automation IDs, class names and values longer than 512 UTF-16 units are cut, and `controls_truncated` is then `true`.
- `controls_truncated`: `true` when the tree hit `max_depth` or `max_nodes`, a password control's subtree was skipped, or a text was cut. Password controls have their `name` not read, their `value` omitted and their descendants not traversed; this is not a general secret detector.
- `controls_diff` (with `diff_from`): `{"added": [lines], "removed": [old indexes], "changed": [lines]}` and `controls` is omitted. Nodes are matched by `runtime_id`, or by their path of (control type, name) pairs from the root when the agent reports none; a node is `changed` when its index, name, rect, enabled, offscreen, focused or value differs (an inserted node therefore lists every node whose index shifted, so that the caller learns the new indexes); `removed` lists indexes of the earlier observation, sorted. When the earlier observation is unknown, of another VM or window, or has no tree, the full `controls` is returned and `stale_risk` says `diff_from ignored: <reason>`.
- `stale_risk`: the explanations above, joined with `; `.
- `agent`: `offline` when the agent could not be reached and only the screenshot is present.

Tree coverage depends on the application's UI Automation provider; custom-drawn controls (common in CAD programs) may be absent. See Microsoft's [UI Automation tree views](https://learn.microsoft.com/en-us/windows/win32/winauto/uiauto-treeoverview), [UI Automation security boundaries](https://learn.microsoft.com/en-us/windows/win32/winauto/uiauto-securityoverview) and [physical pixels and DPI awareness](https://learn.microsoft.com/en-us/windows/win32/hidpi/high-dpi-desktop-application-development-on-windows); the depth, node, text and timeout limits are HyperHand choices.

### 4.4 Actions: targets, check chain, activation and observe_after

The actions are `vm_click`, `vm_drag`, `vm_scroll`, `vm_set_value`, `vm_invoke`, `vm_type` and `vm_key`. They share these parameters:

- `observation_id`: pixels and indexes refer to this observation (see 4.1). `vm_click`, `vm_drag` and `vm_scroll` without it take raw guest screen pixels.
- `handle`, `pid` (`vm_type`, `vm_key`): the window that must receive the input; a group root is accepted and the input goes to the window of its group that is in the foreground.
- `activate` (default `true`): bring the target window to the foreground first when neither it nor a window of its group is there.
- `observe_after`: `none`, `screenshot`, `controls` or `both`; any other value is `invalid_argument`. The default is `screenshot` for `vm_click`, `vm_drag`, `vm_scroll`, `vm_set_value` and `vm_invoke`, and `none` for `vm_type` and `vm_key`.
- `settle_ms`: the wait before the after-action observation, default 300, 0 to 5000 (`invalid_argument` otherwise).

**Check chain.** A targeted action (one with `observation_id`, `index`, `handle` or `pid`) runs these steps under the input lock (see 4.5), in this order (session, freshness, target, activation, enabled, hit, integrity, execution), and stops at the first refusal with nothing done:

1. **Window list.** The host asks the agent for the current windows, session and integrity facts. An agent that does not answer is `agent_required` (`next` suggests `vm_status`, `vm_install_agent`, or raw screen input without `observation_id` and `handle`).
2. **Session** (`session_unusable`, field `session`): the session is locked (`next` `call vm_unlock`); it is not the VM console session (`call vm_doctor; ...`); or keyboard input goes to the secure desktop (`answer the prompt first with vm_key or vm_click without observation_id (raw screen input), then retry`).
3. **Freshness** (`stale_observation`), see 4.1.
4. **Target resolution.** `index` is resolved to its node (`stale_element`, `invalid_argument`) and the target is the observation's tree window (the observed window, or for a whole-screen observation the foreground window whose tree was read); image pixels are mapped to the screen (`invalid_argument`) and the target is the observed window, or none for a whole-screen observation, which is then clicked without any further check. `handle`/`pid` select a window: `no_window` when none matches (`call vm_windows and use a listed handle`), `ambiguous_target` with `handles` when `pid` alone matches several.
5. **Activation.** When neither the target nor a window of its group is in the foreground and `activate` is `true`, the host asks the agent to focus the target (`focus_window`: a minimized window is restored; `SetForegroundWindow`; if that does not work, the agent attaches to the foreground thread's input, injects a zero-distance mouse move and retries; it injects no key, so ribbon programs such as AutoCAD do not enter key-tip mode), lists the windows again and repeats step 3. If the target's group still does not hold the foreground, `activate_failed` with `foreground` (`{handle, pid, class, process, title}` of the window that does, or `null`) and `next` `act on handle <n> first` (adding `it belongs to <process> and probably is a dialog that blocks the target` when it is another process's). With `activate: false` the strict rule applies: a background target is `activate_failed` with `next` `call again with activate: true, or act on handle <n>`.
6. **Enabled** (`target_disabled`): the target is disabled, as an owner window is while its modal dialog runs. Fields `act_on` and `foreground` name the window to act on: the target's own foreground window (usually the dialog), or another process's foreground window that probably blocks it; when no window explains it, `next` is `call vm_observe (whole screen, controls: true) to find what blocks it`. For keyboard input, the receiving window of the group must also be enabled and not minimized.
7. **Hit** (pointer actions only). For an `index` the control is first re-located by runtime ID (`Locate`, see 4.1; `stale_element` when it vanished) and the point is the centre of its current rectangle. The agent then reports which top-level window is at the screen point (`window_at`). A window outside the target's group is `covered` with `window` (`{handle, class, process}`, described from `window_at` even when `vm_windows` does not list it, such as a shell overlay) and `next` `act on handle <n> first, or close it, ...` (listed) or `dismiss it with vm_key esc or close it, ...` (not listed). A point off screen is `invalid_argument`.
8. **Integrity** (`integrity_mismatch`, fields `integrity`, `agent_integrity`): the window that receives the input (the window hit, the keyboard target, or the control's window for `vm_set_value` and `vm_invoke`) runs at a higher integrity level than the agent, so Windows would drop injected input (UIPI). `next`: `launch the application without admin, or use vm_launch with admin:true`. An unknown level on either side never refuses.
9. **Execution.** UI Automation actions (`Locate` included) have a hard 12-second timeout on the host, and the agent's helper its own 10-second timeout; either is `target_not_responding` (`next` `call vm_observe on the window to see whether it answers; close it with vm_key alt+f4 or vm_exec if it hangs`). The check and the input are two steps, so a window that appears in between can still receive the input.
10. **observe_after.** After a successful action the host waits `settle_ms`, then performs the equivalent of `vm_observe` on the observed window (or the whole screen when the action had no window observation) with `screenshot` and `controls` as the mode says, and embeds the result as `after` with a new `observation_id`; the PNG, when included, is the result's first item. A failed action never observes. When the observation itself fails, the action's result is still returned with `after_error` (the error text) instead of `after`.

**Raw screen input.** `vm_click`, `vm_drag` and `vm_scroll` without `observation_id`, and `vm_key` without `handle` and `pid`, send their input to the Hyper-V console at screen pixels or to whatever has the focus, with no checks at all and without contacting the agent: the sign-in screen, the lock screen and UAC prompts are operated this way. `vm_type` without `handle`, `pid` and `index` asks the agent for the window list: when the agent answers and its session can receive injected input (not locked, not on the secure desktop, console session), the text goes to the foreground window through the agent after steps 6 and 8 on it (`no_window` when nothing is in the foreground); when the agent is unreachable, or its session is locked or on the secure desktop, ASCII text is typed on the Hyper-V keyboard with no checks, and text with a non-ASCII character is refused with `agent_required` (unreachable) or `session_unusable` (`next` `call vm_unlock, or type ASCII text`, field `session`).

**Result.** Every action returns its own fields (below) plus `window`, the window that received the input as `{handle, pid, class, process, title}` (`null` for raw screen input), and `after` (or `after_error`) when `observe_after` is not `none`:

```json
{"ok": true, "window": {"handle": 2818992, "pid": 4120, "class": "HwndWrapper[...]", "process": "acad.exe", "title": ""}, "after": {"observation_id": "o-1c88d0e4", "vm": "Win10", "captured_at": "2026-10-10T08:12:05Z", "window": {...}, "screenshot": {...}, "focused": {...}}}
```

Microsoft documents foreground activation restrictions in [SetForegroundWindow](https://learn.microsoft.com/en-us/windows/win32/api/winuser/nf-winuser-setforegroundwindow), UIPI in [SendInput](https://learn.microsoft.com/en-us/windows/win32/api/winuser/nf-winuser-sendinput), and DPI and invisible-border differences in [GetWindowRect](https://learn.microsoft.com/en-us/windows/win32/api/winuser/nf-winuser-getwindowrect). The group rule, the check order and the codes are HyperHand interface choices.

### 4.5 Input serialisation

All mouse and keyboard operations in the host process are serialised by one lock, so concurrent tool calls never interleave their input events. This includes the keyboard steps of `vm_install_agent`. The MCP process holds a second input lock around the check chain and input of every action, around `vm_clipboard_set`, and around the final check and password typing of an unlock (see 3.7), so no other HyperHand tool sends input in between. The after-action observation runs after the lock is released.

### 4.6 Mouse: vm_click, vm_drag, vm_scroll

Mouse input goes through the VM's synthetic mouse (`Msvm_SyntheticMouse`) with absolute positions. If the device is not found the call fails with `Msvm_SyntheticMouse not found (VM not running?)`.

- `vm_click` targets `observation_id` with `x`, `y` (image pixels) or with `index` (the centre of the control's current rect, re-located by runtime ID); or `x`, `y` alone as raw screen pixels. `index` without `observation_id` is `invalid_argument`.
  - `button`: `left` (default), `right` or `middle`; anything else is `invalid_argument`.
  - `count`: 1 (default) to 3 clicks; anything else is `invalid_argument`.
  - `modifiers`: any of `ctrl`, `shift`, `alt`, without repeats (`invalid_argument` otherwise). They are pressed on the synthetic keyboard (`Msvm_Keyboard`) before the mouse moves and released in reverse order afterwards, even if the click fails.
  - The mouse moves to the point, waits 100 ms and clicks `count` times.
  - Result: `{"ok": true, "window": {...}, "after": {...}}`.
- `vm_drag` takes `from: {x, y}` and `to: {x, y}` (both image pixels of `observation_id`, or raw screen pixels without it) and `modifiers`. The check chain runs for the `from` point. It moves to `from`, waits 100 ms, presses the left button, moves to `to` in 8 equal steps 50 ms apart, waits 100 ms and releases the button. The button is released even if a move fails. Result: `{"ok": true, "window": {...}, "after": {...}}`.
- `vm_scroll` takes `x`, `y` (image pixels of `observation_id`, or raw screen pixels) and `delta_y` and/or `delta_x` in wheel notches (120 units each); both zero is `invalid_argument`. A positive `delta_y` scrolls up, a negative one down, through the synthetic mouse after a move and 100 ms wait. A positive `delta_x` scrolls right, a negative one left; the Hyper-V mouse has no horizontal wheel, so the agent sends it with `SendInput` at that screen point (`hscroll`), and `delta_x` is `agent_required` without the agent. Result: `{"ok": true, "window": {...}, "after": {...}}`.

### 4.7 Control actions: vm_set_value, vm_invoke

Both act on a control by its `index` in the tree of an observation taken with `controls: true` (`observation_id` required); the target window is the observation's tree window (for a whole-screen observation, the foreground window whose tree was read). The agent re-finds the element in that window by its runtime ID and performs one UI Automation pattern action in its helper process; the host bounds the call to 12 s.

- `vm_set_value` sets `value` through `ValuePattern.SetValue`.
- `vm_invoke` performs `action`: `Invoke` (`InvokePattern`), `Toggle` (`TogglePattern`), `Expand` or `Collapse` (`ExpandCollapsePattern`), `Select` (`SelectionItemPattern`) or `ScrollIntoView` (`ScrollItemPattern`), case-insensitive. Any other action, including `SetValue`, is `invalid_argument`.
- Refusals: an observation without a tree is `invalid_argument`; an element that no longer exists is `stale_element` (`call vm_observe with controls: true and use an index from its tree`); a control without the pattern is `unsupported_pattern` with `supported`, the control's pattern names as the agent lists them (`Invoke`, `Toggle`, `Expand`, `Collapse`, `Select`, `Value`, `ScrollItem`), and `next` `use vm_invoke with one of supported, or vm_click with this index`; a hung window is `target_not_responding`.
- Result: `{"ok": true, "verified": true, "value": "abc", "window": {...}, "after": {...}}`. After the action the agent re-reads the control's `ValuePattern` value when it has one and is not a password control: `value` is the read-back (`null` when unreadable or unsupported); `verified` is whether it equals the requested value for `vm_set_value`, `null` for `vm_invoke` and `null` when the read-back was cut at 512 UTF-16 units and cannot be compared. A `false` `verified` is reported, not retried.

### 4.8 Keyboard: vm_key

`vm_key` presses a key or a `+`-separated combination through the VM's synthetic keyboard (`Msvm_Keyboard`).

- Pass exactly one of `keys` or `sequence`, an ordered array of up to 256 combinations such as `["ctrl+a", "backspace"]`; both or neither, more than 256, or an unknown name is `invalid_argument` before any key is sent.
- `handle` (optionally restricted by `pid`) or `pid` pins the window that must receive the keys (see 4.4): the keys go to the window of its group that is in the foreground. The window list is fetched again before every further combination and the sequence stops as soon as the pinned group loses the foreground, a dialog disables it, or a combination fails: after at least one combination was sent this is `partial_input` with `applied` and `total` (`call vm_observe to see the state before sending the rest`); a failure before the first one keeps its own code (`target_disabled`, `activate_failed`, `failed`, ...). Earlier input is not undone. Without a selector the keys go wherever the focus is, with no checks (see 4.4).
- Result: `{"ok": true, "combinations": 2, "window": {...}, "after": ...}`; `window` is `null` for untargeted input.
- Names are case-insensitive and surrounding spaces are ignored. Supported names:
  - Modifiers: `ctrl` / `control` / `control_l` / `control_r`, `shift` / `shift_l` / `shift_r`, `alt` / `alt_l` / `alt_r`, `win` / `super_l`.
  - Keys: `enter` / `return`, `esc` / `escape`, `tab`, `space`, `backspace`, `delete` / `del`, `insert`, `home`, `end`, `pageup` / `prior`, `pagedown` / `next`, `up`, `down`, `left`, `right`, `printscreen`, `scrolllock`, `pause`, `apps`, `capslock`, `numlock`, `f1` to `f20`.
  - Numeric keypad: `num0` to `num9` / `kp_0` to `kp_9`, `numenter` / `kp_enter`, `numdot` / `kp_decimal`, `numplus` / `kp_add`, `numminus` / `kp_subtract`, `nummul` / `kp_multiply`, `numdiv` / `kp_divide`. The Hyper-V keyboard takes a virtual-key code without an extended flag, so `numenter` is the same key as `enter`, and the left/right modifier aliases are the plain modifier.
  - Single letters `a`-`z` and digits `0`-`9`.
  - Punctuation keys `;` `=` / `equal` / `plus` (the `+`/`=` key; `+` itself is the separator), `,` / `comma`, `-` / `minus`, `.` / `period`, `/` / `slash`, `` ` ``, `[`, `\`, `]`, `'`.
- A single key is sent as one key press. A combination presses the keys in order and releases them in reverse order; keys already pressed are released even if a later press fails.

### 4.9 Text: vm_type

`vm_type` enters `text` into a window. It has one mode and never touches the guest clipboard.

- Target: `handle` (optionally restricted by `pid`) or `pid` selects the window (a group root is accepted; the text goes to the window of its group in the foreground); `observation_id` with `index` first clicks that control (re-located by runtime ID, with the full check chain, in the observation's tree window) to give it the focus; with none of them the text goes to the foreground window (see 4.4, raw screen input). `index` without `observation_id` (or vice versa), or `index` together with `handle`/`pid`, and an empty `text` are `invalid_argument`.
- With the agent, the text is injected into its session as Unicode key events (`type_keys`: UTF-16 `KEYEVENTF_UNICODE` / `VK_PACKET` through `SendInput`, including surrogate pairs). CRLF becomes one Enter; a lone CR or LF becomes Enter; Tab uses the Tab key. Enter and Tab can submit a command or move focus. Before every character the agent checks that its session is unlocked and not on the secure desktop and that the target is still the visible, enabled, non-minimized foreground window with the requested PID, and it refuses when a modifier or Windows key, or the key the character needs, is already held. The text must be valid UTF-8 of at most 16384 bytes.
- Without a target and without the agent (it does not answer), or when the agent's session is locked or on the secure desktop, ASCII text is typed on the Hyper-V keyboard (`TypeText`) into whatever has the focus; an IME in Chinese mode may swallow it. Text with a non-ASCII character is then `agent_required` or `session_unusable`.
- Result: `{"applied_chars": 8, "total_chars": 8, "window": {...}, "after": ...}`. With `index` the result also has `verified` and `value`: after typing, the control is read back (`Locate`); `value` is its current value and `verified` says whether it contains the typed text (without trailing newlines and tabs); both are `null` when the control has no readable value. A failure after some events were injected is `partial_input` with `applied_chars` and `total_chars` (`call vm_observe with controls: true to see what arrived; do not retype blindly`); a failure before any event is `failed` with `applied_chars: 0`. Earlier input is not undone.
- Windows UIPI blocks injection into higher-integrity applications (refused beforehand as `integrity_mismatch`, see 4.4). Successful injection does not prove the application consumed the text, and a foreground window does not establish which child control has the focus; without `index` nothing is read back. Observe the result (`observe_after`, or `vm_observe` with `controls: true`).

See Microsoft's [SendInput restrictions](https://learn.microsoft.com/en-us/windows/win32/api/winuser/nf-winuser-sendinput) and [KEYBDINPUT Unicode semantics](https://learn.microsoft.com/en-us/windows/win32/api/winuser/ns-winuser-keybdinput).

## 5. Commands

### 5.1 vm_exec

`vm_exec` runs `command` in the guest as the logged-on user (the agent's user), waits for it to exit and returns:

```json
{"exit_code": 0, "stdout": "WIN10\r\n", "stderr": "", "timed_out": false}
```

A non-zero exit code is not a tool error. An empty `command` or an unknown `shell` is `invalid_argument`; an agent that does not answer is `agent_required`; errors from the agent (start failure, cancellation) are `failed`. `vm_exec` is not for starting GUI programs, whose process would belong to the request: use `vm_launch` (see 7.2).

### 5.2 Shells

- `shell` = `powershell` (default, also for an empty value): `powershell.exe -NoProfile -NonInteractive -ExecutionPolicy Bypass -Command` with `[Console]::OutputEncoding` set to UTF-8 before the command.
- `shell` = `cmd`: the command line is exactly `cmd.exe /d /s /c "<command>"` (AutoRun disabled, outer quotes stripped by `/s`).
- Any other value is `invalid_argument` (`shell: expected powershell or cmd, got "<value>"`).
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

`vm_push` and `vm_pull` need the agent (`agent_required` otherwise). Missing `host_path` or `guest_path` is `invalid_argument`. File contents are streamed in 1 MB buffers on both sides and are not held in memory.

### 6.1 vm_push

`vm_push` copies `host_path` (a file, or a directory recursively) into the guest at `guest_path`.

- Single file:
  - If `guest_path` ends in `\` or `/`, the file is written into that directory under its own name.
  - Otherwise `guest_path` is the destination file path.
- Directory: every file below `host_path` is written to `guest_path\<relative path>`. Only files are transferred; empty directories are not created. The host walk does not descend into symbolic links to directories.
- Parent directories in the guest are created as needed.
- Result: `{"copied": 12, "skipped": 30, "bytes": 48213760}` (files copied, unchanged files skipped, bytes sent).
- On failure the error's reason names the guest path and the field `copied` says how many files were copied before it; those files remain.

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
- Result: `{"files": 3, "bytes": 10240, "host_path": "C:\\temp\\reports"}`.
- On failure the error's reason names the guest path and the field `files` says how many files were copied before it.

### 6.4 Temporary .hhpart files

Both directions write each file to a unique `.hyperhand-*.hhpart` temporary file in the destination directory and rename it over the destination only after the whole file has been received, replacing an existing file. On any error only that transfer's temporary file is deleted and the destination is left unchanged. In the guest, a short or failed transfer is never renamed into place.

## 7. Clipboard, Launching, Waiting and Turn End

These tools need the agent, except `vm_end_turn`.

### 7.1 vm_clipboard_get and vm_clipboard_set

- `vm_clipboard_get` returns the guest clipboard as Unicode text: `{"text": "..."}`, an empty string when the clipboard holds no text.
- `vm_clipboard_set` empties the clipboard and puts `text` on it as Unicode text; it holds the input lock (see 4.5) meanwhile. Result: `{"ok": true}`.
- Opening the clipboard is retried 20 times, 50 ms apart; if it stays locked by another program the call fails with `clipboard busy`.
- No other tool uses the clipboard: `vm_type` injects key events.

### 7.2 vm_launch

`vm_launch` starts a program in the guest as a detached process and waits for its first visible top-level window.

- Parameters: `path` (required; `invalid_argument` when empty), `args` (array), `cwd`, `wait_window_ms` (default 60000; zero or negative also means 60 s) and `admin`.
- The agent starts `path` with `args` in `cwd` in a new process group, outside the job object `vm_exec` uses and without captured output, so it outlives the request (`launch`). With `admin: true` it is started elevated through the same worker as `vm_exec` (see 5.5).
- The host then lists the guest's windows every 300 ms until a visible top-level window of that PID appears: the first one with a title is preferred, else the first one. A splash screen or dialog of the process counts.
- Result: `{"pid": 4120, "handle": 197916, "title": "AutoCAD 2015 - Drawing1.dwg", "class": "Afx:400000:...", "elapsed_ms": 8120}`. Pass `handle` to `vm_observe` and the actions.
- No window within `wait_window_ms` is `no_window` with the field `pid` and `next` `call vm_windows later, or vm_observe without handle to see a splash screen or dialog`; the process keeps running.
- The VM is resolved once, so the polling cannot move to another VM.

### 7.3 vm_wait

`vm_wait` polls the guest every 300 ms until a condition holds or `timeout_ms` (default 60000) expires. Result: `{"satisfied": true, "elapsed_ms": 1200}`; a timeout returns `"satisfied": false` and is not an error.

- `kind` = `process_running`: a process named `name` exists.
- `kind` = `process_exit`: no process named `name` exists (true immediately if none was running).
- Process names are compared case-insensitively on the executable name; a directory part is ignored and `.exe` is added if missing, so `notepad`, `Notepad.exe` and `C:\Windows\notepad.exe` are equivalent.
- `kind` = `file_exists`: `path` exists (a file or a directory).
- An empty `name` or `path` for its kind, or any other `kind`, is `invalid_argument`. There are no window conditions: observe the window with `vm_observe`, or use an action's `after` observation.
- `vm_end_turn` cancels pending waits: the cancelled call returns `failed` with reason `the wait was cancelled by vm_end_turn` and `elapsed_ms`. Cancellation by the MCP client is an error too. If the host disconnects, the wait stops (see 1.4).

### 7.4 vm_end_turn

`vm_end_turn` ends an AI turn. It does not need the agent and changes nothing in the guest.

- It cancels this server's pending `vm_wait` calls and deletes the `temp` checkpoints this run created (see 3.4). `vm` limits the checkpoints to one VM; the waits are always all cancelled. `keep` and manual checkpoints, running programs and the VM's power state are not touched.
- Result: `{"cancelled_waits": 1, "deleted_checkpoints": ["run-20261010-0812-7f3a-temp-step3"], "errors": []}`. A checkpoint that could not be deleted is listed in `errors` as `<vm>/<name>: <error>` and kept for the next `vm_end_turn`.
- It is meant for a client's Stop hook and is safe to call at any time. Example hook configurations for Claude Code (`settings.json`, `Stop`) and a Codex plugin (`plugin.json`, `Stop`, `Interrupt`, `SubagentStop`) are in `docs/hooks/`.

## 8. Guest Agent Installation, Update and Diagnostics

### 8.1 vm_install_agent

`vm_install_agent` installs the agent without a guest password. It needs a user logged on to the guest desktop and the guest IME in English mode.

1. The ordinary tray reads `hyperhand-agent.exe` next to its own executable, normally under `%ProgramFiles%\HyperHand`, and streams it to the service.
2. The service stages the contents in its working directory, enables the VM's Guest Service Interface if it is disabled (then waits 3 s) and copies the file with `Copy-VMFile` to `C:\Users\Public\HyperHand\hyperhand-agent.exe`, creating the path and overwriting. The service does not open a caller-supplied host source path.
3. It presses `win+r`, waits 1.5 s, types `C:\Users\Public\HyperHand\hyperhand-agent.exe install` on the synthetic keyboard and presses `enter`.
4. It pings the agent (see 8.4).

Because step 3 types blindly, a failure there is visible only on screen; the tool description advises checking with `vm_observe`.

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
- It deletes `%LOCALAPPDATA%\HyperHand` and `C:\Users\Public\HyperHand`. The file of a running program cannot be deleted but can be renamed on its volume, so before deleting the folder it is running from it moves its own executable to `%TEMP%\hyperhand-agent-uninstalled-<pid>.exe`, where it remains (or to the parent of that folder if `%TEMP%` is on another volume), and leaves the folder as its working directory. No script or helper process is started.
- It shows a message box listing what was removed.

### 8.3 Single instance and startup

- The agent uses the mutex `HyperHandAgent`; a second instance exits immediately.
- At startup it marks itself DPI-aware and deletes a leftover `hyperhand-agent.exe.old` next to itself, retrying for up to 10 s while the previous version is still exiting.

### 8.4 Agent readiness check

`vm_install_agent` and `vm_update_agent` ping the agent every 2 s for up to 30 s, each ping limited to 5 s.

- Success returns `{"agent": {"version": "0.3.0", "hostname": "WIN10", "user": "WIN10\\tester", "protocol": 2}}`. An agent that answers with an older protocol is refused with `agent_outdated` (see 8.6).
- Otherwise the call is refused with `agent_required`, reason `the agent did not answer within 30 s: <last error>` and `next` `look at the screen with vm_observe, then call vm_install_agent again`.

### 8.5 vm_update_agent

`vm_update_agent` replaces the running agent with the installed `hyperhand-agent.exe` next to the host executable. The agent must already be running (`agent_required` otherwise).

1. The host sends the whole executable as the `update_agent` payload.
2. The agent writes `<exe>.new`, deletes any `<exe>.old`, renames the running `<exe>` to `<exe>.old` and `<exe>.new` to `<exe>`. If the last rename fails, the old file is renamed back and the update fails. An empty payload fails with `empty payload`.
3. After a successful update the agent sends its response, releases its mutex, starts the new executable with the same arguments and exits. It restarts even if the response could not be sent.
4. The host closes its connection, waits 2 s and pings the new agent (see 8.4).

The update replaces the file the agent is running from; the HKCU Run entry is unchanged.

### 8.6 Protocol version

The guest protocol has a generation number, `protocol` 2 in this version, reported by the agent's `ping`. There is no compatibility path for older agents:

- Every new connection to an agent is checked once: before the first op other than `ping` or `update_agent`, the host pings the agent and refuses an older `protocol` with `agent_outdated` (fields `agent_protocol`, `host_protocol`; `next` `call vm_update_agent`) for that op and every later one on the connection. `vm_status` (which only pings) and `vm_update_agent` therefore still work on an old agent, so that it can be reported and replaced; `vm_start`, `vm_install_agent` and `vm_update_agent` additionally check the protocol of the agent that answers their readiness ping.
- An agent that answers `unknown op` to a request is reported as `agent_outdated` with the same `next`.
- `vm_status` and `vm_doctor` report the protocol without refusing.

### 8.7 vm_doctor

`vm_doctor` diagnoses the host and the selected VM. It is read-only and changes nothing. Result: `{"checks": [{"check": "host.service", "status": "ok", "detail": "HyperHandService is running"}, ...]}`; `status` is `ok`, `warn` or `fail`, and every `warn` or `fail` has a `suggestion` naming a command or tool call.

| Check | Looks at | fail / warn |
|---|---|---|
| `host.service` | `HyperHandService` in the service control manager | fail: not installed or not running (`run hyperhand.exe install (elevated)`, `Start-Service HyperHandService`); warn: access denied when querying |
| `host.mcp` | this process answered the call; `detail` names the URL it serves | always ok |
| `host.pipe` | dialling `\\.\pipe\HyperHandService` (2 s) | fail: not reachable |
| `host.hvsocket` | the registry registration of the Hyper-V socket service ID (see 1.2) | fail: not registered |
| `host.enhanced_session` | `EnhancedMode` under `HKLM\SOFTWARE\Microsoft\Windows NT\CurrentVersion\Virtualization` | warn: the host allows enhanced session mode |
| `vm.power` | the selected VM | fail: the VM cannot be resolved (see 2.3) or is not running; the guest checks are then skipped |
| `guest.agent` | a 5 s ping: version, protocol, hostname, user | fail: no answer (`call vm_start ...; ... vm_install_agent`), or protocol older than the host's (`call vm_update_agent`) |
| `guest.session` | `session_state` | fail: not available, or not the console session; warn: a UAC prompt is open, or the session is locked |
| `guest.integrity` | the agent's integrity level from `list_windows` | warn: the window list or the level is unavailable |
## 9. Host Tray

### 9.1 Running

`hyperhand.exe [-port <n>]` starts the tray and the MCP server. The port is `-port` if given, else the port saved in the settings window, else 8770.

- The tray starts without requesting elevation. Hyper-V operations require the separately installed and running `HyperHandService`.
- A second instance exits immediately (mutex `Local\HyperHandTray`).
- It listens on `127.0.0.1:<port>`; protected machine configuration is performed only by the installer.
- The tray icon's tooltip is `HyperHand`, or says that the MCP server is not running or the background service is unavailable. A left click, or **Settings...** in its right-click menu, opens the settings window; the menu also has **Restart** and **Quit**.
- The icon is added at once, without waiting for the taskbar. If the taskbar does not accept it yet (for example at logon, while Explorer is still starting), the tray keeps running and retries every 5 seconds and whenever the taskbar is created; it adds the icon again whenever Explorer recreates the taskbar.
- The settings window shows the MCP endpoint (with **Copy**), the port, the server status, the background service status, the version and **Open log folder**. **Change port** and **Apply and restart** save a new port (1024 to 65535, which must be free) in `%LOCALAPPDATA%\HyperHand\settings.json` and restart the tray; this is unavailable when `-port` was given. Its **Virtual machines** list shows the Hyper-V VMs with their state (running, off, saved, paused), whether an unlock password is stored and whether the console opens when started, refreshed every 5 seconds. For the selected VM: **Open console**, which brings an open Virtual Machine Connection window for that VM to the front (a visible window of `<system directory>\vmconnect.exe` whose title, such as `Win10 on localhost - Virtual Machine Connection` or a localized `localhost 上的 Win10 - 虚拟机连接`, names exactly that VM; a title in another layout is not matched, and a new console is opened) or else runs the `HyperHand Console` task with the VM name (see 9.2), after checking that the name is an existing VM's exact name without quotes, line breaks or a trailing backslash; **Open console when started**, a per-VM check box saved in `%LOCALAPPDATA%\HyperHand\settings.json` (see 3.7); the window warns when the host allows enhanced session mode (`EnhancedMode` under `HKLM\SOFTWARE\Microsoft\Windows NT\CurrentVersion\Virtualization`); **Set unlock password...**, which asks in the Windows credential dialog for the password or PIN the guest lock screen asks for and stores it in Windows Credential Manager for the current user (non-ASCII passwords are refused), and **Clear unlock password**. The user name in the dialog is only a note. Restart replaces only this ordinary tray/MCP process, without UAC, preserving the selected port. Active MCP connections and requests are interrupted; the service, guest agent and VMs are not restarted.

### 9.2 install

`hyperhand.exe install` installs or updates the host service and ordinary tray. Installation and uninstallation run in the elevated `hyperhand.exe` itself, through the Windows service control manager, Task Scheduler, local group and security APIs; no script is run.

- If not elevated, it relaunches itself elevated with `install`.
- It installs `hyperhand.exe` and `hyperhand-agent.exe` under `%ProgramFiles%\HyperHand`.
- It records the installing user's SID in the protected `%ProgramData%\HyperHand\config.json` and provides a service-writable `service-data` directory beneath it.
- It stops an installed service and waits for its process to exit before replacing the executables, and ends installed tray processes.
- It configures automatic Windows service `HyperHandService` under `NT SERVICE\HyperHandService` and adds that service account to Hyper-V Administrators. It does not add the human user or use LocalSystem.
- It creates or replaces the `HyperHand` logon task for the installing user with least privilege and the installed executable. Reinstalling migrates the older highest-privilege task.
- It creates or replaces the `HyperHand Console` task for the installing user: no trigger, highest privileges, one action `<system directory>\vmconnect.exe localhost "$(Arg0)"` (the system directory from `GetSystemDirectory`, not an environment variable). An existing task of that name must belong to the installing user and run `vmconnect.exe`, or installation stops.
- It registers the fixed guest socket service (see 1.2) and starts the host components. Repeating `install` deploys an update; normal tray restart does not update binaries.
- On error it logs the error and shows it in a message box.

`hyperhand.exe uninstall` reverses the install.

- If not elevated, it relaunches itself elevated with `uninstall` (one UAC prompt).
- It stops the installed tray and service and removes `HyperHandService`, its Hyper-V Administrators membership, the `HyperHand` logon task, the `HyperHand Console` task and the fixed guest socket registration.
- Run from another copy, it removes the installed host and agent executables itself, only if their hashes still match. Run as the installed `hyperhand.exe`, which cannot delete its own file, it copies itself to the administrators-only `%ProgramData%\HyperHand\uninstall-cleanup.exe`, which waits for it to exit and then does the same. That copy cannot delete itself either: it and the then empty data directory are deleted at the next restart (`MoveFileEx` with `MOVEFILE_DELAY_UNTIL_REBOOT`); a later `install` deletes a leftover copy that is not running. A cleanup failure is written to `%ProgramData%\HyperHand\uninstall-error.log`. Only empty directories are removed.
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
- Payloads carry file contents and the agent executable; they are empty otherwise. File payloads are streamed rather than buffered.
- If a header is not valid JSON, the receiver skips its payload so the stream stays in sync.

### 10.2 Operations

| Op | Args | Result / payload |
|---|---|---|
| `ping` | none | `{version, protocol, hostname, user}`; `protocol` is the generation the agent speaks (see 8.6) |
| `exec` | `{command, shell, cwd, timeout_ms, admin}` | `{exit_code, stdout, stderr, timed_out}` |
| `write_file` | `{path}` + payload (file contents) | none |
| `read_file` | `{path}` | payload (file contents) |
| `list_dir` | `{path}` | `{entries: [{name, is_dir}]}`, links skipped |
| `hash_files` | `{paths}` | `{hashes}`, lowercase hex SHA-256 in order, `""` when unavailable |
| `type_keys` | `{text, handle, pid}` | `{events}`, the number of injected input events |
| `list_controls` | `{handle, pid, max_depth, max_nodes}` | `{nodes: [{index, parent, depth, name, control_type, automation_id, class_name, pid, enabled, offscreen, rect, runtime_id, patterns, value, has_value, focused}], truncated, truncation, focused, selected_text}`, a bounded UIA control-view snapshot; `control_type` is the numeric UIA ID (the host renders names), `focused` the index of the focused node or -1 |
| `control_action` | `{handle, pid, runtime_id, action, value}` | `{rect, value, has_value, verified}`: one UIA pattern action (`SetValue`, `Invoke`, `Toggle`, `Expand`, `Collapse`, `Select`, `ScrollIntoView`) on the element with that runtime ID, or `Locate`, which performs nothing; then the element's current `rect` and its re-read value (`verified` only for `SetValue`, omitted when the read-back was cut); errors `element not found` and `unsupported pattern: <action>; supported: <list>` |
| `launch` | `{path, args, cwd, admin}` | `{pid}`; the process is started detached, outside the `exec` job object |
| `hscroll` | `{x, y, delta}` | none; horizontal wheel notches at a screen point through `SendInput` |
| `clipboard_get` | none | `{text}` |
| `clipboard_set` | `{text}` | none |
| `focus_window` | `{title, handle}` | `{text, handle}`, the focused window's title and handle; the host passes a handle (used by the actions' activation step) |
| `window_at` | `{x, y}` | `{handle, class, pid, process}`, the top-level window a click at that screen point reaches; handle 0 off screen |
| `list_windows` | `{focused}` (optional) | `{windows: [{handle, title, class, pid, process, rect, enabled, foreground, minimized, owner, modal, group_root, integrity}], foreground, focused, session, agent_integrity}`; `focused` is the UIA focused control (see 4.2), read through a helper process only when the args ask for it (`vm_windows`, `vm_observe`), `session` the `session_state` result, `agent_integrity` the agent's own integrity level |
| `wait` | `{kind, name, path, timeout_ms}` | `{satisfied}` |
| `session_state` | none | `{locked, console, secure_desktop, logonui, consent}` for the agent's session (see 3.7) |
| `update_agent` | payload (new executable) | none; the agent then restarts (see 8.5) |

The former `screenshot` op is gone: all screenshots are taken on the host.

### 10.3 Error behaviour

- An unknown op returns the error `unknown op "<op>"`.
- A handler that panics returns the error `<op> panicked: <value>`; the agent keeps running.
- For `write_file`, the agent always consumes the full payload, so a failed write (for example an invalid path) is reported as an error without breaking the connection. Only a failed read from the socket ends the connection.
