package host

import (
	"reflect"
	"testing"

	"hyperhand/internal/proto"
)

func TestRenderControl(t *testing.T) {
	for _, tt := range []struct {
		name string
		node proto.ControlInfo
		want string
	}{
		{"root", proto.ControlInfo{Index: 0, Parent: -1, ControlType: 50032, Name: "AutoCAD 2015 - Drawing1.dwg", Enabled: true, Rect: proto.Rect{Right: 1920, Bottom: 1040}},
			`[0] Window "AutoCAD 2015 - Drawing1.dwg" (0,0 1920x1040)`},
		{"focused edit with id and value", proto.ControlInfo{Index: 12, Depth: 1, ControlType: 50004, AutomationID: "cmdline", Enabled: true, Focused: true, HasValue: true, Value: "LINE", Rect: proto.Rect{Top: 980, Right: 1920, Bottom: 1040}},
			`  [12] Edit "" id=cmdline (0,980 1920x60) focused value="LINE"`},
		{"disabled offscreen button at depth 2", proto.ControlInfo{Index: 3, Depth: 2, ControlType: 50000, Name: `Say "hi"`, Offscreen: true, Rect: proto.Rect{Left: 12, Top: 80, Right: 60, Bottom: 128}},
			`    [3] Button "Say \"hi\"" (12,80 48x48) disabled offscreen`},
		{"empty value is still shown", proto.ControlInfo{Index: 1, ControlType: 50004, Enabled: true, HasValue: true},
			`[1] Edit "" (0,0 0x0) value=""`},
		{"unknown type and id with spaces", proto.ControlInfo{Index: 2, ControlType: 1, AutomationID: "a b", Enabled: true},
			`[2] Custom "" id="a b" (0,0 0x0)`},
	} {
		if got := renderControl(tt.node); got != tt.want {
			t.Errorf("%s:\n got %s\nwant %s", tt.name, got, tt.want)
		}
	}
	nodes := []proto.ControlInfo{{Index: 0, Parent: -1, ControlType: 50032, Enabled: true}, {Index: 1, Depth: 1, ControlType: 50033, Enabled: true}}
	if got, want := renderControls(nodes), "[0] Window \"\" (0,0 0x0)\n  [1] Pane \"\" (0,0 0x0)\n"; got != want {
		t.Errorf("renderControls:\n got %q\nwant %q", got, want)
	}
	if renderControls(nil) != "" {
		t.Error("empty tree renders text")
	}
}

func diffTree() []proto.ControlInfo {
	return []proto.ControlInfo{
		{Index: 0, Parent: -1, Depth: 0, ControlType: 50032, Name: "Options", Enabled: true, RuntimeID: "42.1", Rect: proto.Rect{Right: 500, Bottom: 400}},
		{Index: 1, Parent: 0, Depth: 1, ControlType: 50004, Name: "", AutomationID: "cmdline", Enabled: true, RuntimeID: "42.2", HasValue: true, Value: "", Rect: proto.Rect{Top: 380, Right: 500, Bottom: 400}},
		{Index: 2, Parent: 0, Depth: 1, ControlType: 50000, Name: "OK", Enabled: true, RuntimeID: "42.3", Rect: proto.Rect{Left: 10, Top: 10, Right: 50, Bottom: 30}},
		{Index: 3, Parent: 0, Depth: 1, ControlType: 50000, Name: "Cancel", Enabled: true, RuntimeID: "42.4", Rect: proto.Rect{Left: 60, Top: 10, Right: 100, Bottom: 30}},
	}
}

func TestDiffControlsRuntimeIDs(t *testing.T) {
	old := diffTree()
	cur := diffTree()
	cur[1].Value, cur[1].Focused = "LINE", true // changed
	cur = append(cur[:3], proto.ControlInfo{Index: 3, Parent: 0, Depth: 1, ControlType: 50020, Name: "Specify first point:", Enabled: true, RuntimeID: "42.9", Rect: proto.Rect{Top: 350, Right: 500, Bottom: 380}})
	// Cancel (old index 3) vanished; OK keeps its runtime ID even though its index is unchanged.
	d := diffControls(old, cur)
	want := &controlsDiff{
		Added:   []string{`  [3] Text "Specify first point:" (0,350 500x30)`},
		Removed: []int{3},
		Changed: []string{`  [1] Edit "" id=cmdline (0,380 500x20) focused value="LINE"`},
	}
	if !reflect.DeepEqual(d, want) {
		t.Errorf("diff = %+v, want %+v", d, want)
	}
	if d := diffControls(old, diffTree()); len(d.Added)+len(d.Removed)+len(d.Changed) != 0 || d.Added == nil || d.Removed == nil || d.Changed == nil {
		t.Errorf("identical trees: %+v", d)
	}
}

func TestDiffControlsFallbackPath(t *testing.T) {
	old := diffTree()
	cur := diffTree()
	for i := range old {
		old[i].RuntimeID, cur[i].RuntimeID = "", ""
	}
	// Re-indexed after an insertion at the top: nodes match by parent path, type and name, not by index.
	cur = append([]proto.ControlInfo{cur[0], {Index: 1, Parent: 0, Depth: 1, ControlType: 50021, Name: "Ribbon", Enabled: true}}, cur[1:]...)
	for i := 2; i < len(cur); i++ {
		cur[i].Index = i
	}
	cur[3].Enabled = false // OK, now at index 3
	d := diffControls(old, cur)
	want := &controlsDiff{
		Added:   []string{`  [1] ToolBar "Ribbon" (0,0 0x0)`},
		Removed: []int{},
		Changed: []string{`  [3] Button "OK" (10,10 40x20) disabled`},
	}
	if !reflect.DeepEqual(d, want) {
		t.Errorf("diff = %+v, want %+v", d, want)
	}
	// A renamed node without a runtime ID cannot be matched: it is removed and added.
	cur = diffTree()
	for i := range cur {
		cur[i].RuntimeID = ""
	}
	cur[3].Name = "Close"
	d = diffControls(old, cur)
	if !reflect.DeepEqual(d.Removed, []int{3}) || len(d.Added) != 1 || len(d.Changed) != 0 {
		t.Errorf("renamed: %+v", d)
	}
}
