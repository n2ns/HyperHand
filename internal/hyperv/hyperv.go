// Package hyperv controls a Hyper-V VM from the host through WMI (root\virtualization\v2): screen, mouse, keyboard,
// state, and copying a file into the guest. Needs an elevated process. vm is the VM name; "" means the only running VM
// (or the only VM if none is running).
package hyperv

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/go-ole/go-ole"
	"github.com/go-ole/go-ole/oleutil"
	"golang.org/x/sys/windows"
)

// inputMu serializes mouse and keyboard input so concurrent tool calls do not interleave.
var inputMu sync.Mutex

type VM struct {
	Name  string `json:"name"`
	ID    string `json:"id"` // Msvm_ComputerSystem.Name, used for the Hyper-V socket
	State string `json:"state"`
}

func ListVMs() ([]VM, error) {
	var vms []VM
	err := withWMI(func(s *session) error {
		objs, err := s.vmObjects()
		for _, o := range objs {
			vms = append(vms, s.vmInfo(o))
		}
		return err
	})
	return vms, err
}

// Find resolves a VM name ("" = the only running VM, or the only VM if none is running).
func Find(vm string) (VM, error) {
	var v VM
	err := withWMI(func(s *session) error {
		o, err := s.find(vm)
		if err == nil {
			v = s.vmInfo(o)
		}
		return err
	})
	return v, err
}

// Screenshot returns the VM screen as PNG at its current resolution (pixel coordinates = mouse coordinates).
func Screenshot(vm string) (pngData []byte, width, height int, err error) {
	err = withWMI(func(s *session) error {
		o, err := s.find(vm)
		if err != nil {
			return err
		}
		heads, err := s.assoc(o, "Msvm_VideoHead")
		if err != nil {
			return err
		}
		if len(heads) == 0 {
			return errors.New("no video head (VM not running?)")
		}
		width, height = toInt(s.get(heads[0], "CurrentHorizontalResolution")), toInt(s.get(heads[0], "CurrentVerticalResolution"))
		settings, err := s.assoc(o, "Msvm_VirtualSystemSettingData")
		if err != nil {
			return err
		}
		var target *ole.IDispatch
		for _, sd := range settings {
			if fmt.Sprint(s.get(sd, "VirtualSystemType")) == "Microsoft:Hyper-V:System:Realized" {
				target = sd
			}
		}
		if target == nil {
			return errors.New("realized system settings not found")
		}
		svc, err := s.one("SELECT * FROM Msvm_VirtualSystemManagementService")
		if err != nil {
			return err
		}
		out, err := s.call(svc, "GetVirtualSystemThumbnailImage", "TargetSystem", s.path(target), "WidthPixels", int32(width), "HeightPixels", int32(height))
		if err != nil {
			return err
		}
		v, err := oleutil.GetProperty(out, "ImageData")
		if err != nil {
			return err
		}
		defer v.Clear()
		arr := v.ToArray()
		if arr == nil {
			return errors.New("no image data")
		}
		data, err := safeArrayBytes(arr)
		if err != nil {
			return err
		}
		img, err := rgb565ToRGBA(data, width, height)
		if err != nil {
			return err
		}
		var buf bytes.Buffer
		if err := png.Encode(&buf, img); err != nil {
			return err
		}
		pngData = buf.Bytes()
		return nil
	})
	return
}

// Click: button 1 left, 2 right, 3 middle.
func Click(vm string, x, y, button int, double bool) error {
	inputMu.Lock()
	defer inputMu.Unlock()
	return withDevice(vm, "Msvm_SyntheticMouse", func(s *session, m *ole.IDispatch) error {
		if err := s.move(m, x, y); err != nil {
			return err
		}
		time.Sleep(100 * time.Millisecond)
		for i := 0; i < 1+boolInt(double); i++ {
			if _, err := s.call(m, "ClickButton", "ButtonIndex", int32(button)); err != nil {
				return err
			}
		}
		return nil
	})
}

