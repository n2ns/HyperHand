package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"

	"hyperhand/internal/proto"
)

type setupIdentity struct{ SID, User string }

// Resolve the original caller before UAC. USERNAME/USERPROFILE after alternate
// administrator credentials are entered refer to a different person.
func setupOwner(args []string) (setupIdentity, error) {
	var owner setupIdentity
	f := flag.NewFlagSet("setup", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	f.StringVar(&owner.SID, "owner-sid", "", "original installing user SID")
	f.StringVar(&owner.User, "owner-user", "", "original installing account")
	if err := f.Parse(args); err != nil {
		return owner, err
	}
	if f.NArg() != 0 {
		return owner, errors.New("unexpected setup arguments")
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return owner, err
	}
	if owner.SID == "" && owner.User == "" {
		owner.SID = user.User.Sid.String()
		name, domain, _, err := user.User.Sid.LookupAccount("")
		if err != nil {
			return owner, err
		}
		owner.User = domain + `\` + name
	}
	if owner.SID == "" || owner.User == "" {
		return owner, errors.New("owner SID and account must be supplied together")
	}
	sid, _, _, err := windows.LookupSID("", owner.User)
	if err != nil || sid.String() != owner.SID {
		return owner, errors.New("owner account does not match owner SID")
	}
	if !windows.GetCurrentProcessToken().IsElevated() && owner.SID != user.User.Sid.String() {
		return owner, errors.New("setup must be launched by the installing user")
	}
	return owner, nil
}

// Existing protected roots must already have an administrator/SYSTEM owner.
// Creating the new directory with its final descriptor avoids a writable gap.
func setupRoot(path, ownerSID string) error {
	if err := noSetupReparse(path); err != nil {
		return err
	}
	if _, err := os.Stat(path); err == nil {
		sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
		if err != nil {
			return err
		}
		owner, _, err := sd.Owner()
		if err != nil {
			return err
		}
		if owner.String() != "S-1-5-32-544" && owner.String() != "S-1-5-18" {
			return fmt.Errorf("untrusted installation directory owner: %s", path)
		}
	} else if !os.IsNotExist(err) {
		return err
	} else {
		sd, err := windows.SecurityDescriptorFromString(setupRootSDDL(ownerSID))
		if err != nil {
			return err
		}
		p, _ := windows.UTF16PtrFromString(path)
		return windows.CreateDirectory(p, &windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd})
	}
	sd, err := windows.SecurityDescriptorFromString(setupRootSDDL(ownerSID))
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
}

func setupRootSDDL(ownerSID string) string {
	return "O:BAG:SYD:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;FRFX;;;" + ownerSID + ")"
}

func noSetupReparse(path string) error {
	if !filepath.IsAbs(path) {
		return errors.New("setup path must be absolute")
	}
	for current := filepath.Clean(path); ; current = filepath.Dir(current) {
		p, err := windows.UTF16PtrFromString(current)
		if err != nil {
			return err
		}
		attrs, err := windows.GetFileAttributes(p)
		if err != nil && !errors.Is(err, windows.ERROR_FILE_NOT_FOUND) && !errors.Is(err, windows.ERROR_PATH_NOT_FOUND) {
			return err
		}
		if err == nil && attrs&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
			return fmt.Errorf("refusing reparse point: %s", current)
		}
		if filepath.Dir(current) == current {
			return nil
		}
	}
}

func runSetup(operation string) (bool, error) {
	owner, err := setupOwner(os.Args[2:])
	if err != nil {
		return false, err
	}
	if !windows.GetCurrentProcessToken().IsElevated() {
		return false, runAs(operation, "--owner-sid", owner.SID, "--owner-user", owner.User)
	}
	programData, err := windows.KnownFolderPath(windows.FOLDERID_ProgramData, 0)
	if err != nil {
		return false, err
	}
	programFiles, err := windows.KnownFolderPath(windows.FOLDERID_ProgramFiles, 0)
	if err != nil {
		return false, err
	}
	root, bin := filepath.Join(programData, "HyperHand"), filepath.Join(programFiles, "HyperHand")
	for _, path := range []string{root, bin, filepath.Join(root, "config.json")} {
		if err := noSetupReparse(path); err != nil {
			return false, err
		}
	}
	configPath := filepath.Join(root, "config.json")
	if b, err := os.ReadFile(configPath); err == nil {
		var config struct {
			OwnerSID string `json:"owner_sid"`
		}
		if err := json.Unmarshal(b, &config); err != nil {
			return false, err
		}
		if config.OwnerSID != owner.SID {
			return false, errors.New("installed owner differs; run setup from the original installing user's session")
		}
	} else if !os.IsNotExist(err) {
		return false, err
	} else {
		if operation == "uninstall" {
			return false, errors.New("managed installation not found; no service or files were removed")
		}
		for _, path := range []string{root, bin} {
			entries, err := os.ReadDir(path)
			if err == nil && len(entries) != 0 {
				return false, fmt.Errorf("unrecognized non-empty installation directory: %s", path)
			}
			if err != nil && !os.IsNotExist(err) {
				return false, err
			}
		}
	}
	if err := setupRoot(root, owner.SID); err != nil {
		return false, err
	}
	self, err := os.Executable()
	if err != nil {
		return false, err
	}
	if err := noSetupReparse(self); err != nil {
		return false, err
	}
	system, err := windows.GetSystemDirectory()
	if err != nil {
		return false, err
	}
	f, err := os.CreateTemp(root, "setup-*.ps1")
	if err != nil {
		return false, err
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(setupScript); err != nil {
		f.Close()
		return false, err
	}
	if err := f.Close(); err != nil {
		return false, err
	}
	// A pre-existing root is never relied on for the executable script's ACL.
	sd, err := windows.SecurityDescriptorFromString("O:BAG:SYD:P(A;;FA;;;SY)(A;;FA;;;BA)")
	if err != nil {
		return false, err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return false, err
	}
	if err := windows.SetNamedSecurityInfo(f.Name(), windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		return false, err
	}
	cmd := exec.Command(filepath.Join(system, "WindowsPowerShell", "v1.0", "powershell.exe"), "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", f.Name(),
		"-Operation", operation, "-OwnerSID", owner.SID, "-OwnerUser", owner.User, "-SourceExe", self, "-BinDir", bin, "-DataDir", root, "-SetupPID", fmt.Sprint(os.Getpid()), "-SocketID", proto.ServiceID)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	output, err := cmd.CombinedOutput()
	if err != nil {
		return false, fmt.Errorf("setup: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return true, nil
}

const setupScript = `param([ValidateSet('install','uninstall')][string]$Operation,[string]$OwnerSID,[string]$OwnerUser,[string]$SourceExe,[string]$BinDir,[string]$DataDir,[int]$SetupPID,[guid]$SocketID)
$ErrorActionPreference='Stop'
[Console]::OutputEncoding=[Text.Encoding]::UTF8
$serviceName='HyperHandService'
$serviceAccount='NT SERVICE\HyperHandService'
$hostExe=Join-Path $BinDir 'hyperhand.exe'
$agentExe=Join-Path $BinDir 'hyperhand-agent.exe'
$configPath=Join-Path $DataDir 'config.json'
$serviceData=Join-Path $DataDir 'service-data'
$cleanup=Join-Path $DataDir 'uninstall-cleanup.ps1'
$socketPath='HKLM:\SOFTWARE\Microsoft\Windows NT\CurrentVersion\Virtualization\GuestCommunicationServices\'+$SocketID.ToString()
$groupSID=[Security.Principal.SecurityIdentifier]'S-1-5-32-578'
function Assert-Path([string]$Path) {
 if (-not [IO.Path]::IsPathRooted($Path)) { throw 'Expected absolute path' }
 $current=$Path
 while ($current) {
  if (Test-Path -LiteralPath $current) {
   $item=Get-Item -LiteralPath $current -Force
   if ($item.Attributes -band [IO.FileAttributes]::ReparsePoint) { throw ('Refusing reparse point: '+$current) }
  }
  $parent=[IO.Path]::GetDirectoryName($current)
  if ($parent -eq $current) { break }
  $current=$parent
 }
}
function Set-ProtectedACL([string]$Path,[string]$Extra) {
 Assert-Path $Path
 $acl=Get-Acl -LiteralPath $Path
 $acl.SetSecurityDescriptorSddlForm('O:BAG:SYD:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)'+$Extra)
 Set-Acl -LiteralPath $Path -AclObject $acl
}
function Assert-Owned([string]$Path) {
 Assert-Path $Path
 $owner=(Get-Acl -LiteralPath $Path).GetOwner([Security.Principal.SecurityIdentifier]).Value
 if ($owner -notin @('S-1-5-18','S-1-5-32-544')) { throw ('Untrusted resource owner: '+$Path) }
}
function Stop-Broker {
 $s=Get-Service -Name $serviceName -ErrorAction SilentlyContinue
 if ($s -and $s.Status -ne 'Stopped') { Stop-Service -Name $serviceName -NoWait; $s.WaitForStatus('Stopped',[TimeSpan]::FromSeconds(60)) }
}
function Stop-ExactProcesses([string[]]$Paths) {
 foreach ($p in Get-Process -Name 'hyperhand' -ErrorAction SilentlyContinue) {
  if ($p.Id -eq $SetupPID) { continue }
  try { $path=$p.MainModule.FileName } catch { throw ('Cannot verify HyperHand process '+$p.Id) }
  if ($path -in $Paths) { $p.Kill(); if (-not $p.WaitForExit(10000)) { throw 'HyperHand process did not stop' } }
 }
}
function Set-GroupMember([string]$SID,[bool]$Present) {
 $found=@(Get-LocalGroupMember -SID $groupSID | Where-Object {$_.SID.Value -eq $SID}).Count -ne 0
 if ($Present -and -not $found) { Add-LocalGroupMember -SID $groupSID -Member $SID }
 if (-not $Present -and $found) { Remove-LocalGroupMember -SID $groupSID -Member $SID }
 $after=@(Get-LocalGroupMember -SID $groupSID | Where-Object {$_.SID.Value -eq $SID}).Count -ne 0
 if ($after -ne $Present) { throw 'Hyper-V group membership verification failed' }
}
function Check-Native { if ($LASTEXITCODE -ne 0) { throw ('Native command failed: '+$LASTEXITCODE) } }
Add-Type -TypeDefinition @'
using System;
using System.Runtime.InteropServices;
public static class HyperHandSCM {
 [DllImport("advapi32.dll",CharSet=CharSet.Unicode,SetLastError=true)] public static extern IntPtr OpenSCManager(string machine,string database,uint access);
 [DllImport("advapi32.dll",CharSet=CharSet.Unicode,SetLastError=true)] public static extern IntPtr CreateService(IntPtr manager,string name,string display,uint access,uint type,uint start,uint error,string path,string group,IntPtr tag,string dependencies,string account,string password);
 [DllImport("advapi32.dll",CharSet=CharSet.Unicode,SetLastError=true)] public static extern IntPtr OpenService(IntPtr manager,string name,uint access);
 [DllImport("advapi32.dll",CharSet=CharSet.Unicode,SetLastError=true)] public static extern bool ChangeServiceConfig(IntPtr service,uint type,uint start,uint error,string path,string group,IntPtr tag,string dependencies,string account,string password,string display);
 [DllImport("advapi32.dll",SetLastError=true)] public static extern bool DeleteService(IntPtr service);
 [DllImport("advapi32.dll")] public static extern bool CloseServiceHandle(IntPtr handle);
 public static void Install(string name,string path,string account,bool exists) {
  IntPtr m=OpenSCManager(null,null,0xF003F);
  if(m==IntPtr.Zero) throw new System.ComponentModel.Win32Exception(Marshal.GetLastWin32Error());
  try {
   IntPtr s=exists ? OpenService(m,name,0xF01FF) : CreateService(m,name,name,0xF01FF,0x10,2,1,path,null,IntPtr.Zero,null,account,null);
   if(s==IntPtr.Zero) throw new System.ComponentModel.Win32Exception(Marshal.GetLastWin32Error());
   try { if(exists && !ChangeServiceConfig(s,uint.MaxValue,2,1,path,null,IntPtr.Zero,null,account,null,null)) throw new System.ComponentModel.Win32Exception(Marshal.GetLastWin32Error()); }
   finally { CloseServiceHandle(s); }
  } finally { CloseServiceHandle(m); }
 }
 public static void Delete(string name) {
  IntPtr m=OpenSCManager(null,null,0xF003F);
  if(m==IntPtr.Zero) throw new System.ComponentModel.Win32Exception(Marshal.GetLastWin32Error());
  try {
   IntPtr s=OpenService(m,name,0xF01FF);
   if(s==IntPtr.Zero) throw new System.ComponentModel.Win32Exception(Marshal.GetLastWin32Error());
   try { if(!DeleteService(s)) throw new System.ComponentModel.Win32Exception(Marshal.GetLastWin32Error()); }
   finally { CloseServiceHandle(s); }
  } finally { CloseServiceHandle(m); }
 }
}
'@
foreach ($path in @($BinDir,$DataDir,$hostExe,$agentExe,$configPath,$serviceData,$SourceExe)) { Assert-Path $path }
Assert-Path $cleanup
if (Test-Path -LiteralPath $cleanup) { throw 'Pending uninstall cleanup requires completion or inspection before setup' }
Assert-Owned $DataDir
if (([Security.Principal.NTAccount]$OwnerUser).Translate([Security.Principal.SecurityIdentifier]).Value -ne $OwnerSID) { throw 'Owner SID/account mismatch' }
if (Test-Path -LiteralPath $configPath) {
 Assert-Owned $configPath
 if ((Get-Content -LiteralPath $configPath -Raw -Encoding UTF8 | ConvertFrom-Json).owner_sid -ne $OwnerSID) { throw 'Existing owner differs' }
}
$task=Get-ScheduledTask -TaskName 'HyperHand' -TaskPath '\' -ErrorAction SilentlyContinue
$oldExe=$null
if ($task) {
 $taskUser=$task.Principal.UserId
 if (-not $taskUser.StartsWith('S-1-')) { $taskUser=([Security.Principal.NTAccount]$taskUser).Translate([Security.Principal.SecurityIdentifier]).Value }
 if ($taskUser -ne $OwnerSID -or @($task.Actions).Count -ne 1) { throw 'Existing HyperHand task belongs to another user or has unexpected actions' }
 $oldExe=$task.Actions[0].Execute.Trim('"')
 if ([IO.Path]::GetFileName($oldExe) -ine 'hyperhand.exe' -or $task.Actions[0].Arguments) { throw 'Existing task is not a recognized HyperHand tray task' }
 Assert-Path $oldExe
}
$service=Get-CimInstance Win32_Service -Filter "Name='HyperHandService'"
$serviceCommand='"'+$hostExe+'" service'
if ($service -and ($service.PathName -ine $serviceCommand -or $service.StartName -ine $serviceAccount)) { throw 'Existing service has unexpected binary or account' }
if ($Operation -eq 'install') {
 $sourceAgent=Join-Path ([IO.Path]::GetDirectoryName($SourceExe)) 'hyperhand-agent.exe'
 Assert-Path $sourceAgent
 if (-not (Test-Path -LiteralPath $sourceAgent -PathType Leaf)) { throw 'Place hyperhand-agent.exe beside hyperhand.exe before installing' }
 if (-not (Test-Path -LiteralPath $BinDir)) { New-Item -ItemType Directory -Path $BinDir | Out-Null }
 else { Assert-Owned $BinDir }
 Set-ProtectedACL $BinDir '(A;OICI;FRFX;;;BU)'
 Set-ProtectedACL $DataDir ('(A;OICI;FRFX;;;'+$OwnerSID+')')
 [IO.File]::WriteAllText($configPath,(@{owner_sid=$OwnerSID} | ConvertTo-Json),[Text.UTF8Encoding]::new($false))
 Set-ProtectedACL $configPath ('(A;;FR;;;'+$OwnerSID+')')
 Stop-Broker
 if ($task) { Stop-ScheduledTask -TaskName 'HyperHand' -TaskPath '\' }
 Stop-ExactProcesses @($hostExe,$SourceExe,$oldExe)
 foreach ($pair in @(@($SourceExe,$hostExe),@($sourceAgent,$agentExe))) {
  if ($pair[0] -ine $pair[1]) {
   if (Test-Path -LiteralPath $pair[1]) { Assert-Owned $pair[1] }
   $next=$pair[1]+'.installing'
   Assert-Path $next
   if (Test-Path -LiteralPath $next) { throw ('Stale installer file: '+$next) }
   try {
    Copy-Item -LiteralPath $pair[0] -Destination $next
    Set-ProtectedACL $next '(A;OICI;FRFX;;;BU)'
    if ((Get-FileHash -LiteralPath $pair[0] -Algorithm SHA256).Hash -ne (Get-FileHash -LiteralPath $next -Algorithm SHA256).Hash) { throw 'Copied executable hash mismatch' }
    Move-Item -LiteralPath $next -Destination $pair[1] -Force
   } finally { if (Test-Path -LiteralPath $next) { Remove-Item -LiteralPath $next } }
  }
 }
 [HyperHandSCM]::Install($serviceName,$serviceCommand,$serviceAccount,[bool]$service)
 $serviceSID=([Security.Principal.NTAccount]$serviceAccount).Translate([Security.Principal.SecurityIdentifier]).Value
 & "$env:SystemRoot\System32\sc.exe" 'sidtype' $serviceName 'unrestricted'; Check-Native
 $serviceACL='D:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;0x20015;;;'+$OwnerSID+')'
 & "$env:SystemRoot\System32\sc.exe" 'sdset' $serviceName $serviceACL; Check-Native
 Set-ProtectedACL $BinDir ('(A;OICI;FRFX;;;BU)(A;OICI;FRFX;;;'+$serviceSID+')')
 foreach ($path in @($hostExe,$agentExe)) { Set-ProtectedACL $path ('(A;;FRFX;;;BU)(A;;FRFX;;;'+$serviceSID+')') }
 Set-ProtectedACL $DataDir ('(A;OICI;FRFX;;;'+$OwnerSID+')(A;OICI;FRFX;;;'+$serviceSID+')')
 Set-ProtectedACL $configPath ('(A;;FR;;;'+$OwnerSID+')(A;;FR;;;'+$serviceSID+')')
 if (-not (Test-Path -LiteralPath $serviceData)) { New-Item -ItemType Directory -Path $serviceData | Out-Null }
 else { Assert-Owned $serviceData }
 Set-ProtectedACL $serviceData ('(A;OICI;0x1301bf;;;'+$serviceSID+')')
 Set-GroupMember $serviceSID $true
 $installedService=Get-CimInstance Win32_Service -Filter "Name='HyperHandService'"
 if ($installedService.StartName -ine $serviceAccount -or $installedService.PathName -ine $serviceCommand -or $installedService.StartMode -ne 'Auto') { throw 'Service account, executable or automatic startup verification failed' }
 New-Item -Path $socketPath -Force | Out-Null
 New-ItemProperty -LiteralPath $socketPath -Name 'ElementName' -PropertyType String -Value 'HyperHand' -Force | Out-Null
 Start-Service -Name $serviceName
 (Get-Service -Name $serviceName).WaitForStatus('Running',[TimeSpan]::FromSeconds(60))
 $action=New-ScheduledTaskAction -Execute $hostExe
 $trigger=New-ScheduledTaskTrigger -AtLogOn -User $OwnerSID
 $principal=New-ScheduledTaskPrincipal -UserId $OwnerSID -LogonType Interactive -RunLevel Limited
 $settings=New-ScheduledTaskSettingsSet -ExecutionTimeLimit ([TimeSpan]::Zero) -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries
 Register-ScheduledTask -TaskName 'HyperHand' -TaskPath '\' -Action $action -Trigger $trigger -Principal $principal -Settings $settings -Force | Out-Null
 $installedTask=Get-ScheduledTask -TaskName 'HyperHand' -TaskPath '\'
 if ($installedTask.Principal.RunLevel -ne 'Limited') { throw 'Logon task is not limited' }
 Start-ScheduledTask -TaskName 'HyperHand' -TaskPath '\'
} else {
 $hostHash='missing'
 $agentHash='missing'
 if (Test-Path -LiteralPath $hostExe) { $hostHash=(Get-FileHash -LiteralPath $hostExe -Algorithm SHA256).Hash }
 if (Test-Path -LiteralPath $agentExe) { $agentHash=(Get-FileHash -LiteralPath $agentExe -Algorithm SHA256).Hash }
 Stop-Broker
 if ($task) { Stop-ScheduledTask -TaskName 'HyperHand' -TaskPath '\'; Unregister-ScheduledTask -TaskName 'HyperHand' -TaskPath '\' -Confirm:$false }
 Stop-ExactProcesses @($hostExe,$oldExe)
 if ($service) {
  $serviceSID=([Security.Principal.NTAccount]$serviceAccount).Translate([Security.Principal.SecurityIdentifier]).Value
  Set-GroupMember $serviceSID $false
  [HyperHandSCM]::Delete($serviceName)
 }
 if (Test-Path -LiteralPath $socketPath) { Remove-Item -LiteralPath $socketPath }
 if ((Test-Path -LiteralPath $serviceData) -and @(Get-ChildItem -LiteralPath $serviceData -Force).Count -eq 0) { Remove-Item -LiteralPath $serviceData }
 # The current executable may be the installed host. A protected helper waits
 # for it to close; only named files with the original hashes are removed.
 @'
param([int]$WaitPID,[string]$BinDir,[string]$DataDir,[string]$HostHash,[string]$AgentHash)
$ErrorActionPreference='Stop'
function Assert-Safe([string]$Path) {
 $current=$Path
 while ($current) {
  if (Test-Path -LiteralPath $current) { if ((Get-Item -LiteralPath $current -Force).Attributes -band [IO.FileAttributes]::ReparsePoint) { throw 'Refusing reparse path' } }
  $parent=[IO.Path]::GetDirectoryName($current)
  if ($parent -eq $current) { break }; $current=$parent
 }
}
try {
 Wait-Process -Id $WaitPID -ErrorAction SilentlyContinue
 Assert-Safe $BinDir; Assert-Safe $DataDir
 foreach ($f in @(@('hyperhand.exe',$HostHash),@('hyperhand-agent.exe',$AgentHash))) {
  $path=Join-Path $BinDir $f[0]
  if (Test-Path -LiteralPath $path) {
   Assert-Safe $path
   if ((Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash -ne $f[1]) { throw 'Executable changed; cleanup stopped' }
   Remove-Item -LiteralPath $path -Force
  }
 }
 if ((Test-Path -LiteralPath $BinDir) -and @(Get-ChildItem -LiteralPath $BinDir -Force).Count -eq 0) { Remove-Item -LiteralPath $BinDir }
 # Keep the owner marker with retained service data, so reinstall can verify
 # ownership without deleting those files. Otherwise remove it last.
 $remaining=@(Get-ChildItem -LiteralPath $DataDir -Force | Where-Object { $_.FullName -ne $PSCommandPath -and $_.Name -ne 'config.json' })
 if ($remaining.Count -eq 0) { Remove-Item -LiteralPath (Join-Path $DataDir 'config.json') -ErrorAction SilentlyContinue }
 Remove-Item -LiteralPath $PSCommandPath -Force
 if (@(Get-ChildItem -LiteralPath $DataDir -Force).Count -eq 0) { Remove-Item -LiteralPath $DataDir }
} catch { $_.Exception.ToString() | Set-Content -LiteralPath (Join-Path $DataDir 'uninstall-error.log') -Encoding UTF8 }
'@ | Set-Content -LiteralPath $cleanup -Encoding UTF8
 Set-ProtectedACL $cleanup ''
 $cleanupArguments='-NoProfile -NonInteractive -ExecutionPolicy Bypass -File "'+$cleanup+'" -WaitPID '+$SetupPID+' -BinDir "'+$BinDir+'" -DataDir "'+$DataDir+'" -HostHash '+$hostHash+' -AgentHash '+$agentHash
 Start-Process -FilePath "$env:SystemRoot\System32\WindowsPowerShell\v1.0\powershell.exe" -ArgumentList $cleanupArguments -WindowStyle Hidden | Out-Null
}
`
