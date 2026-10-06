package main

import (
	"fmt"
	"unsafe"

	"github.com/rodrigocfd/windigo/win"
	"golang.org/x/sys/windows"
)

var (
	comctl32           = windows.NewLazySystemDLL("comctl32.dll")
	pLoadIconMetric    = comctl32.NewProc("LoadIconMetric")
	limSmall, limLarge = 0, 1 // LIM_SMALL (SM_CXSMICON), LIM_LARGE (SM_CXICON)
)

// appIcon loads the application icon, resource 1 (winres/winres.json), at the small or large system icon size,
// picking or scaling the closest image in it. The caller destroys it.
func appIcon(lims int) (win.HICON, error) {
	inst, err := win.GetModuleHandle("")
	if err != nil {
		return 0, err
	}
	var h win.HICON
	const iconID = 1 // MAKEINTRESOURCE(1)
	if hr, _, _ := pLoadIconMetric.Call(uintptr(inst), iconID, uintptr(lims), uintptr(unsafe.Pointer(&h))); hr != 0 {
		return 0, fmt.Errorf("LoadIconMetric: HRESULT %#08x", uint32(hr))
	}
	return h, nil
}
