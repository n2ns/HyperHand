# v0.2.0 functional acceptance

Validated on 2026-10-07 (host local date), using the official [v0.2.0 release](https://github.com/n2ns/hyper-hand/releases/tag/v0.2.0), commit `5c34f8bd3d6e09c5a2aef26d0d75e2a675763621`.

## Environment and evidence

- Windows host; installed host at `C:\Program Files\HyperHand\hyperhand.exe`; ordinary-user tray and MCP, `HyperHandService` under its virtual service account.
- Windows 10 guest `Win10`, basic console session, 1920x1080; AutoCAD 2015.
- Host and guest agent both report version 0.2.0. The installed host and agent were checked against the official ZIP.

| Artifact | SHA-256 |
|---|---|
| Release ZIP | `08b052cbd8878a6f9d08f967ae24891f535083ba01d6418bf8481fe5bf04e3b5` |
| Host EXE | `6a05800e7467ec5313659fdd34376eddecb848cdb06577c28f9b962b9ee49a9c` |
| Agent EXE | `c066c49ebaa62c79c828893f1bdec0e937cc6ee4979a1ad18d63801ffaff1b1c` |

Local JSON, request and screenshot evidence is retained under ignored `build/automation-validation-20261007/` and `build/acceptance-20261007/`. These machine-specific artifacts are not part of the release. This report records observations, not a guarantee for every Windows, CAD or session configuration.

## Passed on the official binaries

| Area | Procedure and observed result |
|---|---|
| Unicode input | WinForms fixture received Chinese, emoji, CRLF and Tab exactly in `mode: keys`; clipboard marker remained unchanged. |
| AutoCAD command line | Created an empty test drawing, clicked the floating command line and selected its current foreground HWND/PID. Unicode keys set `USERS1` to `管道验收ABC123`; AutoCAD's COM API read back the exact value. An AutoLISP command wrote a separate marker file, whose content was verified through `vm_exec`. |
| CAD target checks and KeyTips | The main-frame selector correctly refused input after the floating command line became foreground. Observed ribbon KeyTips after Alt; Esc sequence, command-line click and a fresh foreground selector restored input, confirmed by recreating the marker file. Only the test drawing was closed; no existing drawing was edited. This does not establish coverage of the original `SetForegroundWindow` failure/fallback branch. |
| File-dialog input and UIA | Chinese filename input in AutoCAD's Select File dialog was read back exactly. Bounded read-only UIA trees were returned for CAD and the test fixture; depth truncation was observed. The CAD edit did not expose ValuePattern; general CAD value/action support is not claimed. |
| Screenshot coordinates | A 200x40 crop scaled to 100x20 returned the expected transform; mapping back to console coordinates clicked the fixture button and produced its marker. Host and agent metadata were checked in the basic console session. |
| Busy status | During a six-second guest command, `vm_status` returned busy in approximately 387 ms including helper startup; the original command completed successfully. |
| Shutdown, start and automatic console | Confirmed no open CAD documents and exited CAD, called `vm_shutdown`, observed Off, then `vm_start`. After approximately 24.924 seconds the agent answered and the desktop was unlocked. VMConnect count went from zero to one. |
| Unlock and console reuse | Locked the signed-in guest, observed `session: locked`, then called `vm_start`. It reported that the session had been unlocked; a subsequent status confirmed unlocked. The existing VMConnect PID remained unchanged, with no extra console process. No password was exported or logged. |
| No-agent startup failure | Created a separate 128MB VM without an OS, disk or agent. `vm_start` returned an explicit Running-but-desktop-not-usable error after approximately 96.875 seconds. A subsequent status query worked and reported Running/no response. The fixture VM and its directory were removed. This validates the unavailable-agent path, not the actual Windows signed-out screen. |
| Running checkpoint and data disk | Attached a dedicated 256MB VHDX and created a Standard checkpoint while Win10 was running. Changed system-disk and attached-disk markers from A to B and terminated a memory-marker process. Both `start: false` and default restore recovered A on both disks and resumed the same process PID, start time and in-memory token. The agent reconnected and commands succeeded after restoration. |
| Host uninstall and reinstall | Invoked uninstall from the staged official EXE. Verified removal of the service, both tasks, service-account group membership, socket registration, installed EXEs and installed processes, while the settings hash remained unchanged. Reinstalled the same official package under the original owner. Service account/path/start mode, both task XMLs, group membership, socket registration, installed EXE hashes, directory ACLs and settings hash matched the baseline. A non-elevated client then successfully called VM list, status, checkpoints and guest commands; host and agent still reported 0.2.0. |

## Final restored state

The dedicated checkpoint was deleted through Hyper-V and its disk chain merged. The test disk was detached, the original checkpoint settings restored, and the original checkpoint ID and system-disk attachment retained. The memory helper was stopped, the no-agent VM removed, and AutoCAD was reopened. Final ordinary-user checks found only Win10, Running and unlocked; the test volume was absent.

The detached `C:\ProgramData\HyperHand-Acceptance-20261007-Disk\acceptance-data.vhdx` is retained as evidence (256MiB virtual capacity, 132MiB file size at cleanup). It is not attached to a VM. Small guest marker/script files remain under `C:\Users\Public\HyperHand-acceptance-20261007` and `C:\Users\Public\HyperHand-acceptance-command.txt`.

Cleanup required correcting a test-script UTC/local-date comparison and rerunning in a fresh PowerShell process after Hyper-V returned a cached path to an already merged AVHDX. These were fixture issues; no AVHDX was manually removed and no product runtime fix was needed. Final evidence is `running-checkpoint/cleanup.json`, `host-lifecycle/uninstall-verification.json`, `host-lifecycle/lifecycle.json` and `final-result.json` under the local acceptance directory.

`start: false` left this running Standard checkpoint in **Running**, rather than Saved. The flag suppresses HyperHand's additional Start call; it does not force a power state. The old unconditional Standard-to-Saved statement was corrected in the documentation and source comments, with no runtime change.

The no-agent test used helper `-TimeoutSec 150`. The 90-second setting is an agent-polling window; individual ping waits and loop intervals can make the observed call longer. Successful startup can also include unlock and readiness checks.

## Remaining coverage

- Host tray Restart: actual menu click is unverified. Windows Computer Use returned `native pipe is unavailable` / OS error 2, including after resetting its runtime. Process-level restart evidence is not a substitute for menu acceptance.
- The host lifecycle test used an external staged EXE. The installed EXE's self-removal/deferred-cleanup branch and guest uninstall were not exercised. The stored unlock credential remained present after reinstall; its contents were not inspected.
- Enhanced-session/RDP switching, multiple monitors, non-default DPI and guest-agent screenshot origin across those configurations remain unverified.
- A real Windows cold boot with no signed-in user, Production checkpoint rollback, an application blocking shutdown and the original focus-failure fallback path were not exercised in this round.
- Release CI previously passed race tests, vet and builds. This follow-up changes documentation and comments only; no new binary was built for acceptance.

## Official references used

- [Microsoft: checkpoints](https://learn.microsoft.com/en-us/windows-server/virtualization/hyper-v/checkpoints) distinguishes Standard memory snapshots from Production snapshots and requires checkpoint deletion through Hyper-V so differencing disks are merged. Never delete AVHDX files directly.
- [Microsoft: enhanced session mode](https://learn.microsoft.com/en-us/windows-server/virtualization/hyper-v/enhanced-session-mode) describes its RDP-based session and device sharing; basic-console observations do not establish enhanced-session coverage.
- [Microsoft: SendInput](https://learn.microsoft.com/en-us/windows/win32/api/winuser/nf-winuser-sendinput) documents UIPI and current keyboard-state constraints. Success in these fixtures does not imply support for higher-integrity windows or every custom CAD control.
