//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"runtime"
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
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
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
	wantColumns := []string{"Time", "Domain", "Type", "Result", "Server", "Answer IP", "Latency"}
	if got := g.requestLog.Columns().Len(); got != len(wantColumns) {
		t.Fatalf("column count = %d, want %d", got, len(wantColumns))
	}
	for i, title := range wantColumns {
		if got := g.requestLog.Columns().At(i).Title(); got != title {
			t.Errorf("column %d = %q, want %q", i, got, title)
		}
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
	fixture := make([]DNSRequestEntry, 8)
	for i := 0; i < 8; i++ {
		fixture[i] = DNSRequestEntry{Domain: fmt.Sprintf("host-%d.example.", i), Type: "A", Result: "NOERROR"}
	}
	// Simulate: service has 8 entries (version=8). GUI starts fresh (version=0).
	g.logModel.rows = nil
	g.logVersion = 0
	g.instanceID = ""
	g.applyLogRows(fixture, 8, "inst-1")
	g.applyLogRows(nil, 10, "inst-1")
	g.applyLogRows(fixture, 9, "inst-1")
	if g.logModel.RowCount() != 0 {
		t.Error("queued pre-clear batch restored old rows")
	}
	g.applyLogRows(fixture, 11, "inst-1")
	// Test new instance after restart resets and applies despite lower version
	g.logModel.rows = nil
	g.logVersion = 0
	g.instanceID = ""
	g.applyLogRows(nil, 1, "inst-2")
	if g.logModel.RowCount() != 0 {
		t.Error("new instance clear should apply")
	}
	g.logModel.rows = nil
	g.logVersion = 0
	g.applyLogRows(fixture, 8, "inst-2")
	if g.logModel.RowCount() != 8 {
		t.Error("new instance rows should apply")
	}
	if val := g.logModel.Value(0, 1); val != "host-7.example" {
		t.Errorf("domain value = %q, want %q", val, "host-7.example")
	}
	if val := g.logModel.Value(7, 1); val != "host-0.example" {
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
		columnWidth := 0
		for i := 0; i < g.requestLog.Columns().Len(); i++ {
			columnWidth += g.requestLog.Columns().At(i).Width()
		}
		if columnWidth > g.requestLog.ClientBounds().Width {
			t.Errorf("columns overflow: width=%d, table=%d", columnWidth, g.requestLog.ClientBounds().Width)
		}
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

func TestNativeLogIncrementalAtCapacity(t *testing.T) {
	if os.Getenv("SONDNS_UI_TEST") != "1" {
		t.Skip("set SONDNS_UI_TEST=1 on a Windows desktop")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
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

	rows := make([]DNSRequestEntry, maxDNSLogRows)
	for i := range rows {
		rows[i] = DNSRequestEntry{Domain: fmt.Sprintf("old-%03d.example.", i), Type: "A", Result: "NOERROR"}
	}
	g.applyLogRows(rows, maxDNSLogRows, "capacity-test")

	next := append([]DNSRequestEntry(nil), rows[1:]...)
	next = append(next, DNSRequestEntry{Domain: "new.example.", Type: "A", Result: "NOERROR"})
	g.applyLogRows(next, maxDNSLogRows+1, "capacity-test")

	if got := g.logModel.RowCount(); got != maxDNSLogRows {
		t.Fatalf("row count = %d, want %d", got, maxDNSLogRows)
	}
	if got := g.logModel.Value(0, 1); got != "new.example" {
		t.Errorf("first row = %q, want new request", got)
	}
	if got := g.logModel.Value(maxDNSLogRows-1, 1); got != "old-001.example" {
		t.Errorf("last row = %q, want oldest retained request", got)
	}
	assertNativeLogCount(t, g, maxDNSLogRows)

	// Exercise repeated rolling snapshots, not only the Go slice's row count.
	ring := NewDNSRequestRing()
	for batch := 0; batch < 30; batch++ {
		for i := 0; i < 17; i++ {
			ring.Add(DNSRequestEntry{Domain: fmt.Sprintf("batch-%d-%d.example", batch, i), Type: "A"})
		}
		snapshot, version := ring.Snapshot()
		g.applyLogRows(snapshot, version, "rolling-service")
		for i := range snapshot {
			if !reflect.DeepEqual(g.logModel.rows[len(snapshot)-1-i], snapshot[i]) {
				t.Fatalf("batch %d: model differs from service snapshot at %d", batch, i)
			}
		}
		assertNativeLogCount(t, g, len(snapshot))
	}

	// A foreign clear plus new rows must not retain the old service history.
	ring.Clear()
	ring.Add(DNSRequestEntry{Domain: "after-clear.example"})
	snapshot, version := ring.Snapshot()
	g.applyLogRows(snapshot, version, "rolling-service")
	if len(g.logModel.rows) != 1 || g.logModel.rows[0].Domain != "after-clear.example" {
		t.Fatal("external clear retained stale rows")
	}
	// A delayed empty callback must not erase newer requests.
	g.applyLogRows(nil, version-1, "rolling-service")
	if len(g.logModel.rows) != 1 {
		t.Fatal("stale empty response erased current rows")
	}
	// Clear invalidates every response captured before it, including another
	// instance. Unchanged responses do not mean an empty log.
	g.logGeneration++
	g.acceptLogResponse(dnsIPCResponse{Entries: rows, Version: 999, InstanceID: "old-service"}, nil, g.logGeneration-1)
	g.acceptLogResponse(dnsIPCResponse{Unchanged: true}, nil, g.logGeneration)
	if len(g.logModel.rows) != 1 || g.instanceID != "rolling-service" {
		t.Fatal("stale or unchanged response changed log")
	}
	g.acceptLogResponse(dnsIPCResponse{Version: 0, InstanceID: "restarted-service"}, nil, g.logGeneration)
	if len(g.logModel.rows) != 0 || g.instanceID != "restarted-service" {
		t.Fatal("empty restart snapshot did not reset log")
	}
	assertNativeLogCount(t, g, 0)
}

func assertNativeLogCount(t *testing.T, g *guiState, want int) {
	t.Helper()
	const lvmGetItemCount = 0x1004
	for h := win.GetWindow(g.requestLog.Handle(), win.GW_CHILD); h != 0; h = win.GetWindow(h, win.GW_HWNDNEXT) {
		if got := int(win.SendMessage(h, lvmGetItemCount, 0, 0)); got != want {
			t.Fatalf("native list count=%d, model count=%d, want=%d", got, g.logModel.RowCount(), want)
		}
	}
}

func TestNativeLogViewport(t *testing.T) {
	if os.Getenv("SONDNS_UI_TEST") != "1" {
		t.Skip("set SONDNS_UI_TEST=1 on a Windows desktop")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
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
	ring := NewDNSRequestRing()
	add := func(n int) {
		for i := 0; i < n; i++ {
			ring.Add(DNSRequestEntry{Domain: fmt.Sprintf("request-%d.example", ring.Version())})
		}
		rows, version := ring.Snapshot()
		g.applyLogRows(rows, version, "viewport-test")
	}
	add(maxDNSLogRows)
	mw.SetPersistent(false)
	mw.Show()
	mw.Starting().Attach(func() {
		time.AfterFunc(100*time.Millisecond, func() {
			mw.Synchronize(func() {
				defer mw.Close()
				var list win.HWND
				for h := win.GetWindow(g.requestLog.Handle(), win.GW_CHILD); h != 0; h = win.GetWindow(h, win.GW_HWNDNEXT) {
					var bounds win.RECT
					win.GetClientRect(h, &bounds)
					if bounds.Right > 0 {
						list = h
					}
				}
				if list == 0 {
					t.Error("visible native list not found")
					return
				}
				topIndex := func() int { return int(win.SendMessage(list, win.LVM_GETTOPINDEX, 0, 0)) }
				// A selected row outside the viewport must not pull the reader
				// away from the current top item when a batch arrives.
				_ = g.requestLog.SetCurrentIndex(10)
				selected := g.logModel.rows[10].Domain
				g.requestLog.EnsureItemVisible(100)
				top := topIndex()
				anchor := g.logModel.rows[top].Domain
				counts := 0
				g.requestLog.ItemCountChanged().Attach(func() { counts++ })
				add(7)
				if got := topIndex(); got != top+7 || g.logModel.rows[got].Domain != anchor {
					t.Errorf("viewport moved: top=%d, want=%d with same request", got, top+7)
				}
				if got := g.requestLog.CurrentIndex(); got != 17 || g.logModel.rows[got].Domain != selected {
					t.Errorf("selection moved: index=%d, want=17 with same request", got)
				}
				if counts != 0 {
					t.Errorf("full ring changed native item count %d times", counts)
				}
				// Follow the live head while retaining an offscreen selection.
				g.requestLog.EnsureItemVisible(0)
				started := time.Now()
				for i := 0; i < 30; i++ {
					add(1)
					if got := topIndex(); got != 0 {
						t.Errorf("live head jumped to row %d", got)
						break
					}
				}
				t.Logf("30 native updates: %s", time.Since(started))
				if got := g.requestLog.CurrentIndex(); got != 47 || g.logModel.rows[got].Domain != selected {
					t.Errorf("live-head selection lost: index=%d", got)
				}
				_ = g.requestLog.SetCurrentIndex(maxDNSLogRows - 1)
				add(1)
				if got := g.requestLog.CurrentIndex(); got != -1 {
					t.Errorf("evicted row remains selected: %d", got)
				}
				assertNativeLogCount(t, g, maxDNSLogRows)
			})
		})
	})
	mw.Run()
}
