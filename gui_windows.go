//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/lxn/walk"
	"github.com/lxn/win"
	"github.com/miekg/dns"
	"golang.org/x/sys/windows/registry"
	"golang.org/x/sys/windows/svc"
)

type guiState struct {
	mw                                                                 *walk.MainWindow
	radioSonCMS, radioNextDNS                                          *walk.RadioButton
	autoStart                                                          *walk.CheckBox
	checkButton, clearButton                                           *walk.PushButton
	serviceButton                                                      *walk.SplitButton
	stopAction, restartAction, uninstallAction                         *walk.Action
	status, serviceStatus                                              *walk.Label
	detailsButton                                                      *walk.PushButton
	dpiMode                                                            *walk.ComboBox
	requestLog                                                         *walk.TableView
	logModel                                                           *requestLogModel
	logStyler                                                          *requestLogStyler
	notifyIcon                                                         *walk.NotifyIcon
	logVersion                                                         uint64
	logGeneration                                                      uint64
	logPollPending, logClearing                                        bool
	lastLogError                                                       string
	logHighlightGeneration                                             uint64
	logHighlightTimer                                                  *time.Timer
	instanceID                                                         string
	service                                                            serviceSnapshot
	copyDomainAction, copyIPAction, detailsAction                      *walk.Action
	serviceKnown, autoDirty, busy, checking, exiting, syncingAutoStart bool
	syncingDPI                                                         bool
	columnsInitialized                                                 bool
	config                                                             Config
	configPath                                                         string
	ctx                                                                context.Context
	cancel                                                             context.CancelFunc
}

func runGUI() error { return runGUIWithConfigAndTray(defaultConfigPath(), false) }

func runGUIWithConfig(path string) error { return runGUIWithConfigAndTray(path, false) }

func runGUIWithConfigAndTray(path string, startInTray bool) error {
	instance, acquired, err := acquireProcessMutex("Local\\SonDNS.GUI")
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
	c, err := LoadConfig(path)
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
	if c.CurrentProvider() == ProviderNextDNS {
		g.radioNextDNS.SetChecked(true)
	} else {
		g.radioSonCMS.SetChecked(true)
	}
	g.radioSonCMS.CheckedChanged().Attach(func() {
		if g.radioSonCMS.Checked() {
			g.switchProvider(ProviderSonCMS)
		}
	})
	g.radioNextDNS.CheckedChanged().Attach(func() {
		if g.radioNextDNS.Checked() {
			g.switchProvider(ProviderNextDNS)
		}
	})
	g.autoStart.SetChecked(c.ServiceAutoStart)
	g.autoStart.CheckedChanged().Attach(g.toggleAutoStart)
	g.setCaptureMode(c.DPI.CaptureMode)
	if err = g.installTray(); err != nil {
		return fmt.Errorf("initialize tray icon: %w", err)
	}
	if !startInTray {
		mw.Show()
		_ = g.radioSonCMS.SetFocus()
	}
	go g.monitor()
	go g.monitorRequestLog()
	go g.startServiceIfNeeded()
	mw.Run()
	return nil
}

var dpiCaptureModes = []string{"Default", "TCP", "TCP + QUIC UDP"}
var dpiCaptureModeValues = []string{"default", "tcp", "tcp+udp"}

type dpiCaptureModeModel struct{ walk.ListModelBase }

func (m *dpiCaptureModeModel) ItemCount() int              { return len(dpiCaptureModes) }
func (m *dpiCaptureModeModel) Value(index int) interface{} { return dpiCaptureModes[index] }

func (g *guiState) captureModeValue() string {
	idx := g.dpiMode.CurrentIndex()
	if idx < 0 || idx >= len(dpiCaptureModeValues) {
		return "default"
	}
	return dpiCaptureModeValues[idx]
}

func (g *guiState) setCaptureMode(mode string) {
	for i, value := range dpiCaptureModeValues {
		if value == mode {
			g.syncingDPI = true
			_ = g.dpiMode.SetCurrentIndex(i)
			g.syncingDPI = false
			return
		}
	}
	g.syncingDPI = true
	_ = g.dpiMode.SetCurrentIndex(0)
	g.syncingDPI = false
}