func Drag(vm string, x1, y1, x2, y2 int) error {
	inputMu.Lock()
	defer inputMu.Unlock()
	return withDevice(vm, "Msvm_SyntheticMouse", func(s *session, m *ole.IDispatch) (err error) {
		if err := s.move(m, x1, y1); err != nil {
			return err
		}
		time.Sleep(100 * time.Millisecond)
		if _, err := s.call(m, "SetButtonState", "ButtonIndex", int32(1), "IsDown", true); err != nil {
			return err
		}
		defer func() { // always release the button, even if a move failed
			if _, uerr := s.call(m, "SetButtonState", "ButtonIndex", int32(1), "IsDown", false); err == nil {
				err = uerr
			}
		}()
		const steps = 8
		for i := 1; i <= steps; i++ {
			time.Sleep(50 * time.Millisecond)
			if err := s.move(m, x1+(x2-x1)*i/steps, y1+(y2-y1)*i/steps); err != nil {
				return err
			}
		}
		time.Sleep(100 * time.Millisecond)
		return nil
	})
}

// Scroll: positive delta scrolls up.
func Scroll(vm string, x, y, delta int) error {
	inputMu.Lock()
	defer inputMu.Unlock()
	return withDevice(vm, "Msvm_SyntheticMouse", func(s *session, m *ole.IDispatch) error {
		if err := s.move(m, x, y); err != nil {
			return err
		}
		time.Sleep(100 * time.Millisecond)
		// delta is in wheel notches; Windows uses 120 units per notch.
		_, err := s.call(m, "SetScrollPosition", "ScrollPositionDelta", int32(delta*120))
		return err
	})
}

// TypeText types ASCII text.
func TypeText(vm, text string) error {
	for _, r := range text {
		if r > 127 {
			return fmt.Errorf("TypeText supports ASCII only (got %q)", r)
		}
	}
	inputMu.Lock()
	defer inputMu.Unlock()
	return withDevice(vm, "Msvm_Keyboard", func(s *session, k *ole.IDispatch) error {
		_, err := s.call(k, "TypeText", "AsciiText", text)
		return err
	})
}

// PressKeys presses a key or a combination such as "enter", "esc", "f2", "ctrl+v", "win+r", "alt+f4".
func PressKeys(vm, keys string) error {
	codes, err := parseKeys(keys)
	if err != nil {
		return err
	}
	inputMu.Lock()
	defer inputMu.Unlock()
	return withDevice(vm, "Msvm_Keyboard", func(s *session, k *ole.IDispatch) error {
		if len(codes) == 1 {
			_, err := s.call(k, "TypeKey", "KeyCode", int32(codes[0]))
			return err
		}
		var firstErr error
		pressed := 0
		for _, c := range codes {
			if _, err := s.call(k, "PressKey", "KeyCode", int32(c)); err != nil {
				firstErr = err
				break
			}
			pressed++
		}
		for i := pressed - 1; i >= 0; i-- {
			if _, err := s.call(k, "ReleaseKey", "KeyCode", int32(codes[i])); err != nil && firstErr == nil {
				firstErr = err
			}
		}
		return firstErr
	})
}

func Start(vm string) error { return requestState(vm, 2) }

func Stop(vm string) error { return requestState(vm, 3) }

// CopyToGuest copies a host file into the guest (enables the Guest Service Interface if needed; no guest password).
func CopyToGuest(vm, hostPath, guestPath string) error {
	_, err := vmScript(vm, `$gs=Get-VMIntegrationService -VM $vm | Where-Object { $_.Id -like '*6C09BB55-D683-4DA0-8931-C9BF705F6480' }
if ($gs -and -not $gs.Enabled) { Enable-VMIntegrationService -VMIntegrationService $gs; Start-Sleep -Seconds 3 }
Copy-VMFile -VM $vm -SourcePath `+psq(hostPath)+` -DestinationPath `+psq(guestPath)+` -CreateFullPath -FileSource Host -Force`)
	return err
}

