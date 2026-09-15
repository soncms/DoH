//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"
	"unsafe"

	"github.com/lxn/walk"
	"github.com/lxn/win"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
)

// Run explicitly on an interactive Windows desktop. This exercises our actual
// widgets/layout, without changing any Windows display or service settings.
func TestNativeLayout(t *testing.T) {
	if os.Getenv("SONDNS_UI_TEST") != "1" {
		t.Skip("set SONDNS_UI_TEST=1 on a Windows desktop")
	}
	mw, err := walk.NewMainWindow()
	if err != nil {
		t.Fatal(err)
	}
	defer mw.Dispose()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	g := &guiState{mw: mw, ctx: ctx, cancel: cancel, config: DefaultConfig()}
	if err = g.build(); err != nil {
		t.Fatal(err)
	}
	g.radioNextDNS.SetChecked(true)
	g.applyServiceSnapshot(serviceSnapshot{true, svc.Running, windows.SERVICE_DEMAND_START}, nil)
	g.applyServiceSnapshot(serviceSnapshot{}, errors.New("access denied"))
	if g.serviceStatus.Text() != "status unavailable" || g.serviceButton.Enabled() {
		t.Error("SCM query error was treated as absent service")
	}
	g.applyServiceSnapshot(serviceSnapshot{true, svc.Running, windows.SERVICE_AUTO_START}, nil)
	if !g.autoStart.Checked() {
		t.Error("checkbox does not reflect SCM")
	}
	g.autoStart.SetChecked(false)
	g.autoDirty = true
	g.applyServiceSnapshot(serviceSnapshot{true, svc.Running, windows.SERVICE_AUTO_START}, nil)
	if g.autoStart.Checked() {
		t.Error("poll erased unsaved startup choice")
	}
	edited, err := g.editedConfig()
	if err != nil {
		t.Fatal(err)
	}
	if len(edited.Upstreams) != 1 || edited.Upstreams[0].URL != URLNextDNS || edited.ServiceAutoStart {
		t.Errorf("edited config = %+v, want NextDNS upstream", edited.Upstreams)
	}
	for i := 0; i < 8; i++ {
		g.logModel.rows = append(g.logModel.rows, DNSRequestEntry{Domain: fmt.Sprintf("host-%d.example.", i), Type: "A", Result: "NOERROR"})
	}
	fixture := append([]DNSRequestEntry(nil), g.logModel.rows...)
	g.applyLogRows(fixture, 1, "inst-1")
	g.applyLogRows(nil, 3, "inst-1")
	g.applyLogRows(fixture, 2, "inst-1")
	if g.logModel.RowCount() != 0 {
		t.Error("queued pre-clear batch restored old rows")
	}
	g.applyLogRows(fixture, 4, "inst-1")
	// Test new instance after restart resets and applies despite lower version
	g.applyLogRows(nil, 1, "inst-2")
	if g.logModel.RowCount() != 0 {
		t.Error("new instance clear should apply")
	}
	g.applyLogRows(fixture, 2, "inst-2")
	if g.logModel.RowCount() != 8 {
		t.Error("new instance rows should apply")
	}
	if val := g.logModel.Value(0, 1); val != "host-0.example" {
		t.Errorf("domain value = %q, want %q", val, "host-0.example")
	}
	g.logModel.PublishRowsReset()
	mw.SetPersistent(false)
	check := func(expected walk.Size) {
		if actual := mw.Size(); actual != expected {
			t.Errorf("window size = %+v, want %+v", actual, expected)
		}
		t.Logf("window=%+v client=%+v table=%+v", mw.Size(), mw.ClientBounds(), g.requestLog.Bounds())
		var visit func(walk.Container)
		visit = func(parent walk.Container) {
			bounds := parent.ClientBounds()
			for i := 0; i < parent.Children().Len(); i++ {
				child := parent.Children().At(i)
				b := child.Bounds()
				if b.X < 0 || b.Y < 0 || b.X+b.Width > bounds.Width+1 || b.Y+b.Height > bounds.Height+1 {
					t.Errorf("%T outside parent: %+v in %+v", child, b, bounds)
				}
				if sub, ok := child.(walk.Container); ok {
					visit(sub)
				}
			}
		}
		visit(mw)
		g.resizeLogColumns()
		if !g.requestLog.ItemVisible(0) || !g.requestLog.ItemVisible(7) {
			for h := win.GetWindow(g.requestLog.Handle(), win.GW_CHILD); h != 0; h = win.GetWindow(h, win.GW_HWNDNEXT) {
				var cr, first, last win.RECT
				win.GetClientRect(h, &cr)
				win.SendMessage(h, win.LVM_GETITEMRECT, 0, uintptr(unsafe.Pointer(&first)))
				win.SendMessage(h, win.LVM_GETITEMRECT, 7, uintptr(unsafe.Pointer(&last)))
				t.Logf("list client=%+v first=%+v last=%+v page=%d top=%d", cr, first, last, win.SendMessage(h, win.LVM_GETCOUNTPERPAGE, 0, 0), win.SendMessage(h, win.LVM_GETTOPINDEX, 0, 0))
			}
			for y := 0; y < g.requestLog.HeightPixels(); y += 20 {
				t.Logf("row at y=%d: %d", y, g.requestLog.IndexAt(50, y))
			}
			t.Error("compact list must fit eight rows")
		}

		if g.autoStart.Bounds().Width < g.autoStart.SizeHint().Width {
			t.Error("autostart checkbox text clipped")
		}
	}
	mw.Show()
	mw.Starting().Attach(func() {
		time.AfterFunc(400*time.Millisecond, func() {
			mw.Synchronize(func() {
				check(compactWindowSize)
				if mw.Title() != "SonDNS" {
					t.Errorf("title=%q", mw.Title())
				}
				if !g.requestLog.ColumnsSizable() {
					t.Error("columns must be sizable")
				}
				_ = mw.Close()
			})
		})
	})
	mw.Run()
}
