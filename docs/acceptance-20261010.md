# Win10 functional acceptance, 2026-10-10

Real Hyper-V VM `Win10`, installed MCP endpoint `127.0.0.1:8770/mcp`, basic console, 1920 x 1080. The initial run exercised all 32 tools on `4498588`; the follow-up reproduced and repaired five product defects, expanded branch coverage, and reran affected behavior on installed binaries. Final product code: `ecc7441d83a58d3f32070a038a0ee1f9665f586b`. Final installed host and agent version: `ecc7441-acceptance`.

Acceptance passed for the exercised scenarios in this environment. This is not a claim that every environment or failure combination was tested; remaining limits are listed below. Tool responses were checked against fixture state, process identity, file contents or checkpoint state wherever applicable.

## Repairs, each committed separately

| Commit | Reproduced problem | Repair and verification |
|---|---|---|
| `98712c6` | Window enumeration crashed under race/checkptr while reading a token label from a byte buffer. | Aligned storage and preserved SID pointer provenance. Repeated focused tests and full agent race suite passed; real guest window/integrity queries worked. |
| `ccad97c` | Installation returned the old agent's version before the replacement was ready. | A per-installation random ID must match the new agent's ping. Real old-version replacement and same-version reinstallation returned distinct IDs, replaced PIDs and exact hashes. The ID is absent from the persistent Run key. Focused timeout/cancellation and old-response rejection tests passed. |
| `d04bb75` | A read-only WPF input returned `agent_required` although the agent was responding. | Provider failures remain operation failures; transport errors still mean offline. Real read-only/disabled refusals preserved state and the agent remained usable. Tests prohibit unintended raw-keyboard fallback after provider errors. |
| `e9d8de8` | A real WPF ListItem incorrectly lacked `Select`. | Corrected SelectionItem availability property `30037` to `30036`, checked against the Windows SDK. A native LISTBOX test fails with the old constant and passes with the repair. Real WPF Item 40 scrolled into view and became selected. |
| `ecc7441` | Missing or mistyped tool arguments returned SDK plain text instead of JSON. | Validate/decode inside the structured-result wrapper before VM access. Real missing/unknown/wrong-type cases returned JSON `invalid_argument`; identifiable task/run IDs were retained. Invalid task identity is not fabricated. Null/omitted arguments remain compatible with optional-only tools. |

## Input discrepancy: diagnosed fixture failure

The initial run intermittently lost characters while typing Chinese, emoji, newline and Tab into a PowerShell WinForms fixture. Eighteen subsequent C# fixture cases, including 25 ms and 100 ms synchronous text-change workloads, received the exact full text.

Differential reproduction captured native messages, text changes, focus and managed stacks. Four of five uniquely identified synchronous-save cases failed before repair. Records show `FileInfo.MoveTo` throwing `IOException` during state publication, followed by `Application.ThreadContext.OnThreadException` and a modal exception dialog. Characters reached the application's message filter while the original TextBox had lost focus; the dialog consumed them. This explains the discrepancy without attributing it to missing SendInput events.

The local fixture now uses same-directory atomic replacement, retries transient I/O conflicts on its timer, and updates its last-published state only after success. All ten uniquely identified follow-up cases passed: five synchronous and five deferred, with no save errors. Each required an exact final value and a matching TextChanged event from that case, avoiding false success from the previous iteration. Production input code was not changed. `applied_chars` counts injected characters, not guaranteed application processing; use index-based readback or a fresh observation when confirmation matters.

## Real tool and branch coverage

| Tools / area | Independently checked behavior |
|---|---|
| `vm_list`, `vm_status`, `vm_doctor` | Identity, Running/Off, locked/unlocked, busy agent during a command, version/protocol and final healthy diagnostics. |
| `vm_apps`, `vm_launch` | Start Menu and user App Paths, stable IDs, filtering/limits/truncation/empty results, preserved argument arrays including empty/spaced arguments, actual cwd, PID/handle. A synthetic app changed from not running to running; an identically named executable in another directory remained not running. A no-window launch returned an independently verified live PID. |
| `vm_windows`, `vm_observe` | HWND/PID/class/integrity, focus, owner/group/modal state, whole-screen/window images, scaling/cropping, controls, diff/fallback, node truncation and eight-entry cache eviction. Password names/values were absent from tree and focus results. |
| `vm_click`, `vm_drag`, `vm_scroll` | Pixel/index targeting, scaled click, drag counters, vertical scrolling and horizontal wheel delivery plus actual offset. Fresh observations of covered/disabled modal targets refused input without changing click counts. |
| `vm_set_value`, `vm_invoke` | Unicode SetValue/readback; Invoke, Toggle, Expand, Collapse, Select and ScrollIntoView. Read-only/disabled refusals without mutation. A 600-character value was set while truncated readback correctly returned `verified: null`. |
| `vm_type`, `vm_key` | Targeted keyboard/chord operations, Chinese/emoji/CRLF/Tab, slow controls, exact readback and clipboard preservation. High-integrity type/key/click targets returned `integrity_mismatch`. Invalid buttons/counts/modifiers/key sequences/selectors returned actionable errors. |
| `vm_exec` | PowerShell/CMD, cwd, stdout/stderr/nonzero status, elevation, timeout and child cleanup. A timed-out child did not perform its delayed file write. Guest commands continued during the observed VMConnect disconnection. |
| `vm_push`, `vm_pull` | Nested directories, Unicode/spaced filenames, empty/binary files, identical-file skipping, same-size changed content, force upload, exact bytes and missing-path failures. |
| `vm_clipboard_get`, `vm_clipboard_set` | Unicode roundtrip, independent preservation across typing and restoration of the original clipboard without publishing its contents. |
| `vm_wait` | File/process presence, process exit, timeout and cancellation. Ending one task canceled its wait while another task's wait completed normally. |
| `vm_end_turn` | Writer exclusion with other-task reads, foreign `all_temp` refusal, own temp cleanup/release, ended-ID refusal, idempotency and scoped reuse. Final runtime cleanup waited about 4.55 seconds for an in-flight five-second command; completed output and guest marker verified. |
| Observation freshness | Old coordinates after mutation, usable after observations, foreign-task IDs, shared revisions, stable runtime IDs after ordinary mutations, autonomous local pixels, drag destination pixels, window movement and lifecycle invalidation after restore. |
| Checkpoint tools | Standard memory/disk restore, stable keep ID, `save_current`, A/B file restoration, by-ID deletion, duplicate-name ambiguity for restore/keep/delete, manual-by-name refusal, Unicode keep label, exact subtree removal and original snapshot retention. |
| `vm_shutdown`, `vm_start`, `vm_turn_off`, `vm_unlock` | Graceful shutdown, Off-to-usable cold start, already-running readiness, hard power-off/restart, lock detection and stored-credential unlock. These power branches ran in the initial round; follow-up restore/start/install and final status were rechecked. |
| `vm_install_agent`, `vm_update_agent` | Old/same-version readiness, exact running path/hash/PID, update/reconnect and final installation after restoring the original snapshot. Old-agent unsupported operations returned `agent_outdated`. |