type Checkpoint struct {
	Name         string `json:"Name"`
	CreationTime string `json:"CreationTime"`
}

func ListCheckpoints(vm string) ([]Checkpoint, error) {
	out, err := vmScript(vm, `$c=@(Get-VMSnapshot -VM $vm | Sort-Object CreationTime | Select-Object Name,@{n='CreationTime';e={$_.CreationTime.ToString('yyyy-MM-dd HH:mm:ss')}})
if ($c.Count) { ConvertTo-Json -InputObject $c -Compress }`)
	if err != nil {
		return nil, err
	}
	return parseCheckpoints(out)
}

func CreateCheckpoint(vm, name string) error {
	_, err := vmScript(vm, `Checkpoint-VM -VM $vm -SnapshotName `+psq(name))
	return err
}

// RestoreCheckpoint applies a checkpoint (exact, case-sensitive name; no wildcards). A production checkpoint leaves the
// VM off, a standard one leaves it saved.
func RestoreCheckpoint(vm, name string) error {
	_, err := vmScript(vm, `$c=Get-VMSnapshot -VM $vm | Where-Object { $_.Name -ceq `+psq(name)+` } | Select-Object -First 1
if (-not $c) { throw ('checkpoint not found: ' + `+psq(name)+`) }
$c | Restore-VMSnapshot -Confirm:$false`)
	return err
}

// parseCheckpoints reads ConvertTo-Json output: an array, a single object, or nothing.
func parseCheckpoints(out []byte) ([]Checkpoint, error) {
	out = bytes.TrimSpace(bytes.TrimPrefix(out, []byte("\xef\xbb\xbf")))
	if len(out) == 0 {
		return nil, nil
	}
	var cps []Checkpoint
	if out[0] == '{' {
		cps = make([]Checkpoint, 1)
		return cps, json.Unmarshal(out, &cps[0])
	}
	return cps, json.Unmarshal(out, &cps)
}

func psq(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// vmScript runs a PowerShell script with $vm set to the VM (looked up by Id) and returns its UTF-8 output.
func vmScript(vm, script string) ([]byte, error) {
	v, err := Find(vm)
	if err != nil {
		return nil, err
	}
	script = "[Console]::OutputEncoding=[Text.Encoding]::UTF8; $ErrorActionPreference='Stop'; $ProgressPreference='SilentlyContinue'; $vm=Get-VM -Id " + psq(v.ID) + "\n" + script
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW} // no console flash (the tray is a GUI exe)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("powershell: %v: %s", err, strings.TrimSpace(stderr.String()+"\n"+string(out)))
	}
	return out, nil
}

func requestState(vm string, state int32) error {
	return withWMI(func(s *session) error {
		o, err := s.find(vm)
		if err != nil {
			return err
		}
		_, err = s.call(o, "RequestStateChange", "RequestedState", state)
		return err
	})
}

// ---- WMI plumbing ----

type session struct {
	svc  *ole.IDispatch
	objs []*ole.IDispatch // released when the session ends
}

// withWMI runs f with a fresh WMI connection on a locked OS thread.
func withWMI(f func(*session) error) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := ole.CoInitializeEx(0, ole.COINIT_APARTMENTTHREADED); err != nil {
		if oe, ok := err.(*ole.OleError); !ok || oe.Code() != 1 { // S_FALSE = already initialized
			return err
		}
	}
	defer ole.CoUninitialize()
	s := &session{}
	defer func() {
		for i := len(s.objs) - 1; i >= 0; i-- {
			s.objs[i].Release()
		}
	}()
	unk, err := oleutil.CreateObject("WbemScripting.SWbemLocator")
	if err != nil {
		return err
	}
	defer unk.Release()
	loc, err := unk.QueryInterface(ole.IID_IDispatch)
	if err != nil {
		return err
	}
	s.keep(loc)
	if s.svc, err = s.dispatch(oleutil.CallMethod(loc, "ConnectServer", ".", `root\virtualization\v2`)); err != nil {
		return fmt.Errorf("connect root\\virtualization\\v2: %w", err)
	}
	return f(s)
}

