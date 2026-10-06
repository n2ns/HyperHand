package main

import (
	"unsafe"

	"github.com/rodrigocfd/windigo/co"
	"github.com/rodrigocfd/windigo/ui"
	"github.com/rodrigocfd/windigo/win"
	"golang.org/x/sys/windows"
)

// Colours of the settings window, close to the Windows 11 settings pages.
var (
	colorText    = win.RGB(26, 26, 26)
	colorLabel   = win.RGB(96, 96, 96)
	colorGood    = win.RGB(15, 123, 15)
	colorBad     = win.RGB(196, 43, 28)
	colorWarn    = win.RGB(157, 93, 0)
	colorReadBox = win.RGB(243, 243, 243) // background of read-only fields
)

var (
	gdi32              = windows.NewLazySystemDLL("gdi32.dll")
	pCreateSolidBrush  = gdi32.NewProc("CreateSolidBrush")
	uxtheme            = windows.NewLazySystemDLL("uxtheme.dll")
	pSetWindowTheme    = uxtheme.NewProc("SetWindowTheme")
	explorerThemeName  = windows.StringToUTF16Ptr("Explorer")
	settingsWhiteBrush = func() win.HBRUSH { b, _ := win.GetSysColorBrush(co.COLOR_WINDOW); return b }
)

// windowStyle holds the fonts, brushes and per-control text colours of one settings window.
type windowStyle struct {
	title, section win.HFONT
	readBox        win.HBRUSH
	colors         map[win.HWND]win.COLORREF
	readOnly       map[win.HWND]bool
}

func newWindowStyle() *windowStyle {
	s := &windowStyle{colors: map[win.HWND]win.COLORREF{}, readOnly: map[win.HWND]bool{}}
	var ncm win.NONCLIENTMETRICS
	ncm.SetCbSize()
	if win.SystemParametersInfo(co.SPI_GETNONCLIENTMETRICS, uint32(unsafe.Sizeof(ncm)), unsafe.Pointer(&ncm), co.SPIF(0)) == nil {
		lf := ncm.LfMessageFont
		lf.Height = lf.Height * 16 / 10
		lf.Weight = co.FW_SEMIBOLD
		s.title, _ = win.CreateFontIndirect(&lf)
		lf = ncm.LfMessageFont
		lf.Height = lf.Height * 12 / 10
		lf.Weight = co.FW_SEMIBOLD
		s.section, _ = win.CreateFontIndirect(&lf)
	}
	b, _, _ := pCreateSolidBrush.Call(uintptr(colorReadBox))
	s.readBox = win.HBRUSH(b)
	return s
}

func (s *windowStyle) free() {
	for _, f := range []win.HFONT{s.title, s.section} {
		if f != 0 {
			f.DeleteObject()
		}
	}
	if s.readBox != 0 {
		s.readBox.DeleteObject()
	}
}

func setFont(h win.HWND, f win.HFONT) {
	if f != 0 {
		h.SendMessage(co.WM_SETFONT, win.WPARAM(f), 1)
	}
}

// explorerTheme gives a list view the Explorer look (hover and selection highlight).
func explorerTheme(h win.HWND) {
	pSetWindowTheme.Call(uintptr(h), uintptr(unsafe.Pointer(explorerThemeName)), 0)
}

// ctlColor paints static text, check boxes and read-only edits on the window's white background.
func (s *windowStyle) ctlColor(p ui.WmCtlColor) win.HBRUSH {
	hdc, h := p.Hdc(), p.HwndControl()
	color, ok := s.colors[h]
	if !ok {
		color = colorText
	}
	hdc.SetTextColor(color)
	if s.readOnly[h] {
		hdc.SetBkColor(colorReadBox)
		return s.readBox
	}
	hdc.SetBkMode(co.BKMODE_TRANSPARENT)
	return settingsWhiteBrush()
}