After the structured-validation change, the final runtime repeated task ownership, cleanup, cross-task observation rejection and shared-revision checks. Live schema checks covered missing required fields, unknown fields, wrong scalar types, invalid task identity and null/empty optional arguments.

## Build and automated verification

On final product commit `ecc7441`, both commands passed:

```powershell
go test -race ./cmd/... ./internal/... -count=1 -timeout=240s
go vet ./cmd/... ./internal/...
```

Both binaries were built with `-H windowsgui`. Added regressions include real native UIA selection, installation identity, transport/provider classification and structured input validation.

`go test -race ./...` was also attempted. Maintained packages passed, but ignored historical experiment `build/service-poc-20261006/service` does not compile against current checkpoint/click APIs (`main.go:439,455,487,488,559`). It was not modified or deleted. This is a local ignored-artifact failure, not a passing full `./...` result.

## Final deployment and restoration

A running Standard checkpoint preserved guest memory/disks before each round. Final restoration recovered original Notepad PID 2012 with its unsaved-title marker, AutoCAD PID 10624 and Edge PID 9824. This verifies process/state restoration, not every byte of each application's documents.

The final agent was installed after restoration and its running path/hash checked. Guest fixtures, synthetic discovery shortcuts and test App Paths registration were absent; clipboard restored. All test snapshots were removed, leaving original `76221ce2-ee75-48bc-b135-829277608b43`, also the current parent. Final VM: Running, unlocked, basic console, all `vm_doctor` checks OK.

Host service Running; tray at installed path; existing disabled **Start with Windows** preference preserved. These are local acceptance builds of committed source, not an official release.

| Binary | Installed location | SHA-256 |
|---|---|---|
| Host | `C:\Program Files\HyperHand\hyperhand.exe` | `40AD7CE162FEC262F299B87F0110FE4C3888E17BC0E3E175A5F17049FC5402E4` |
| Agent | Host staged copy and guest `%LOCALAPPDATA%\HyperHand\hyperhand-agent.exe` | `91069D89F29F71D8930E8F7640573D505F376AE3A0CCFD8552E2AAD15F0DBF38` |

## Evidence and limits

Ignored local artifacts: `build/acceptance-20261010/` and `build/acceptance-20261010-full/`, containing per-call JSON/PNG, assertion scripts, fixture sources, input message traces, installation identities and binary hashes. Key follow-up summaries:

- `input-results.json`: 18 exact C# input cases.
- `input-legacy-v2-results-f60f1b.json`: fixture failure reproduction.
- `input-legacy-fixed-results-fdd83f.json`: ten repaired-fixture cases.
- `advanced-143342-d2db8e-results.json`: advanced UI checks.
- `extras-passed.json`, `boundaries-passed.json`, `integrity-passed.json`, `tasks-passed.json`, `checkpoint-boundaries-passed.json`, `final-inflight-passed.json`: supplemental assertions.
- `cleanup-passed.json`, `host-final.json`, `guest-final.json`: final restoration/deployment.

Repeated labels can overwrite earlier diagnostic records; file counts are not total request counts.

Not exercised: enhanced/RDP sessions, multiple guest monitors/DPI configurations, a second VM, Production-only checkpoints, host uninstall, interactive secure-desktop UAC approval/cancellation, credential mismatch, every key/modifier combination or every injected failure. Elevation used the VM's existing no-consent administrator policy and does not establish interactive UAC behavior. Discovery reports four unreadable processes, so `running: false` can be incomplete. Screenshot freshness is a local heuristic, not atomic with arbitrary later repaint.

The host VMConnect viewer can still show its disconnected/reconnect dialog after lifecycle operations. Current console integration only foregrounds an existing viewer; automatic reconnection is not implemented. The independent guest screenshot/command channel worked during the observed disconnection. Guest tools cannot click a host-side dialog; this run did not introduce a privileged host UI helper.
