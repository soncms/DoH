//go:build windows && !console

package main

import (
	"github.com/lxn/walk"
	"golang.org/x/sys/windows/svc"
	"strings"
	"time"
)

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
		var response dnsIPCResponse
		_, err := queryThisServiceSnapshot()
		if err == nil {
			response, err = callDNSIPC(dnsIPCRequest{Op: "clear_log"})
		}
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
	if valid {
		_ = g.requestLog.SetToolTipText(g.logModel.rows[idx].Error)
	} else {
		_ = g.requestLog.SetToolTipText("")
	}
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
