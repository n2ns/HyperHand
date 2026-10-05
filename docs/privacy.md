# Privacy

HyperHand runs entirely on your machine and its Hyper-V VMs.

## Network

- HyperHand collects no telemetry and makes no outbound network connections.
- The host MCP server listens on `127.0.0.1` only (port 8770 by default, set with `-port`). It is not reachable from other machines.
- Traffic between the host and the guest agent uses Hyper-V sockets. It does not pass through the guest's or the host's network adapters.
- Copying the agent into the guest uses the Hyper-V Guest Service Interface (`Copy-VMFile`).

## Data handled

Screenshots, typed text, clipboard contents, command output and file contents pass between the guest, the host tray and the MCP client that requested them. HyperHand does not store them, apart from the files you copy with `vm_push` and `vm_pull`.

## Data stored locally

On the host:

- Log file: `%LOCALAPPDATA%\HyperHand\hyperhand.log` (listening address, errors).
- Scheduled task `HyperHand`, created by `hyperhand.exe install`.
- Registry key `HKLM\SOFTWARE\Microsoft\Windows NT\CurrentVersion\Virtualization\GuestCommunicationServices\3ce544e1-2645-4383-b332-fedf8a18736b`, which registers the Hyper-V socket service.
- Unique `.hyperhand-*.hhpart` files in the destination directory while `vm_pull` writes a file; renamed to the target on completion, deleted on failure.

In the guest:

- The agent at `C:\Users\Public\HyperHand\hyperhand-agent.exe` (copied by `vm_install_agent`) and `%LOCALAPPDATA%\HyperHand\hyperhand-agent.exe` (installed copy).
- Registry value `HyperHandAgent` under `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`.
- Unique `.hyperhand-*.hhpart` files in the destination directory while `vm_push` writes a file; renamed to the target on completion, deleted on failure.
- Temporary `hh-admin-*.ps1` files under the elevated worker's temp directory for PowerShell commands with `admin: true`; deleted when the command finishes. Commands and results pass over a single-use local named pipe, not a guest network connection.

## Access control

The MCP server has no authentication. Any local process that can reach `127.0.0.1:8770` can control the VMs through all HyperHand tools, including running commands in the guest and copying files between host and guest.