func withDevice(vm, class string, f func(*session, *ole.IDispatch) error) error {
	return withWMI(func(s *session) error {
		o, err := s.find(vm)
		if err != nil {
			return err
		}
		devs, err := s.assoc(o, class)
		if err != nil {
			return err
		}
		if len(devs) == 0 {
			return fmt.Errorf("%s not found (VM not running?)", class)
		}
		return f(s, devs[0])
	})
}

func (s *session) keep(d *ole.IDispatch) *ole.IDispatch { s.objs = append(s.objs, d); return d }

func (s *session) dispatch(v *ole.VARIANT, err error) (*ole.IDispatch, error) {
	if err != nil {
		return nil, err
	}
	return s.keep(v.ToIDispatch()), nil
}

func (s *session) get(o *ole.IDispatch, name string) interface{} {
	v, err := oleutil.GetProperty(o, name)
	if err != nil {
		return nil
	}
	defer v.Clear()
	return v.Value()
}

func (s *session) path(o *ole.IDispatch) string {
	p, err := s.dispatch(oleutil.GetProperty(o, "Path_"))
	if err != nil {
		return ""
	}
	return fmt.Sprint(s.get(p, "Path"))
}

// list collects the items of an SWbemObjectSet.
func (s *session) list(set *ole.IDispatch, err error) ([]*ole.IDispatch, error) {
	if err != nil {
		return nil, err
	}
	var out []*ole.IDispatch
	err = oleutil.ForEach(set, func(v *ole.VARIANT) error {
		out = append(out, s.keep(v.ToIDispatch())) // ForEach does not clear v, so we own the reference
		return nil
	})
	return out, err
}

func (s *session) query(wql string) ([]*ole.IDispatch, error) {
	return s.list(s.dispatch(oleutil.CallMethod(s.svc, "ExecQuery", wql)))
}

func (s *session) one(wql string) (*ole.IDispatch, error) {
	r, err := s.query(wql)
	if err == nil && len(r) == 0 {
		err = fmt.Errorf("no result for %s", wql)
	}
	if err != nil {
		return nil, err
	}
	return r[0], nil
}

func (s *session) assoc(o *ole.IDispatch, resultClass string) ([]*ole.IDispatch, error) {
	return s.list(s.dispatch(oleutil.CallMethod(o, "Associators_", "", resultClass)))
}

// call invokes a WMI method with name/value argument pairs and returns the out-parameters object.
// ReturnValue 0 (done) and 4096 (job started) are success.
func (s *session) call(o *ole.IDispatch, method string, args ...interface{}) (*ole.IDispatch, error) {
	var in *ole.IDispatch
	if len(args) > 0 {
		methods, err := s.dispatch(oleutil.GetProperty(o, "Methods_"))
		if err != nil {
			return nil, err
		}
		m, err := s.dispatch(oleutil.CallMethod(methods, "Item", method))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", method, err)
		}
		params, err := s.dispatch(oleutil.GetProperty(m, "InParameters"))
		if err != nil {
			return nil, err
		}
		if in, err = s.dispatch(oleutil.CallMethod(params, "SpawnInstance_")); err != nil {
			return nil, err
		}
		for i := 0; i+1 < len(args); i += 2 {
			if _, err := oleutil.PutProperty(in, args[i].(string), args[i+1]); err != nil {
				return nil, fmt.Errorf("%s.%s: %w", method, args[i], err)
			}
		}
	}
	var callArgs []interface{}
	callArgs = append(callArgs, method)
	if in != nil {
		callArgs = append(callArgs, in)
	}
	out, err := s.dispatch(oleutil.CallMethod(o, "ExecMethod_", callArgs...))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", method, err)
	}
	if rv := toInt(s.get(out, "ReturnValue")); rv != 0 && rv != 4096 {
		return nil, fmt.Errorf("%s returned %d", method, rv)
	}
	return out, nil
}

