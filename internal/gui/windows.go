//go:build windows && !console

package gui

import (
	"context"
	"errors"
	"fmt"
	"syscall"
	"time"

	"github.com/lxn/walk"
	"github.com/lxn/win"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	appconfig "sondns/internal/config"
	"sondns/internal/control"
	"sondns/internal/service"
)

type guiState struct {
	mw                                                       *walk.MainWindow
	dnsProvider                                              *walk.ComboBox
	autoStart                                                *walk.CheckBox
	clearButton                                              *walk.PushButton
	serviceButton                                            *walk.SplitButton
	stopAction, restartAction, uninstallAction               *walk.Action
	copyPortableAction, migrateAction                        *walk.Action
	status, serviceStatus                                    *walk.Label
	dpiMode                                                  *walk.ComboBox
	requestLog                                               *walk.TableView
	logModel                                                 *requestLogModel
	logStyler                                                *requestLogStyler
	notifyIcon                                               *walk.NotifyIcon
	logVersion                                               uint64
	logGeneration                                            uint64
	logPollPending, logClearing                              bool
	lastLogError                                             string
	logHighlightGeneration                                   uint64
	logHighlightTimer                                        *time.Timer
	instanceID                                               string
	service                                                  service.ServiceSnapshot
	copyDomainAction, copyIPAction                           *walk.Action
	serviceKnown, autoDirty, busy, exiting, syncingAutoStart bool
	syncingDPI, syncingProvider                              bool
	columnsInitialized                                       bool
	config                                                   appconfig.Config
	configPath                                               string
	ctx                                                      context.Context
	cancel                                                   context.CancelFunc
}

func Run(path string, startInTray bool) error {
	var bindingWarning error
	if path == "" {
		path, bindingWarning = service.ImplicitGUIConfig()
		if path == "" {
			return bindingWarning
		}
	}
	path, err := service.PortableConfigPath(service.PortableRoot(), path)
	if err != nil {
		return err
	}
	instance, acquired, err := service.AcquireProcessMutex("Local\\SonDNS.GUI")
	if err != nil {
		return err
	}
	if !acquired {
		titlePtr, _ := syscall.UTF16PtrFromString("SonDNS")
		clsPtr, _ := syscall.UTF16PtrFromString(`\o/ Walk_MainWindow_Class \o/`)
		hwnd := win.FindWindow(clsPtr, titlePtr)
		if hwnd == 0 {
			hwnd = win.FindWindow(nil, titlePtr)
		}
		if hwnd != 0 {
			restoreMainWindow(hwnd)
			win.SetForegroundWindow(hwnd)
		}
		return nil
	}
	defer instance.Close()
	needsNoticeRefresh, err := prepareGUIPortableNotices(service.PortableRoot(), windows.GetCurrentProcessToken().IsElevated())
	if err != nil {
		return err
	}
	c, err := appconfig.Load(path)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	g := &guiState{config: c, configPath: path, ctx: ctx, cancel: cancel}
	mw, err := walk.NewMainWindow()
	if err != nil {
		return err
	}
	g.mw = mw
	defer mw.Dispose()
	mw.SetPersistent(false)
	if err = g.build(); err != nil {
		return err
	}
	if needsNoticeRefresh {
		g.setStatus("Use Manage to refresh portable documents", false)
	}
	if bindingWarning != nil {
		g.setStatus("Service configuration: "+bindingWarning.Error(), true)
	}
	g.setDNSProvider(c.CurrentProvider())
	g.autoStart.SetChecked(c.ServiceAutoStart)
	g.autoStart.CheckedChanged().Attach(g.toggleAutoStart)
	g.setDPIMode(c.DPI.Enabled, c.DPI.CaptureMode)
	if err = g.installTray(); err != nil {
		return fmt.Errorf("initialize tray icon: %w", err)
	}
	if !startInTray {
		mw.Show()
		_ = g.dnsProvider.SetFocus()
	}
	go g.monitor()
	go g.monitorRequestLog()
	mw.Run()
	return nil
}

var dnsProviderNames = []string{appconfig.ProviderSonCMS, appconfig.ProviderNextDNS}

type dnsProviderModel struct {
	walk.ListModelBase
	custom bool
}

