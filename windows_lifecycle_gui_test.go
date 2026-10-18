//go:build windows && !console

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"

	"github.com/lxn/win"
	"golang.org/x/sys/windows"
)

// These helpers are called only from the opted-in empty-machine lifecycle test.
// They operate the built GUI process, not a test replacement for guiState or SCM.
type lifecycleMigrationGUIReport struct {
	MenuText, Confirmation, FailureDetails, FailureStatus, SuccessStatus string
	DefaultButton                                                        uint16
	CancelledUnchanged, ValidationFailureUnchanged, Completed            bool
	ProcessesReaped                                                      int
}

var lifecycleGUIUser32 = windows.NewLazySystemDLL("user32.dll")
var lifecycleGUIEnumWindows = lifecycleGUIUser32.NewProc("EnumWindows")
var lifecycleGUISendTimeout = lifecycleGUIUser32.NewProc("SendMessageTimeoutW")
var lifecycleGUIGetMenuBarInfo = lifecycleGUIUser32.NewProc("GetMenuBarInfo")

// Windows retains callback thunks for process lifetime. Reuse one, including
// across polling iterations, instead of allocating a new thunk per enumeration.
var lifecycleGUIEnumeration struct {
	sync.Mutex
	windows []win.HWND
}
var lifecycleGUIEnumCallback = windows.NewCallback(func(hwnd, _ uintptr) uintptr {
	lifecycleGUIEnumeration.windows = append(lifecycleGUIEnumeration.windows, win.HWND(hwnd))
	return 1
})

type lifecycleGUI struct {
	t       *testing.T
	cmd     *exec.Cmd
	pid     uint32
	main    win.HWND
	button  win.HWND
	report  *lifecycleMigrationGUIReport
	stopped bool
}

func startLifecycleGUI(t *testing.T, exe, config string, report *lifecycleMigrationGUIReport) *lifecycleGUI {
	t.Helper()
	g := &lifecycleGUI{t: t, cmd: exec.Command(exe, "gui", "--config", config), report: report}
	if err := g.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	g.pid = uint32(g.cmd.Process.Pid)
	t.Cleanup(g.stop)
	ready := false
	defer func() {
		if !ready {
			g.stop() // Also reap before outer fixture cleanup if startup fails.
		}
	}()
	g.wait("main window", func() bool {
		g.main = g.find(0, `\o/ Walk_MainWindow_Class \o/`, "SonDNS")
		return g.main != 0
	})
	g.wait("enabled service split button", func() bool {
		for _, h := range lifecycleGUIWindows(g.main) {
			if g.owns(h) && strings.EqualFold(lifecycleGUIClass(h), "Button") &&
				win.GetWindowLong(h, win.GWL_STYLE)&0xf == win.BS_SPLITBUTTON && win.IsWindowEnabled(h) {
				g.button = h
				return true
			}
		}
		return false
	})
	ready = true
	return g
}

func (g *lifecycleGUI) stop() {
	if g.stopped {
		return
	}
	g.stopped = true
	// os.Process holds the exact process handle opened at creation. Never kill
	// by image name: the live test service can use the same EXE as this GUI.
	if err := g.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		g.t.Errorf("stop owned GUI process: %v", err)
	}
	_ = g.cmd.Wait()
	g.report.ProcessesReaped++
}

func (g *lifecycleGUI) owns(h win.HWND) bool {
	var pid uint32
	win.GetWindowThreadProcessId(h, &pid)
	return h != 0 && pid == g.pid
}

func lifecycleGUIWindows(parent win.HWND) []win.HWND {
	lifecycleGUIEnumeration.Lock()
	defer lifecycleGUIEnumeration.Unlock()
	lifecycleGUIEnumeration.windows = nil
	if parent == 0 {
		lifecycleGUIEnumWindows.Call(lifecycleGUIEnumCallback, 0)
	} else {
		win.EnumChildWindows(parent, lifecycleGUIEnumCallback, 0)
	}
	return append([]win.HWND(nil), lifecycleGUIEnumeration.windows...)
}