func (g *guiState) changeCaptureMode() {
	if g.syncingDPI || g.dpiMode == nil {
		return
	}
	mode := g.captureModeValue()
	g.config.DPI.CaptureMode = mode
	cfg := g.config
	if err := cfg.normalize(); err != nil {
		g.setStatus(err.Error(), true)
		return
	}
	g.config = cfg
	if err := SaveConfig(g.configPath, cfg); err != nil {
		g.setStatus("DPI mode save failed: "+err.Error(), true)
		return
	}
	if g.serviceKnown && g.service.Exists && g.service.State != svc.Stopped {
		g.runAction("Applying DPI mode…", func() (string, error) {
			return "DPI mode applied", restartService()
		}, nil)
	} else {
		g.setStatus("DPI mode saved", false)
	}
}

const runRegistryKey = `Software\Microsoft\Windows\CurrentVersion\Run`
const runRegistryName = "SonDNS"

func syncGUIAutostart(enable bool) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, runRegistryKey, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if enable {
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		target := fmt.Sprintf(`"%s" gui --tray`, exe)
		return k.SetStringValue(runRegistryName, target)
	}
	err = k.DeleteValue(runRegistryName)
	if errors.Is(err, registry.ErrNotExist) {
		return nil
	}
	return err
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

func (g *guiState) startServiceIfNeeded() {
	state, err := queryServiceSnapshot()
	if err != nil || !state.Exists || state.State != svc.Stopped {
		return
	}
	if err := startInstalledService(); err != nil {
		g.onUI(func() { g.setStatus("DNS service start failed: "+err.Error(), true) })
		return
	}
	g.onUI(func() { g.setStatus("DNS service starting…", false) })
}