func (m *dnsProviderModel) ItemCount() int {
	if m.custom {
		return len(dnsProviderNames) + 1
	}
	return len(dnsProviderNames)
}

func (m *dnsProviderModel) Value(index int) interface{} {
	if m.custom && index == len(dnsProviderNames) {
		return appconfig.ProviderCustom
	}
	return dnsProviderNames[index]
}

func (g *guiState) selectedDNSProvider() string {
	idx := g.dnsProvider.CurrentIndex()
	if idx < 0 || idx >= len(dnsProviderNames) {
		return g.config.CurrentProvider()
	}
	return dnsProviderNames[idx]
}

func (g *guiState) setDNSProvider(provider string) {
	g.syncingProvider = true
	defer func() { g.syncingProvider = false }()
	custom := provider == appconfig.ProviderCustom
	if model, ok := g.dnsProvider.Model().(*dnsProviderModel); !ok || model.custom != custom {
		_ = g.dnsProvider.SetModel(&dnsProviderModel{custom: custom})
	}
	index := 0
	if custom {
		index = len(dnsProviderNames)
	}
	for i, name := range dnsProviderNames {
		if name == provider {
			index = i
			break
		}
	}
	_ = g.dnsProvider.SetCurrentIndex(index)
}

func (g *guiState) changeDNSProvider() {
	if g.syncingProvider || g.dnsProvider == nil {
		return
	}
	if g.busy {
		g.setDNSProvider(g.config.CurrentProvider())
		return
	}
	previous := g.config.Clone()
	candidate := previous.Clone()
	candidate.Upstreams = []appconfig.Upstream{appconfig.PredefinedUpstream(g.selectedDNSProvider())}
	applied := previous
	syncSelection := func() { g.config = applied; g.setDNSProvider(applied.CurrentProvider()) }
	g.runActionWithFailure("Applying DNS provider…", func() (string, error) {
		var message string
		var err error
		applied, message, err = service.ApplyDNSProvider(previous, candidate, func(c appconfig.Config) error { return service.SavePortableConfig(g.configPath, c) }, func() (service.ServiceSnapshot, error) { return service.QueryServiceSnapshotForConfig(g.configPath) }, control.Call)
		return message, err
	}, syncSelection, func(err error) { syncSelection(); g.setStatus(err.Error(), true) })
}

var dpiCaptureModes = []string{"Disabled", "Default", "TCP", "TCP + QUIC UDP"}

var dpiCaptureModeValues = []string{"disabled", "default", "tcp", "tcp+udp"}

const dpiDisabledIndex = 0

type dpiCaptureModeModel struct{ walk.ListModelBase }

func (m *dpiCaptureModeModel) ItemCount() int { return len(dpiCaptureModes) }

func (m *dpiCaptureModeModel) Value(index int) interface{} { return dpiCaptureModes[index] }

func (g *guiState) selectedDPIMode() (bool, string) {
	idx := g.dpiMode.CurrentIndex()
	mode := g.config.DPI.CaptureMode
	if mode == "" {
		mode = "default"
	}
	if idx == dpiDisabledIndex {
		return false, mode
	}
	if idx < 0 || idx >= len(dpiCaptureModeValues) {
		return g.config.DPI.Enabled, mode
	}
	return true, dpiCaptureModeValues[idx]
}

func (g *guiState) setDPIMode(enabled bool, mode string) {
	index := dpiDisabledIndex
	if enabled {
		for i, value := range dpiCaptureModeValues {
			if value == mode {
				index = i
				break
			}
		}
		if index == dpiDisabledIndex {
			index = 1
		}
	}
	g.syncingDPI = true
	_ = g.dpiMode.SetCurrentIndex(index)
	g.syncingDPI = false
}