func lifecycleGUIClass(h win.HWND) string {
	var buf [256]uint16
	n, err := win.GetClassName(h, &buf[0], len(buf))
	if err != nil || n == 0 {
		return ""
	}
	return windows.UTF16ToString(buf[:n])
}

func (g *lifecycleGUI) send(h win.HWND, msg uint32, w, l uintptr) uintptr {
	g.t.Helper()
	if !g.owns(h) {
		g.t.Fatal("GUI interaction refused a window outside the owned process")
	}
	var result uintptr
	ok, _, err := lifecycleGUISendTimeout.Call(uintptr(h), uintptr(msg), w, l,
		3 /* SMTO_BLOCK | SMTO_ABORTIFHUNG */, 1000, uintptr(unsafe.Pointer(&result)))
	if ok == 0 {
		g.t.Fatalf("GUI message %#x timed out or failed: %v", msg, err)
	}
	return result
}

func (g *lifecycleGUI) post(h win.HWND, msg uint32, w, l uintptr) {
	g.t.Helper()
	if !g.owns(h) || win.PostMessage(h, msg, w, l) == 0 {
		g.t.Fatal("could not post message to owned GUI window")
	}
}

func (g *lifecycleGUI) text(h win.HWND) string {
	if !g.owns(h) {
		g.t.Fatal("GUI text read refused an unowned window")
	}
	var buf [16384]uint16
	var result uintptr
	// Keep the pointer conversion at Proc.Call so Go retains the buffer for the
	// entire system call (including Windows' cross-process WM_GETTEXT marshalling).
	ok, _, err := lifecycleGUISendTimeout.Call(uintptr(h), win.WM_GETTEXT, uintptr(len(buf)),
		uintptr(unsafe.Pointer(&buf[0])), 3, 1000, uintptr(unsafe.Pointer(&result)))
	if ok == 0 {
		g.t.Fatalf("read owned GUI text: %v", err)
	}
	return windows.UTF16ToString(buf[:])
}

func (g *lifecycleGUI) find(parent win.HWND, class, text string) win.HWND {
	for _, h := range lifecycleGUIWindows(parent) {
		if g.owns(h) && lifecycleGUIClass(h) == class && (text == "" || g.text(h) == text) {
			return h
		}
	}
	return 0
}

func (g *lifecycleGUI) wait(label string, ready func() bool) {
	g.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	var observed []string
	for _, h := range lifecycleGUIWindows(0) {
		if g.owns(h) {
			observed = append(observed, lifecycleGUIClass(h)+": "+g.text(h))
			for _, child := range lifecycleGUIWindows(h) {
				if g.owns(child) {
					observed = append(observed, fmt.Sprintf("  %s id=%d enabled=%t: %s", lifecycleGUIClass(child),
						win.GetWindowLong(child, win.GWL_ID), win.IsWindowEnabled(child), g.text(child)))
				}
			}
		}
	}
	g.t.Fatalf("waiting for GUI %s; owned windows: %v", label, observed)
}

func (g *lifecycleGUI) staticText(parent win.HWND, prefix string) string {
	for _, h := range lifecycleGUIWindows(parent) {
		if g.owns(h) && strings.EqualFold(lifecycleGUIClass(h), "Static") {
			if text := g.text(h); text != "" && strings.HasPrefix(text, prefix) {
				return text
			}
		}
	}
	return ""
}

