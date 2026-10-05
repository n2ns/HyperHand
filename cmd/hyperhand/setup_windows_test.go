package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestSetupOwnerRoundTrip(t *testing.T) {
	owner, err := setupOwner(nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := setupOwner([]string{"--owner-sid", owner.SID, "--owner-user", owner.User})
	if err != nil || got != owner {
		t.Fatalf("UAC identity round trip: %#v, %v", got, err)
	}
	for _, args := range [][]string{
		{"--owner-sid", owner.SID},
		{"--owner-user", owner.User},
		{"--owner-sid", "S-1-5-18", "--owner-user", owner.User},
		{"unexpected"},
	} {
		if _, err := setupOwner(args); err == nil {
			t.Fatalf("accepted ambiguous/mismatched identity: %v", args)
		}
	}
}

func TestSetupRejectsReparseAncestor(t *testing.T) {
	base := t.TempDir()
	if err := noSetupReparse(filepath.Join(base, "missing", "file")); err != nil {
		t.Fatal(err)
	}
	if err := noSetupReparse(`relative\file`); err == nil {
		t.Fatal("accepted relative path")
	}
	target, link := filepath.Join(base, "target"), filepath.Join(base, "link")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink privilege unavailable: %v", err)
	}
	if err := noSetupReparse(filepath.Join(link, "not-created")); err == nil {
		t.Fatal("accepted reparse ancestor")
	}
}

func TestSetupACLHasNoUserWriteGrant(t *testing.T) {
	owner, err := setupOwner(nil)
	if err != nil {
		t.Fatal(err)
	}
	sd, err := windows.SecurityDescriptorFromString(setupRootSDDL(owner.SID))
	if err != nil {
		t.Fatal(err)
	}
	s := sd.String()
	if !strings.Contains(s, "D:P") || !strings.Contains(s, ";;;"+owner.SID+")") {
		t.Fatalf("missing protected DACL or original owner: %s", s)
	}
	if strings.Contains(s, "(A;OICI;FA;;;"+owner.SID+")") {
		t.Fatal("owner may modify privileged setup files")
	}
}

// Parse both embedded PowerShell programs and compile only the C# interop
// definitions. Never execute setup, service, registry or VM operations here.
func TestSetupPowerShellParses(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "setup.ps1")
	if err := os.WriteFile(script, []byte(setupScript), 0600); err != nil {
		t.Fatal(err)
	}
	harness := filepath.Join(dir, "parse.ps1")
	const parser = `param([string]$Source)
$ErrorActionPreference='Stop'
$tokens=$null; $errors=$null
$ast=[Management.Automation.Language.Parser]::ParseFile($Source,[ref]$tokens,[ref]$errors)
if ($errors.Count) { throw ($errors | Out-String) }
$strings=$ast.FindAll({param($n) $n -is [Management.Automation.Language.StringConstantExpressionAst]},$true)
$interop=$strings | Where-Object {$_.Value.Contains('public static class HyperHandSCM')}
if (@($interop).Count -ne 1) { throw 'SCM wrapper not found' }
Add-Type -TypeDefinition $interop.Value
$cleanup=$strings | Where-Object {$_.Value.StartsWith('param([int]$WaitPID')}
if (@($cleanup).Count -ne 1) { throw 'Cleanup script not found' }
[void][Management.Automation.Language.Parser]::ParseInput($cleanup.Value,[ref]$tokens,[ref]$errors)
if ($errors.Count) { throw ($errors | Out-String) }
# Check the exact read-only path traversal used by setup on PowerShell 5.1.
$function=$ast.Find({param($n) $n -is [Management.Automation.Language.FunctionDefinitionAst] -and $n.Name -eq 'Assert-Path'},$true)
. ([scriptblock]::Create($function.Extent.Text))
Assert-Path $Source
`
	if err := os.WriteFile(harness, []byte(parser), 0600); err != nil {
		t.Fatal(err)
	}
	system, err := windows.GetSystemDirectory()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(filepath.Join(system, "WindowsPowerShell", "v1.0", "powershell.exe"), "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", harness, "-Source", script)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("PowerShell parser/interop validation: %v\n%s", err, output)
	}
}
