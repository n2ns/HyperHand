# Semantic control acceptance — 2026-10-10

This follow-up covers the AI-facing semantic control additions, not a repeat of the complete 32-tool acceptance in `acceptance-20261010.md`.

## Changes and evidence

| Commit | Change | Verification completed |
| --- | --- | --- |
| `e55534d` | Executable actions and readable control state in observations | Real Win10 MCP observation, state-only diff, password redaction, unsupported-action names; native Win32 state tests and host race tests |
| `0fb8781` | Post-action state and three-valued verification | Real Win10 MCP Toggle, Expand, Collapse, Select, ScrollIntoView and empty SetValue; independent WPF fixture state confirms changes; Invoke remains unverified while its click counter changes |
| `1b46574` | Control-targeted four-direction ScrollPattern | Real Win10 MCP four directions and four boundaries with independent WPF offsets, axis filtering/refusals and percentage diffs; native Win32 `LB_GETTOPINDEX`; host command mappings, percentage/unknown/boundary tests and explicit empty actions surviving JSON transport |

Final source passed `go test -race ./cmd/... ./internal/... -count=1 -timeout=240s` and `go vet ./cmd/... ./internal/...` on the host. The final race-enabled `internal/agent` test binary also passed its complete suite inside Win10. Independent source review found no remaining findings.

An earlier host `TestHScroll` run failed at `SetCursorPos`; the same test passed in the guest and in the final complete host rerun after the pending UAC prompt ended. No mouse code was changed to suppress that failure.

## Installed runtime and VM validation

After the user approved the renewed Windows UAC prompt, the installed host and guest were updated to `semantic-scroll-20261010`. The WPF test through the installed host MCP server passed all 12 stages: deployment, axis/action discovery, four directional movements, four unchanged boundaries, unsupported-axis/control refusals and percentage diffs. Each boundary returned `verified:false` with its actual position; movement returned `verified:true` and matched the fixture's independent offset.

Cleanup restored the initial running VM checkpoint, reinstalled the final guest agent and verified its path/hash, restored the clipboard, deleted the test baseline checkpoint and ended the task. The original Notepad (including its unsaved marker), AutoCAD and Edge processes remain. The semantic fixture is absent, only the original manual checkpoint remains and all `vm_doctor` checks are OK. The installed host service is running, the tray uses its installed path and Start with Windows remains disabled.

Final read-only observations of the original applications also passed: Notepad returned 27 nodes; AutoCAD returned the requested 200-node limit with `controls_truncated:true`. Both exposed actions and semantic state without a stale-risk warning. This checks observation compatibility, not exhaustive coverage of AutoCAD's custom drawing surface.

The final binaries are in ignored `build/semantic-20261010/phase3-bin/`; the installed host and staged agent match their hashes:

| File | SHA-256 |
| --- | --- |
| `hyperhand.exe` | `CB772589F27248D90E1990B88EBE208FB65035600182B0F70FB62C3474726F8C` |
| `hyperhand-agent.exe` | `F8F723CBB9876E055C49E682D601C2849D3FE9698DEED08EFD6B61E9C6862578` |

The ignored `phase3_live.py` harness also checks that each directional movement leaves the other control's offset, button click count and horizontal mouse-wheel event count unchanged. The fixture publishes state atomically to avoid partial-file reads. This distinguishes targeted UIA scrolling from unintended input side effects. Both complete runs passed; the strengthened final run is `phase3-advanced-154706-b36d41-results.json`. Independent review of that result confirmed horizontal and vertical movements of `0 -> 16 -> 0`, all four unchanged boundaries returning `verified:false`, and no cross-control or mouse-wheel side effects.

Test scripts and raw MCP evidence are retained under ignored `build/semantic-20261010/`. UIA coverage remains provider-dependent; Invoke has no generic business-success postcondition, and asynchronous changes beyond the bounded read-back interval can require another observation.
