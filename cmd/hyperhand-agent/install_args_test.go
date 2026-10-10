package main

import (
	"reflect"
	"testing"
)

func TestInstallIDArguments(t *testing.T) {
	const id = "0123456789abcdef0123456789abcdef"
	for _, value := range []string{"", id} {
		got, err := parseInstallID(installLaunchArgs(value))
		if err != nil || got != value {
			t.Fatalf("installation %q: %q, %v", value, got, err)
		}
	}
	if !reflect.DeepEqual(installLaunchArgs(id), []string{"--install-id", id}) {
		t.Fatal("installer did not forward its ID to the launched process")
	}
	for _, args := range [][]string{{"--install-id"}, {"--install-id", ""}, {"--install-id", "short"}, {"--install-id", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"}, {"--install-id", id, "extra"}, {"other", id}} {
		if _, err := parseInstallID(args); err == nil {
			t.Errorf("accepted invalid arguments: %q", args)
		}
	}
}
