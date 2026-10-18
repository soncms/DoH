//go:build windows && !console

package main

import (
	"github.com/lxn/walk"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"time"
)

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
	g.updateActionsForAdmin(windows.GetCurrentProcessToken().IsElevated())
}

func (g *guiState) updateActionsForAdmin(admin bool) {
	enabled := g.serviceKnown && !g.busy && !g.service.pending()
	g.serviceButton.SetEnabled(!g.busy && (!admin || enabled))
	g.autoStart.SetEnabled(admin && !g.busy)
	g.dpiMode.SetEnabled(admin && !g.busy)
	g.dnsProvider.SetEnabled(admin && !g.busy)
	g.clearButton.SetEnabled(admin && !g.busy && !g.logClearing)
	if !admin {
		_ = g.serviceButton.SetText("Manage…")
		_ = g.serviceButton.SetToolTipText("Open SonDNS with administrator access to change settings")
	} else if !g.service.Exists {
		_ = g.serviceButton.SetText("Install && start")
	} else if g.service.State == svc.Stopped {
		_ = g.serviceButton.SetText("Start")
	} else {
		_ = g.serviceButton.SetText("Restart")
	}
	_ = g.stopAction.SetEnabled(admin && enabled && g.service.Exists && g.service.State != svc.Stopped)
	_ = g.restartAction.SetEnabled(admin && enabled && g.service.Exists)
	_ = g.uninstallAction.SetEnabled(admin && enabled && g.service.Exists)
	_ = g.copyPortableAction.SetEnabled(admin && !g.busy)
	_ = g.migrateAction.SetEnabled(admin && enabled && g.service.Exists)
}

func (g *guiState) runAction(label string, work func() (string, error), done func()) {
	g.runActionWithFailure(label, work, done, nil)
}

func (g *guiState) runActionWithFailure(label string, work func() (string, error), done func(), failed func(error)) {
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
				if failed != nil {
					failed(err)
				} else {
					g.setStatus(err.Error(), true)
				}
			} else {
				if done != nil {
					done()
				}
				if g.ctx.Err() != nil || g.mw.IsDisposed() {
					return
				}
				g.setStatus(message, false)
			}
			g.applyServiceSnapshot(state, stateErr)
		})
	}()
}

func (g *guiState) installOrStart() {
	if !g.requireAdminGUI() {
		return
	}
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
	if err := validatePortableServiceFolder(portableRoot()); err != nil {
		g.offerSecurePortableCopy(c, err)
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
		if err := savePortableConfig(g.configPath, c); err != nil {
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