func (g *guiState) changeCaptureMode() {
	if g.syncingDPI || g.dpiMode == nil {
		return
	}
	if g.busy {
		g.setDPIMode(g.config.DPI.Enabled, g.config.DPI.CaptureMode)
		return
	}
	if _, err := service.QueryServiceSnapshotForConfig(g.configPath); err != nil {
		g.setDPIMode(g.config.DPI.Enabled, g.config.DPI.CaptureMode)
		g.setStatus(err.Error(), true)
		return
	}
	enabled, mode := g.selectedDPIMode()
	previous := g.config.Clone()
	candidate := g.config.Clone()
	candidate.DPI.Enabled = enabled
	candidate.DPI.CaptureMode = mode
	if err := candidate.Normalize(); err != nil {
		g.setDPIMode(previous.DPI.Enabled, previous.DPI.CaptureMode)
		g.setStatus(err.Error(), true)
		return
	}
	if err := service.SavePortableConfig(g.configPath, candidate); err != nil {
		g.setDPIMode(previous.DPI.Enabled, previous.DPI.CaptureMode)
		g.setStatus("DPI mode save failed: "+err.Error(), true)
		return
	}
	g.config = candidate
	if g.serviceKnown && g.service.Exists && g.service.State != svc.Stopped {
		g.runActionWithFailure("Applying DPI mode…", func() (string, error) {
			return "DPI mode applied", service.RestartService()
		}, nil, func(err error) {
			g.config = previous
			rollbackErr := service.SavePortableConfig(g.configPath, previous)
			if rollbackErr == nil {
				rollbackErr = service.RestartService()
			}
			err = errors.Join(err, rollbackErr)
			g.setDPIMode(previous.DPI.Enabled, previous.DPI.CaptureMode)
			g.setStatus("DPI mode apply failed: "+err.Error(), true)
		})
	} else {
		g.setStatus("DPI mode saved", false)
	}
}

func (g *guiState) onUI(f func()) {
	if g.ctx.Err() != nil {
		return
	}
	g.mw.Synchronize(func() {
		if g.ctx.Err() == nil && !g.mw.IsDisposed() {
			f()
		}
	})
}

// Poll SCM independently of log reads. Pending transitions use a
// bounded fast interval; normal monitoring catches external service changes.
// 200 ms keeps the table responsive while avoiding ten IPC snapshots per
// second when the service is idle.
const requestLogPollInterval = 200 * time.Millisecond