func (g *lifecycleGUI) confirmation(p serviceMigrationPlan) win.HWND {
	g.t.Helper()
	var rect win.RECT
	if !win.GetClientRect(g.button, &rect) {
		g.t.Fatal("get split button bounds")
	}
	point := uintptr(uint32(uint16(rect.Right-4)) | uint32(uint16(rect.Bottom/2))<<16)
	g.post(g.button, win.WM_LBUTTONDOWN, win.MK_LBUTTON, point)
	g.post(g.button, win.WM_LBUTTONUP, 0, point)
	var popup win.HWND
	g.wait("service menu", func() bool { popup = g.find(0, "#32768", ""); return popup != 0 })
	// GetMenuBarInfo(OBJID_CLIENT) is the documented way to obtain the popup
	// menu. Read its real item text, ID and state before dispatching its command.
	var info struct {
		Size       uint32
		Rect       win.RECT
		Menu       win.HMENU
		Window     win.HWND
		FocusFlags uint32
	}
	info.Size = uint32(unsafe.Sizeof(info))
	objidClient := int32(-4)
	if ok, _, err := lifecycleGUIGetMenuBarInfo.Call(uintptr(popup), uintptr(objidClient), 0, uintptr(unsafe.Pointer(&info))); ok == 0 || info.Menu == 0 {
		g.t.Fatalf("read popup menu: %v", err)
	}
	var action uint32
	for i := int32(0); i < win.GetMenuItemCount(info.Menu); i++ {
		var text [128]uint16
		item := win.MENUITEMINFO{FMask: win.MIIM_ID | win.MIIM_STATE | win.MIIM_STRING, DwTypeData: &text[0], Cch: uint32(len(text))}
		item.CbSize = uint32(unsafe.Sizeof(item))
		if !win.GetMenuItemInfo(info.Menu, uint32(i), 1, &item) {
			g.t.Fatal("read migration menu item")
		}
		if windows.UTF16ToString(text[:]) == "Migrate existing service…" {
			if item.FState&(win.MF_DISABLED|win.MF_GRAYED) != 0 || item.WID == 0 {
				g.t.Fatal("migration menu action is disabled or missing its command ID")
			}
			g.report.MenuText, action = windows.UTF16ToString(text[:]), item.WID
		}
	}
	if action == 0 {
		g.t.Fatal("migration action missing from actual GUI menu")
	}
	g.post(g.button, win.WM_CANCELMODE, 0, 0)
	g.wait("menu dismissed", func() bool { return !g.owns(popup) })
	// WM_COMMAND is the same command the native popup sends to its owner.
	g.post(g.button, win.WM_COMMAND, uintptr(action), 0)
	var dialog win.HWND
	g.wait("migration confirmation", func() bool {
		dialog = g.find(0, "#32770", "Migrate SonDNS service")
		return dialog != 0
	})
	actual := strings.ReplaceAll(g.staticText(dialog, "Move the existing SonDNS service"), "\r\n", "\n")
	if actual != migrationPreview(p) {
		g.t.Fatalf("actual GUI preview differs from inspected plan: %q want %q", actual, migrationPreview(p))
	}
	def := g.send(dialog, win.WM_USER /* DM_GETDEFID */, 0, 0)
	if uint16(def>>16) != 0x534b || uint16(def) != win.IDNO {
		g.t.Fatalf("migration default button is not No: %#x", def)
	}
	g.report.Confirmation, g.report.DefaultButton = actual, uint16(def)
	return dialog
}

func (g *lifecycleGUI) click(dialog win.HWND, id int32) {
	g.t.Helper()
	title := g.text(dialog)
	var button win.HWND
	// A message box becomes enumerable before Windows finishes initializing all
	// its buttons. Readiness is not implied by the title/static text being set.
	g.wait(fmt.Sprintf("enabled dialog button %d in %s", id, title), func() bool {
		button = win.GetDlgItem(dialog, id)
		return g.owns(button) && win.IsWindowEnabled(button)
	})
	g.post(button, win.BM_CLICK, 0, 0)
	g.wait("dialog dismissed", func() bool { return !g.owns(dialog) || g.text(dialog) != title })
}

