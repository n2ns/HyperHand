# Semantic control acceptance — 2026-10-10

This follow-up covers the AI-facing semantic control additions, not a repeat of the complete 32-tool acceptance in `acceptance-20261010.md`.

## Changes and evidence

| Commit | Change | Verification completed |
| --- | --- | --- |
| `e55534d` | Executable actions and readable control state in observations | Real Win10 MCP observation, state-only diff, password redaction, unsupported-action names; native Win32 state tests and host race tests |
| `0fb8781` | Post-action state and three-valued verification | Real Win10 MCP Toggle, Expand, Collapse, Select, ScrollIntoView and empty SetValue; independent WPF fixture state confirms changes; Invoke remains unverified while its click counter changes |
| `1b46574` | Control-targeted four-direction ScrollPattern | Native Win32 ListBox up/down, boundary, axis filtering and independent `LB_GETTOPINDEX`; all four command mappings through host MCP tests; percentage/unknown/boundary verification tests; explicit empty actions survive JSON transport |

Final source passed `go test -race ./cmd/... ./internal/... -count=1 -timeout=240s` and `go vet ./cmd/... ./internal/...` on the host. The final race-enabled `internal/agent` test binary also passed its complete suite inside Win10. Independent source review found no remaining findings.

An earlier host `TestHScroll` run failed at `SetCursorPos`; the same test passed in the guest and in the final complete host rerun after the pending UAC prompt ended. No mouse code was changed to suppress that failure.

## Remaining deployment validation

The four-direction WPF test through the installed host MCP server has **not run**. Deployment of `semantic-scroll-20261010` was stopped by Windows UAC: the installer logged `The operation was canceled by the user.` at 15:26:27 local time. The installed host and guest remain on the verified second-stage build `semantic-verify-20261010`.

Cleanup restored the initial running VM checkpoint, reinstalled the second-stage guest agent and verified its path/hash, restored the clipboard, deleted the temporary baseline checkpoint and ended the task. The original Notepad (including its unsaved marker), AutoCAD and Edge processes remain; the semantic fixture and test binary are absent. Only the original manual checkpoint remains, and `vm_doctor` reports all checks OK.

The prepared final binaries are in ignored `build/semantic-20261010/phase3-bin/`:

| File | SHA-256 |
| --- | --- |
| `hyperhand.exe` | `CB772589F27248D90E1990B88EBE208FB65035600182B0F70FB62C3474726F8C` |
| `hyperhand-agent.exe` | `F8F723CBB9876E055C49E682D601C2849D3FE9698DEED08EFD6B61E9C6862578` |

The ignored `phase3_live.py` harness has 12 stages for four directions, four boundaries, actual WPF offsets, unsupported axes/controls and percentage diffs. It must be run after installer approval and a fresh VM baseline checkpoint; these pending cases are not claimed as passed. Host and guest runtime path/hash must then be rechecked.

Test scripts and raw MCP evidence are retained under ignored `build/semantic-20261010/`. UIA coverage remains provider-dependent; Invoke has no generic business-success postcondition, and asynchronous changes beyond the bounded read-back interval can require another observation.