func (g *guiState) monitorRequestLog() {
	ticker := time.NewTicker(requestLogPollInterval)
	defer ticker.Stop()
	for {
		g.onUI(g.pollRequestLog)
		select {
		case <-g.ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// All cursor and UI state is owned by the UI thread. Only one read may be in
// flight, and Clear invalidates callbacks queued before it began.
func (g *guiState) pollRequestLog() {
	if g.logPollPending || g.logClearing {
		return
	}
	g.logPollPending = true
	generation := g.logGeneration
	req := control.Request{Op: "snapshot", Version: g.logVersion, InstanceID: g.instanceID}
	go func() {
		response, err := control.Call(req)
		g.onUI(func() {
			g.logPollPending = false
			g.acceptLogResponse(response, err, generation)
		})
	}()
}

func (g *guiState) acceptLogResponse(response control.Response, err error, generation uint64) {
	if g.logClearing || generation != g.logGeneration {
		return
	}
	if err != nil {
		if g.service.State == svc.Running && err.Error() != g.lastLogError {
			g.lastLogError = err.Error()
			g.setStatus("DNS service: "+err.Error(), true)
		}
		return
	}
	g.lastLogError = ""
	if !response.Unchanged {
		g.applyLogRows(response.Entries, response.Version, response.InstanceID)
	}
}

func (g *guiState) setStatus(message string, failed bool) {
	_ = g.status.SetText(message)
	_ = g.status.SetToolTipText(message)
	c := walk.RGB(71, 85, 105)
	if failed {
		c = walk.RGB(185, 28, 28)
	}
	g.status.SetTextColor(c)
}

func (g *guiState) toggleAutoStart() {
	if g.syncingAutoStart {
		return
	}
	if g.busy {
		g.syncingAutoStart = true
		g.autoStart.SetChecked(g.config.ServiceAutoStart)
		g.syncingAutoStart = false
		return
	}
	enable := g.autoStart.Checked()
	previous := g.config.Clone()
	candidate := previous.Clone()
	candidate.ServiceAutoStart = enable
	g.autoDirty = true
	g.runActionWithFailure("Updating Start with Windows…", func() (string, error) {
		_, err := service.ApplyStartWithWindows(previous, candidate, func() (service.ServiceSnapshot, error) { return service.QueryServiceSnapshotForConfig(g.configPath) },
			func(enable bool) (func() error, error) { return service.ChangeServiceAutoStart(g.configPath, enable) },
			func(enable bool) (func() error, error) { return service.ChangeGUIAutostart(g.configPath, enable) },
			func(c appconfig.Config) error { return service.SavePortableConfig(g.configPath, c) })
		if err != nil {
			return "", err
		}
		mode := "disabled"
		if enable {
			mode = "enabled"
		}
		return "Start with Windows " + mode, nil
	}, func() { g.config = candidate; g.autoDirty = false }, func(err error) {
		g.autoDirty = false
		g.syncingAutoStart = true
		g.autoStart.SetChecked(previous.ServiceAutoStart)
		g.syncingAutoStart = false
		g.setStatus("Start with Windows update failed: "+err.Error(), true)
	})
}

func (g *guiState) editedConfig() (appconfig.Config, error) {
	c := g.config.Clone()
	// Provider changes already save/apply through changeDNSProvider. Installing
	// or changing DPI must preserve custom URLs, bootstrap addresses and DNSSEC.
	if g.dnsProvider != nil {
		if provider := g.selectedDNSProvider(); provider != appconfig.ProviderCustom && provider != c.CurrentProvider() {
			c.Upstreams = []appconfig.Upstream{appconfig.PredefinedUpstream(provider)}
		}
	}
	c.Interception = "windivert"
	if g.dpiMode != nil {
		c.DPI.Enabled, c.DPI.CaptureMode = g.selectedDPIMode()
	}
	c.ServiceAutoStart = g.autoStart.Checked()
	if err := c.Normalize(); err != nil {
		return appconfig.Config{}, err
	}
	return c, nil
}

func (g *guiState) installTray() error {
	icon, err := newAppIcon()
	if err != nil {
		return err
	}
	if err = g.mw.SetIcon(icon); err != nil {
		return err
	}
	ni, err := walk.NewNotifyIcon(g.mw)
	if err != nil {
		return err
	}
	dispose := func() {
		_ = ni.Dispose()
		if g.notifyIcon == ni {
			g.notifyIcon = nil
		}
	}
	if err = ni.SetIcon(icon); err != nil {
		dispose()
		return err
	}
	if err = ni.SetToolTip("SonDNS"); err != nil {
		dispose()
		return err
	}
	if err = ni.SetVisible(true); err != nil {
		dispose()
		return err
	}
	g.notifyIcon = ni
	showWindow := func() {
		restoreMainWindow(g.mw.Handle())
		_ = g.mw.Activate()
	}
	ni.MouseDown().Attach(func(_, _ int, button walk.MouseButton) {
		if button == walk.LeftButton {
			showWindow()
		}
	})
	show := walk.NewAction()
	_ = show.SetText("Open SonDNS")
	show.Triggered().Attach(showWindow)
	if err = ni.ContextMenu().Actions().Add(show); err != nil {
		dispose()
		return err
	}
	exit := walk.NewAction()
	_ = exit.SetText("Exit")
	exit.Triggered().Attach(func() { g.exiting = true; _ = g.mw.Close() })
	if err = ni.ContextMenu().Actions().Add(exit); err != nil {
		dispose()
		return err
	}
	g.mw.Closing().Attach(func(canceled *bool, _ walk.CloseReason) {
		if !g.exiting {
			*canceled = true
			win.ShowWindow(g.mw.Handle(), win.SW_HIDE)
			return
		}
		g.cancel()
		dispose()
	})
	return nil
}

func restoreMainWindow(hwnd win.HWND) {
	if hwnd == 0 {
		return
	}
	// Show() alone does not clear a hidden/minimized state on every Windows
	// shell version. Restore first, then show and foreground the real HWND.
	win.ShowWindow(hwnd, win.SW_RESTORE)
	win.ShowWindow(hwnd, win.SW_SHOW)
	win.BringWindowToTop(hwnd)
	win.SetForegroundWindow(hwnd)
}

func newAppIcon() (*walk.Icon, error) {
	// rsrc assigns ID 1 to the manifest and ID 2 to the first icon group.
	// Load the same multi-resolution resource Explorer uses for the EXE.
	return walk.NewIconFromResourceIdWithSize(2, walk.Size{Width: 16, Height: 16})
}