// vmObjects returns the Msvm_ComputerSystem objects that are VMs (GUID Name), not the host.
func (s *session) vmObjects() ([]*ole.IDispatch, error) {
	all, err := s.query("SELECT * FROM Msvm_ComputerSystem")
	var vms []*ole.IDispatch
	for _, o := range all {
		if name := fmt.Sprint(s.get(o, "Name")); len(name) == 36 && strings.Count(name, "-") == 4 {
			vms = append(vms, o)
		}
	}
	return vms, err
}

func (s *session) vmInfo(o *ole.IDispatch) VM {
	return VM{Name: fmt.Sprint(s.get(o, "ElementName")), ID: fmt.Sprint(s.get(o, "Name")), State: stateName(toInt(s.get(o, "EnabledState")))}
}

func (s *session) find(vm string) (*ole.IDispatch, error) {
	objs, err := s.vmObjects()
	if err != nil {
		return nil, err
	}
	var hits []*ole.IDispatch
	for _, o := range objs {
		if vm == "" && toInt(s.get(o, "EnabledState")) == 2 || vm != "" && strings.EqualFold(fmt.Sprint(s.get(o, "ElementName")), vm) {
			hits = append(hits, o)
		}
	}
	if vm == "" && len(hits) == 0 && len(objs) == 1 { // nothing running but only one VM: use it (e.g. to start it)
		return objs[0], nil
	}
	switch {
	case len(hits) == 1:
		return hits[0], nil
	case vm == "" && len(hits) == 0:
		return nil, errors.New("no running VM")
	case vm == "":
		return nil, errors.New("several VMs are running; specify one by name")
	case len(hits) == 0:
		return nil, fmt.Errorf("VM %q not found", vm)
	default:
		return nil, fmt.Errorf("several VMs are named %q", vm)
	}
}

func (s *session) move(m *ole.IDispatch, x, y int) error {
	_, err := s.call(m, "SetAbsolutePosition", "HorizontalPosition", int32(x), "VerticalPosition", int32(y))
	return err
}

// ---- pure helpers ----

func stateName(st int) string {
	switch st {
	case 2:
		return "Running"
	case 3:
		return "Off"
	case 6:
		return "Saved"
	case 9:
		return "Paused"
	}
	return strconv.Itoa(st)
}

