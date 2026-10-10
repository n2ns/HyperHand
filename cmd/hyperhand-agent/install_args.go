package main

import (
	"encoding/hex"
	"fmt"
)

// The installation ID is transient: do not put it in the logon Run key.
func installLaunchArgs(id string) []string {
	if id == "" {
		return nil
	}
	return []string{"--install-id", id}
}

func parseInstallID(args []string) (string, error) {
	if len(args) == 0 {
		return "", nil
	}
	if len(args) == 2 && args[0] == "--install-id" && len(args[1]) == 32 {
		if _, err := hex.DecodeString(args[1]); err == nil {
			return args[1], nil
		}
	}
	return "", fmt.Errorf("expected --install-id followed by 32 hexadecimal characters")
}
