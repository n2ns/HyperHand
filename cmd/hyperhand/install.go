package main

import "golang.org/x/sys/windows"

// install is the only elevation boundary; the installed tray remains a normal
// interactive-user application and the broker uses a virtual service account.
func install() error {
	completed, err := runSetup("install")
	if err == nil && completed {
		msgBox("HyperHand installed. The dedicated broker service starts automatically; the tray runs at logon with normal user permissions.\nMCP: http://127.0.0.1:8770/mcp", windows.MB_ICONINFORMATION)
	}
	return err
}