func toInt(v interface{}) int {
	switch n := v.(type) {
	case int8, int16, int32, int64, int, uint8, uint16, uint32, uint64, uint:
		i, _ := strconv.Atoi(fmt.Sprint(n))
		return i
	case string:
		i, _ := strconv.Atoi(n)
		return i
	}
	return 0
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

var (
	oleaut32                  = windows.NewLazySystemDLL("oleaut32.dll")
	procSafeArrayAccessData   = oleaut32.NewProc("SafeArrayAccessData")
	procSafeArrayUnaccessData = oleaut32.NewProc("SafeArrayUnaccessData")
	procSafeArrayGetElemsize  = oleaut32.NewProc("SafeArrayGetElemsize")
)

// safeArrayBytes reads WMI's uint8[] SAFEARRAY. VT_UI1 and VT_VARIANT arrays are read straight from the array data
// (ToValueArray builds millions of interface values); anything else falls back to ToValueArray.
// ToByteArray misreads WMI's uint8 array (every byte came back as 0x11, the VT_UI1 tag).
func safeArrayBytes(arr *ole.SafeArrayConversion) ([]byte, error) {
	vt, err := arr.GetType()
	if err != nil {
		return nil, err
	}
	n, err := arr.TotalElements(0)
	if err != nil {
		return nil, err
	}
	if (ole.VT(vt) == ole.VT_UI1 || ole.VT(vt) == ole.VT_VARIANT) && n > 0 {
		// Element size: 1, or 16/24 for a VARIANT (32/64-bit). Called directly: go-ole's GetSize returns the size cast
		// to a pointer (dereferencing it crashes).
		es, _, _ := procSafeArrayGetElemsize.Call(uintptr(unsafe.Pointer(arr.Array)))
		if es == 0 {
			return nil, errors.New("SafeArrayGetElemsize returned 0")
		}
		size := &[]uint32{uint32(es)}[0]
		var p unsafe.Pointer
		if hr, _, _ := procSafeArrayAccessData.Call(uintptr(unsafe.Pointer(arr.Array)), uintptr(unsafe.Pointer(&p))); hr != 0 {
			return nil, fmt.Errorf("SafeArrayAccessData: 0x%08x", uint32(hr))
		}
		defer procSafeArrayUnaccessData.Call(uintptr(unsafe.Pointer(arr.Array)))
		raw := unsafe.Slice((*byte)(p), int(n)*int(*size))
		if ole.VT(vt) == ole.VT_UI1 {
			return bytes.Clone(raw), nil
		}
		return variantBytes(raw, int(*size)), nil
	}
	vals := arr.ToValueArray()
	data := make([]byte, len(vals))
	for i, x := range vals {
		if b, ok := x.(uint8); ok {
			data[i] = b
		} else {
			data[i] = byte(toInt(x))
		}
	}
	return data, nil
}

// variantBytes returns the value byte (offset 8) of each elemSize-byte VARIANT in a raw VARIANT array.
func variantBytes(raw []byte, elemSize int) []byte {
	out := make([]byte, len(raw)/elemSize)
	for i := range out {
		out[i] = raw[i*elemSize+8]
	}
	return out
}

// rgb565ToRGBA converts row-major little-endian RGB565 pixels to an RGBA image.
func rgb565ToRGBA(data []byte, w, h int) (*image.RGBA, error) {
	if w <= 0 || h <= 0 || len(data) < w*h*2 {
		return nil, fmt.Errorf("image data has %d bytes, want %d for %dx%d", len(data), w*h*2, w, h)
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < w*h; i++ {
		p := uint16(data[2*i]) | uint16(data[2*i+1])<<8
		r, g, b := uint8(p>>11), uint8(p>>5&0x3f), uint8(p&0x1f)
		img.SetRGBA(i%w, i/w, color.RGBA{r<<3 | r>>2, g<<2 | g>>4, b<<3 | b>>2, 255})
	}
	return img, nil
}

var keyNames = map[string]int{
	"ctrl": 0x11, "control": 0x11, "shift": 0x10, "alt": 0x12, "win": 0x5B,
	"enter": 0x0D, "return": 0x0D, "esc": 0x1B, "escape": 0x1B, "tab": 0x09, "space": 0x20,
	"backspace": 0x08, "delete": 0x2E, "del": 0x2E, "insert": 0x2D, "home": 0x24, "end": 0x23,
	"pageup": 0x21, "pagedown": 0x22, "up": 0x26, "down": 0x28, "left": 0x25, "right": 0x27,
	";": 0xBA, "=": 0xBB, "plus": 0xBB, ",": 0xBC, "-": 0xBD, ".": 0xBE, "/": 0xBF, "`": 0xC0,
	"[": 0xDB, "\\": 0xDC, "]": 0xDD, "'": 0xDE,
}

// parseKeys turns "ctrl+shift+esc" into Windows virtual-key codes.
func parseKeys(keys string) ([]int, error) {
	var codes []int
	for _, k := range strings.Split(strings.ToLower(strings.TrimSpace(keys)), "+") {
		k = strings.TrimSpace(k)
		code, ok := keyNames[k]
		switch {
		case ok:
		case len(k) == 1 && (k[0] >= 'a' && k[0] <= 'z' || k[0] >= '0' && k[0] <= '9'):
			code = int(strings.ToUpper(k)[0])
		case len(k) >= 2 && k[0] == 'f':
			if n, err := strconv.Atoi(k[1:]); err == nil && n >= 1 && n <= 12 {
				code = 0x6F + n
			}
		}
		if code == 0 {
			return nil, fmt.Errorf("unknown key %q in %q", k, keys)
		}
		codes = append(codes, code)
	}
	return codes, nil
}
