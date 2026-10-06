package main

import (
	"path/filepath"
	"testing"

	"hyperhand/internal/hyperv"
)

func TestCheckConsoleVM(t *testing.T) {
	vms := []hyperv.VM{{Name: "Win10"}, {Name: "Test VM"}}
	for _, ok := range []string{"Win10", "Test VM"} {
		if err := checkConsoleVM(ok, vms); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	// Only exact names of existing VMs: the name becomes a quoted vmconnect.exe argument.
	for _, bad := range []string{"", "win10", "Missing", `Win10" /edit`, "Win10\r\n"} {
		if err := checkConsoleVM(bad, vms); err == nil {
			t.Errorf("%q: accepted", bad)
		}
	}
	for _, name := range []string{`a"b`, `Test\`} {
		if err := checkConsoleVM(name, []hyperv.VM{{Name: name}}); err == nil {
			t.Errorf("%q must be refused even when it exists", name)
		}
	}
}

func TestConsoleTitleVM(t *testing.T) {
	for _, c := range []struct {
		title, want string
		ok          bool
	}{
		{"localhost 上的 Win10 - 虚拟机连接", "Win10", true},
		{"Win10 on localhost - Virtual Machine Connection", "Win10", true},
		{"Win10 Test on localhost - Virtual Machine Connection", "Win10 Test", true},
		{"localhost 上的 Win10 Test - 虚拟机连接", "Win10 Test", true},
		{"localhost 上的 A - B - 虚拟机连接", "A - B", true}, // the product name follows the last " - "
		{"localhost on localhost - Virtual Machine Connection", "localhost", true},
		{"虚拟机连接", "", false},
		{"Win10 - Virtual Machine Connection", "", false},
		{"localhost - Virtual Machine Connection", "", false},
	} {
		if got, ok := consoleTitleVM(c.title); got != c.want || ok != c.ok {
			t.Errorf("%q: %q %v, want %q %v", c.title, got, ok, c.want, c.ok)
		}
	}
}

func TestConsoleSettingsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "settings.json")
	s := loadConsoleSettings(path) // missing file: nothing enabled
	if s.onStart("Win10") {
		t.Fatal("enabled without a settings file")
	}
	if err := s.setOnStart("Win10", true); err != nil {
		t.Fatal(err)
	}
	if err := s.setOnStart("Other", true); err != nil {
		t.Fatal(err)
	}
	if err := s.setOnStart("Other", false); err != nil {
		t.Fatal(err)
	}
	r := loadConsoleSettings(path)
	if !r.onStart("Win10") || r.onStart("Other") {
		t.Errorf("reloaded: %v", r.vms)
	}
}
