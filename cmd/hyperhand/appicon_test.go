package main

import "testing"

// The application icon is in the executable's resources at both sizes the tray and settings window load.
func TestAppIcon(t *testing.T) {
	for _, lims := range []int{limSmall, limLarge} {
		h, err := appIcon(lims)
		if err != nil {
			t.Fatal(err)
		}
		h.DestroyIcon()
	}
}