// Poll SCM independently of log reads. Pending transitions use a
// bounded fast interval; normal monitoring catches external service changes.
func (g *guiState) monitor() {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	var nextService, pendingSince time.Time
	timedOut := false
	for {
		now := time.Now()
		if !now.Before(nextService) {
			snapshot, err := queryServiceSnapshot()
			delay := time.Second
			if err == nil && snapshot.pending() {
				if pendingSince.IsZero() {
					pendingSince = now
				}
				if now.Sub(pendingSince) < 10*time.Second {
					delay = 200 * time.Millisecond
				} else if !timedOut {
					timedOut = true
					g.onUI(func() { g.setStatus("Service transition timed out", true) })
				}
			} else {
				pendingSince = time.Time{}
				timedOut = false
			}
			g.onUI(func() { g.applyServiceSnapshot(snapshot, err) })
			nextService = now.Add(delay)
		}
		select {
		case <-g.ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

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
	req := dnsIPCRequest{Op: "snapshot", Version: g.logVersion, InstanceID: g.instanceID}
	go func() {
		response, err := callDNSIPC(req)
		g.onUI(func() {
			g.logPollPending = false
			g.acceptLogResponse(response, err, generation)
		})
	}()
}

func (g *guiState) acceptLogResponse(response dnsIPCResponse, err error, generation uint64) {
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

func (g *guiState) applyServiceSnapshot(s serviceSnapshot, err error) {
	if err != nil {
		g.serviceKnown = false
		_ = g.serviceStatus.SetText("status unavailable")
		_ = g.serviceStatus.SetToolTipText(err.Error())
	} else {
		g.serviceKnown = true
		g.service = s
		_ = g.serviceStatus.SetToolTipText("")
		_ = g.serviceStatus.SetText(s.text())
		auto := g.config.ServiceAutoStart
		if s.Exists {
			auto = s.autoStart()
		}
		if !g.autoDirty && g.autoStart.Checked() != auto {
			g.syncingAutoStart = true
			g.autoStart.SetChecked(auto)
			g.syncingAutoStart = false
		}
	}
	g.updateActions()
}

func (g *guiState) updateActions() {
	enabled := g.serviceKnown && !g.busy && !g.service.pending()
	g.serviceButton.SetEnabled(enabled)
	g.autoStart.SetEnabled(!g.busy)
	g.checkButton.SetEnabled(!g.busy && !g.checking)
	g.radioSonCMS.SetEnabled(!g.busy)
	g.radioNextDNS.SetEnabled(!g.busy)
	if !g.service.Exists {
		_ = g.serviceButton.SetText("Install && start")
	} else if g.service.State == svc.Stopped {
		_ = g.serviceButton.SetText("Start")
	} else {
		_ = g.serviceButton.SetText("Restart")
	}
	_ = g.stopAction.SetEnabled(enabled && g.service.Exists && g.service.State != svc.Stopped)
	_ = g.restartAction.SetEnabled(enabled && g.service.Exists)
	_ = g.uninstallAction.SetEnabled(enabled && g.service.Exists)
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

func (g *guiState) switchProvider(provider string) {
	upstream := PredefinedUpstream(provider)
	g.config.Upstreams = []Upstream{upstream}
	cfg := g.config
	cfgPath := g.configPath

	go func() {
		_ = SaveConfig(cfgPath, cfg)
		res, err := callDNSIPC(dnsIPCRequest{
			Op:        "set_upstream",
			Upstreams: cfg.Upstreams,
		})
		g.onUI(func() {
			if err != nil {
				g.setStatus("Switched to "+provider+" (service offline)", false)
			} else if !res.OK {
				g.setStatus("Switch error: "+res.Error, true)
			} else {
				g.setStatus("DNS switched to "+provider, false)
			}
		})
	}()
}

func (g *guiState) toggleAutoStart() {
	if g.syncingAutoStart {
		return
	}
	enable := g.autoStart.Checked()
	g.config.ServiceAutoStart = enable
	cfg := g.config
	cfgPath := g.configPath

	go func() {
		_ = SaveConfig(cfgPath, cfg)
		state, err := queryServiceSnapshot()
		if err == nil && state.Exists && state.StartType != serviceStartType(enable) {
			_ = setServiceAutoStart(enable)
		}
		_ = syncGUIAutostart(enable)
		g.onUI(func() {
			mode := "disabled"
			if enable {
				mode = "enabled"
			}
			g.setStatus("Start with Windows "+mode, false)
		})
	}()
}

func (g *guiState) editedConfig() (Config, error) {
	c := g.config
	provider := ProviderSonCMS
	if g.radioNextDNS != nil && g.radioNextDNS.Checked() {
		provider = ProviderNextDNS
	}
	c.Upstreams = []Upstream{PredefinedUpstream(provider)}
	c.Interception = "windivert"
	if g.dpiMode != nil {
		c.DPI.CaptureMode = g.captureModeValue()
	}
	c.ServiceAutoStart = g.autoStart.Checked()
	if err := c.normalize(); err != nil {
		return Config{}, err
	}
	return c, nil
}

func (g *guiState) runAction(label string, work func() (string, error), done func()) {
	if g.busy {
		return
	}
	g.busy = true
	g.setStatus(label, false)
	g.updateActions()
	go func() {
		message, err := work()
		state, stateErr := queryServiceSnapshot()
		g.onUI(func() {
			g.busy = false
			if err != nil {
				g.setStatus(err.Error(), true)
			} else {
				if done != nil {
					done()
				}
				g.setStatus(message, false)
			}
			g.applyServiceSnapshot(state, stateErr)
		})
	}()
}

func (g *guiState) installOrStart() {
	if !g.serviceKnown || g.busy {
		return
	}
	if !g.service.Exists {
		g.installService()
		return
	}
	if g.service.State == svc.Stopped {
		g.runAction("Starting…", func() (string, error) { return "Start requested", startInstalledService() }, nil)
	} else {
		g.restartService()
	}
}

func (g *guiState) installService() {
	c, err := g.editedConfig()
	if err != nil {
		g.setStatus(err.Error(), true)
		return
	}
	mode := "manual"
	if c.ServiceAutoStart {
		mode = "automatic (starts with Windows)"
	}
	message := "Install and start SonDNS?\nStartup: " + mode + "."
	if c.Interception == "windivert" {
		message += "\nThis enables the WinDivert driver."
	}
	if walk.MsgBox(g.mw, "Install service", message, walk.MsgBoxYesNo|walk.MsgBoxIconQuestion|walk.MsgBoxDefButton2) != walk.DlgCmdYes {
		return
	}
	g.runAction("Installing…", func() (string, error) {
		if err := SaveConfig(g.configPath, c); err != nil {
			return "", err
		}
		return "Start requested", installService([]string{"--config", g.configPath})
	}, func() { g.config = c; g.autoDirty = false })
}

func (g *guiState) restartService() {
	if walk.MsgBox(g.mw, "Restart service", "Restart with the saved settings?", walk.MsgBoxYesNo|walk.MsgBoxIconQuestion|walk.MsgBoxDefButton2) != walk.DlgCmdYes {
		return
	}
	g.runAction("Restarting…", func() (string, error) { return "Restart requested", restartService() }, nil)
}

func (g *guiState) stopService() {
	g.runAction("Stopping…", func() (string, error) { return "Service stopped", stopService() }, nil)
}

func (g *guiState) uninstallService() {
	if walk.MsgBox(g.mw, "Uninstall service", "Stop and remove SonDNS?", walk.MsgBoxYesNo|walk.MsgBoxIconQuestion|walk.MsgBoxDefButton2) != walk.DlgCmdYes {
		return
	}
	g.runAction("Removing…", func() (string, error) {
		return "Installation removed; SonDNS is ready to install again", uninstallService()
	}, nil)
}

func (g *guiState) checkResolver() {
	if g.checking {
		return
	}
	c, err := g.editedConfig()
	if err != nil {
		g.setStatus(err.Error(), true)
		return
	}
	g.checking = true
	g.checkButton.SetEnabled(false)
	g.setStatus("Checking…", false)
	go func() {
		started := time.Now()
		c.Upstreams = c.Upstreams[:1]
		r, err := NewResolver(c)
		if err == nil {
			defer func() {
				for _, client := range r.clients {
					client.http.CloseIdleConnections()
				}
			}()
			msg := new(dns.Msg)
			msg.SetQuestion("example.com.", dns.TypeA)
			wire, _ := msg.Pack()
			ctx, cancel := context.WithTimeout(g.ctx, c.Timeout)
			defer cancel()
			_, err = r.Resolve(ctx, wire)
		}
		g.onUI(func() {
			g.checking = false
			g.checkButton.SetEnabled(true)
			if err != nil {
				g.setStatus("Check failed: "+err.Error(), true)
			} else {
				provider := c.CurrentProvider()
				g.setStatus(fmt.Sprintf("%s OK · %d ms", provider, time.Since(started).Milliseconds()), false)
			}
		})
	}()
}

func (g *guiState) applyLogRows(rows []DNSRequestEntry, version uint64, instanceID string) {
	isNewInstance := instanceID != "" && g.instanceID != "" && instanceID != g.instanceID
	if isNewInstance {
		g.instanceID = instanceID
		g.logVersion = 0
	} else if instanceID != "" && g.instanceID == "" {
		g.instanceID = instanceID
	}
	if !isNewInstance && version <= g.logVersion {
		return
	}

	// Clear (explicit or new-instance): full reset, no flicker concern.
	if len(rows) == 0 {
		g.logVersion = version
		g.logModel.rows = nil
		g.logModel.PublishRowsReset()
		g.resetLogHighlight()
		g.updateCopyActions()
		return
	}

	// A full snapshot is authoritative, including after a clear from another
	// client. Version counts mutations (Clear as well as Add), not just rows.
	delta := int(min(version-g.logVersion, uint64(len(rows))))
	if isNewInstance || len(rows) != min(len(g.logModel.rows)+delta, maxDNSLogRows) {
		wasAtTop := g.requestLog.IsAtTop()
		wasAtBottom := g.requestLog.IsAtBottom()
		reversed := make([]DNSRequestEntry, len(rows))
		for i, entry := range rows {
			reversed[len(rows)-1-i] = entry
		}
		g.logVersion = version
		g.logModel.rows = reversed
		g.logModel.PublishRowsReset()
		g.resetLogHighlight()
		g.updateCopyActions()
		if wasAtTop {
			g.requestLog.EnsureItemVisible(0)
		} else if wasAtBottom {
			g.requestLog.EnsureItemVisible(g.logModel.RowCount() - 1)
		}
		return
	}

	if delta <= 0 {
		g.logVersion = version
		return
	}

	combined := make([]DNSRequestEntry, len(rows))
	for i, entry := range rows {
		combined[len(rows)-1-i] = entry
	}

	wasAtTop := g.requestLog.IsAtTop()
	g.logVersion = version
	g.logModel.rows = combined
	g.highlightNewLogRows(delta)
	// Apply the final count once and preserve the reader's scroll/selection.
	// Separate remove/insert events temporarily shrink the virtual list and
	// Walk's normal insertion handler scrolls to the selection on every batch.
	_ = g.requestLog.RefreshRowsAfterPrepend(delta)
	if wasAtTop {
		// Native ListView can restore the previous selection after a model
		// refresh and jump the scrollbar. Reassert the live-head viewport.
		g.requestLog.EnsureItemVisible(0)
	}
	g.updateCopyActions()
}

const (
	newLogHighlightDuration = 2 * time.Second
	maxHighlightedLogRows   = 10
)

func (g *guiState) highlightNewLogRows(count int) {
	if g.logStyler == nil || count <= 0 {
		return
	}
	if count > maxHighlightedLogRows {
		count = maxHighlightedLogRows
	}
	g.logHighlightGeneration++
	g.logStyler.rows = count
	g.logStyler.until = time.Now().Add(newLogHighlightDuration)
	if g.logHighlightTimer != nil {
		g.logHighlightTimer.Stop()
	}
	generation := g.logHighlightGeneration
	g.logHighlightTimer = time.AfterFunc(newLogHighlightDuration, func() {
		g.onUI(func() {
			if g.logHighlightGeneration != generation || g.logStyler == nil {
				return
			}
			count := min(g.logStyler.rows, g.logModel.RowCount())
			g.logStyler.rows = 0
			g.logStyler.until = time.Time{}
			if count > 0 {
				g.logModel.PublishRowsChanged(0, count-1)
			}
		})
	})
}

func (g *guiState) resetLogHighlight() {
	g.logHighlightGeneration++
	if g.logHighlightTimer != nil {
		g.logHighlightTimer.Stop()
		g.logHighlightTimer = nil
	}
	if g.logStyler == nil {
		return
	}
	g.logStyler.rows = 0
	g.logStyler.until = time.Time{}
	_ = g.requestLog.Invalidate()
}

func (g *guiState) scrollLogToEnd() {
	if g.logModel == nil || g.logModel.RowCount() == 0 {
		return
	}
	g.requestLog.EnsureItemVisible(g.logModel.RowCount() - 1)
}

func (g *guiState) clearRequestLog() {
	if g.logClearing {
		return
	}
	g.logClearing = true
	g.logGeneration++
	g.logModel.rows = nil
	g.logModel.PublishRowsReset()
	g.resetLogHighlight()
	g.updateCopyActions()

	g.clearButton.SetEnabled(false)
	go func() {
		response, err := callDNSIPC(dnsIPCRequest{Op: "clear_log"})
		g.onUI(func() {
			g.logClearing = false
			g.clearButton.SetEnabled(true)
			if err != nil {
				g.logVersion = 0
				g.instanceID = ""
				if g.service.State == svc.Stopped || !g.service.Exists {
					g.setStatus("Log cleared (service stopped)", false)
				} else {
					g.setStatus("Clear log IPC: "+err.Error(), true)
				}
				return
			}
			g.applyLogRows(nil, response.Version, response.InstanceID)
			g.setStatus("Log cleared", false)
		})
	}()
}

func (g *guiState) setupRequestContextMenu() {
	menu, err := walk.NewMenu()
	if err != nil {
		return
	}
	g.copyDomainAction = walk.NewAction()
	_ = g.copyDomainAction.SetText("Copy domain")
	g.copyDomainAction.Triggered().Attach(func() { g.copyRequestField(false) })
	g.copyIPAction = walk.NewAction()
	_ = g.copyIPAction.SetText("Copy destination IP")
	g.copyIPAction.Triggered().Attach(func() { g.copyRequestField(true) })
	_ = menu.Actions().Add(g.copyDomainAction)
	_ = menu.Actions().Add(g.copyIPAction)
	g.detailsAction = walk.NewAction()
	_ = g.detailsAction.SetText("Show request details")
	g.detailsAction.Triggered().Attach(g.showSelectedRequest)
	_ = menu.Actions().Add(g.detailsAction)
	g.requestLog.SetContextMenu(menu)
	g.requestLog.CurrentIndexChanged().Attach(func() { g.updateCopyActions() })
	g.requestLog.MouseDown().Attach(func(x, y int, button walk.MouseButton) {
		if button == walk.RightButton {
			if idx := g.requestLog.IndexAt(x, y); idx >= 0 {
				_ = g.requestLog.SetCurrentIndex(idx)
			}
			g.updateCopyActions()
		}
	})
	g.updateCopyActions()
}

func (g *guiState) updateCopyActions() {
	idx := g.requestLog.CurrentIndex()
	valid := idx >= 0 && idx < len(g.logModel.rows)
	_ = g.copyDomainAction.SetEnabled(valid)
	_ = g.copyIPAction.SetEnabled(valid && len(g.logModel.rows[idx].AnswerIPs) > 0)
	_ = g.detailsAction.SetEnabled(valid)
	if g.detailsButton != nil {
		g.detailsButton.SetEnabled(valid)
	}
	if valid {
		_ = g.requestLog.SetToolTipText(g.logModel.rows[idx].Error)
	} else {
		_ = g.requestLog.SetToolTipText("")
	}
}

func (g *guiState) showSelectedRequest() {
	idx := g.requestLog.CurrentIndex()
	if idx < 0 || idx >= len(g.logModel.rows) {
		return
	}
	e := g.logModel.rows[idx]
	message := fmt.Sprintf("Domain: %s\nType: %s\nResult: %s\nProtocol: %s\nLatency: %.1f ms", strings.TrimSuffix(e.Domain, "."), e.Type, e.Result, e.Protocol, float64(e.DurationUS)/1000)
	if e.Error != "" {
		message += "\nError: " + e.Error
	}
	walk.MsgBox(g.mw, "DNS request details", message, walk.MsgBoxOK|walk.MsgBoxIconInformation)
}
func (g *guiState) copyRequestField(ip bool) {
	idx := g.requestLog.CurrentIndex()
	if idx < 0 || idx >= len(g.logModel.rows) {
		return
	}
	value := strings.TrimSuffix(g.logModel.rows[idx].Domain, ".")
	if ip {
		value = strings.Join(g.logModel.rows[idx].AnswerIPs, "\n")
	}
	if value != "" {
		if err := walk.Clipboard().SetText(value); err != nil {
			g.setStatus("Copy failed: "+err.Error(), true)
		} else {
			g.setStatus("Copied", false)
		}
	}
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
	dashboard := walk.NewAction()
	_ = dashboard.SetText("Open Connections dashboard")
	dashboard.Triggered().Attach(openConnectionDashboard)
	if err = ni.ContextMenu().Actions().Add(dashboard); err != nil {
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
	img := image.NewRGBA(image.Rect(0, 0, 32, 32))
	draw.Draw(img, img.Bounds(), &image.Uniform{C: color.RGBA{R: 37, G: 99, B: 235, A: 255}}, image.Point{}, draw.Src)
	for y := 5; y < 27; y++ {
		for x := 5; x < 27; x++ {
			dx, dy := x-16, y-16
			if dx*dx+dy*dy < 90 {
				img.Set(x, y, color.RGBA{R: 186, G: 230, B: 253, A: 255})
			}
		}
	}
	return walk.NewIconFromImage(img)
}