func requireNoLifecycleGUIBackup(t *testing.T, target string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(filepath.Dir(target), "backups"))
	if err != nil && !errors.Is(err, os.ErrNotExist) || len(entries) != 0 {
		t.Fatalf("cancel/pre-stage failure created a backup: %v %v", entries, err)
	}
}

func runLifecycleGUIRejects(t *testing.T, p serviceMigrationPlan, report *lifecycleMigrationGUIReport, unchanged func()) {
	t.Helper()
	g := startLifecycleGUI(t, p.TargetExe, p.TargetConfig, report)
	defer g.stop()
	dialog := g.confirmation(p)
	g.click(dialog, win.IDNO)
	requireNoLifecycleGUIBackup(t, p.TargetExe)
	unchanged()
	report.CancelledUnchanged = true
	t.Log("live GUI migration cancellation: default No, exact state and DNS unchanged")

	dialog = g.confirmation(p)
	name, err := windows.UTF16PtrFromString(p.SourceConfig)
	if err != nil {
		t.Fatal(err)
	}
	lock, err := windows.CreateFile(name, windows.GENERIC_READ, 0, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if lock != 0 {
			_ = windows.CloseHandle(lock)
		}
	}()
	g.click(dialog, win.IDYES)
	var failure win.HWND
	g.wait("real validation error", func() bool { failure = g.find(0, "#32770", "Migration failed"); return failure != 0 })
	report.FailureDetails = g.staticText(failure, "")
	// ERROR_SHARING_VIOLATION comes from the real source-file lock. A synthetic
	// error callback or an error raised before confirmation does not satisfy this.
	if !strings.Contains(strings.ToLower(report.FailureDetails), "another process") ||
		!strings.Contains(report.FailureDetails, p.SourceConfig) {
		t.Fatalf("unexpected GUI validation error: %s", report.FailureDetails)
	}
	report.FailureStatus = g.staticText(g.main, "Migration failed;")
	if report.FailureStatus != "Migration failed; review the error details" {
		t.Fatalf("pre-stage failure points at a nonexistent recovery report: %q", report.FailureStatus)
	}
	g.click(failure, win.IDOK)
	if err := windows.CloseHandle(lock); err != nil {
		t.Fatal(err)
	}
	lock = 0
	requireNoLifecycleGUIBackup(t, p.TargetExe)
	unchanged()
	report.ValidationFailureUnchanged = true
	t.Log("live GUI migration validation failure: actual file lock, no backup, exact state and DNS unchanged")
}

func runLifecycleGUIMigrate(t *testing.T, p serviceMigrationPlan, report *lifecycleMigrationGUIReport) string {
	t.Helper()
	g := startLifecycleGUI(t, p.TargetExe, p.TargetConfig, report)
	defer g.stop()
	g.click(g.confirmation(p), win.IDYES)
	const prefix = "Migration complete; backup: "
	g.wait("migration success status", func() bool {
		report.SuccessStatus = g.staticText(g.main, prefix)
		return report.SuccessStatus != ""
	})
	backup := strings.TrimPrefix(report.SuccessStatus, prefix)
	if !insideRoot(filepath.Join(filepath.Dir(p.TargetExe), "backups"), backup) ||
		!strings.HasPrefix(filepath.Base(backup), "migration-") {
		t.Fatalf("GUI success has no concrete portable backup path: %q", backup)
	}
	// Read the actual native combobox selections after the success callback.
	selections := map[string]bool{}
	for _, h := range lifecycleGUIWindows(g.main) {
		if g.owns(h) && strings.EqualFold(lifecycleGUIClass(h), "ComboBox") {
			selections[g.text(h)] = true
		}
	}
	if !selections[ProviderCustom] || !selections["TCP"] {
		t.Fatalf("GUI did not apply imported custom upstream/DPI choices: %v", selections)
	}
	report.Completed = true
	t.Log("live GUI migration success: confirmation, imported controls and concrete backup path verified")
	return backup
}
