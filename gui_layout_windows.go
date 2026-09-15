//go:build windows

package main

import (
	"fmt"
	"github.com/lxn/walk"
	"strings"
)

var compactWindowSize = walk.Size{Width: 560, Height: 420}

func uiRow(parent walk.Container, height int) (*walk.Composite, error) {
	row, err := walk.NewComposite(parent)
	if err != nil {
		return nil, err
	}
	layout := walk.NewHBoxLayout()
	_ = layout.SetMargins(walk.Margins{})
	_ = layout.SetSpacing(5)
	_ = layout.SetAlignment(walk.AlignHNearVCenter)
	if err = row.SetLayout(layout); err != nil {
		return nil, err
	}
	err = row.SetMinMaxSize(walk.Size{Height: height}, walk.Size{Height: height})
	return row, err
}
func uiLabel(parent walk.Container, text string, bold bool) (*walk.Label, error) {
	label, err := walk.NewLabel(parent)
	if err != nil {
		return nil, err
	}
	if err = label.SetText(text); err != nil {
		return nil, err
	}
	label.SetTextColor(walk.RGB(30, 41, 59))
	if bold {
		font, e := walk.NewFont("Segoe UI", 9, walk.FontBold)
		if e != nil {
			return nil, e
		}
		label.SetFont(font)
	}
	return label, nil
}
func uiButton(parent walk.Container, text string, width, height int, click func()) (*walk.PushButton, error) {
	b, err := walk.NewPushButton(parent)
	if err != nil {
		return nil, err
	}
	if err = b.SetText(text); err != nil {
		return nil, err
	}
	if err = b.SetMinMaxSize(walk.Size{Width: width, Height: height}, walk.Size{Width: width, Height: height}); err != nil {
		return nil, err
	}
	b.Clicked().Attach(click)
	return b, nil
}

func (g *guiState) build() error {
	mw := g.mw
	if err := mw.SetTitle("SonDNS"); err != nil {
		return err
	}
	font, err := walk.NewFont("Segoe UI", 9, 0)
	if err != nil {
		return err
	}
	mw.SetFont(font)
	bg, err := walk.NewSolidColorBrush(walk.RGB(255, 255, 255))
	if err != nil {
		return err
	}
	mw.SetBackground(bg)
	layout := walk.NewVBoxLayout()
	_ = layout.SetMargins(walk.Margins{HNear: 12, VNear: 8, HFar: 12, VFar: 8})
	_ = layout.SetSpacing(3)
	_ = layout.SetAlignment(walk.AlignHNearVNear)
	if err = mw.SetLayout(layout); err != nil {
		return err
	}
	dnsRow, err := uiRow(mw, 26)
	if err != nil {
		return err
	}
	label, err := uiLabel(dnsRow, "DNS Server", false)
	if err != nil {
		return err
	}
	_ = label.SetMinMaxSize(walk.Size{Width: 70, Height: 18}, walk.Size{Width: 70, Height: 18})
	g.radioSonCMS, err = walk.NewRadioButton(dnsRow)
	if err != nil {
		return err
	}
	_ = g.radioSonCMS.SetText("SonCMS")
	_ = g.radioSonCMS.SetMinMaxSize(walk.Size{Width: 80, Height: 24}, walk.Size{Width: 80, Height: 24})
	g.radioNextDNS, err = walk.NewRadioButton(dnsRow)
	if err != nil {
		return err
	}
	_ = g.radioNextDNS.SetText("NextDNS")
	_ = g.radioNextDNS.SetMinMaxSize(walk.Size{Width: 85, Height: 24}, walk.Size{Width: 85, Height: 24})
	_, _ = walk.NewHSpacer(dnsRow)
	g.checkButton, err = uiButton(dnsRow, "Check", 64, 26, g.checkResolver)
	if err != nil {
		return err
	}
	service, err := uiRow(mw, 26)
	if err != nil {
		return err
	}
	g.serviceStatus, err = uiLabel(service, "checking…", false)
	if err != nil {
		return err
	}
	_ = g.serviceStatus.SetEllipsisMode(walk.EllipsisEnd)
	_ = g.serviceStatus.SetMinMaxSize(walk.Size{Width: 174, Height: 18}, walk.Size{Height: 18})
	_, _ = walk.NewHSpacer(service)
	g.autoStart, err = walk.NewCheckBox(service)
	if err != nil {
		return err
	}
	_ = g.autoStart.SetText("Start with Windows")
	_ = g.autoStart.SetMinMaxSize(walk.Size{Width: 138, Height: 24}, walk.Size{Width: 138, Height: 24})
	g.serviceButton, err = walk.NewSplitButton(service)
	if err != nil {
		return err
	}
	_ = g.serviceButton.SetText("Install && start")
	_ = g.serviceButton.SetMinMaxSize(walk.Size{Width: 118, Height: 26}, walk.Size{Width: 118, Height: 26})
	g.serviceButton.Clicked().Attach(g.installOrStart)
	g.stopAction = walk.NewAction()
	_ = g.stopAction.SetText("Stop")
	g.stopAction.Triggered().Attach(g.stopService)
	g.restartAction = walk.NewAction()
	_ = g.restartAction.SetText("Restart")
	g.restartAction.Triggered().Attach(g.restartService)
	g.uninstallAction = walk.NewAction()
	_ = g.uninstallAction.SetText("Uninstall")
	g.uninstallAction.Triggered().Attach(g.uninstallService)
	for _, a := range []*walk.Action{g.stopAction, g.restartAction, g.uninstallAction} {
		if err = g.serviceButton.Menu().Actions().Add(a); err != nil {
			return err
		}
	}
	logHeader, err := uiRow(mw, 26)
	if err != nil {
		return err
	}
	if _, err = uiLabel(logHeader, "DNS requests", true); err != nil {
		return err
	}
	_, _ = walk.NewHSpacer(logHeader)
	g.clearButton, err = uiButton(logHeader, "Clear log", 72, 26, g.clearRequestLog)
	if err != nil {
		return err
	}
	g.clearCacheButton, err = uiButton(logHeader, "Clear DNS cache", 112, 26, g.clearDNSCache)
	if err != nil {
		return err
	}
	g.requestLog, err = walk.NewTableView(mw)
	if err != nil {
		return err
	}
	g.requestLog.SetPersistent(false)
	g.requestLog.SetRestoringCurrentItemOnReset(false)
	g.requestLog.SetAlternatingRowBG(true)
	g.requestLog.SetGridlines(false)
	_ = g.requestLog.SetColumnsSizable(true)
	g.requestLog.SetColumnsOrderable(false)
	g.requestLog.SetScrollbarOrientation(walk.Horizontal | walk.Vertical)
	_ = g.requestLog.SetMinMaxSize(walk.Size{Height: 230}, walk.Size{})
	for i, name := range []string{"Time", "Domain", "Type", "Result", "Answer IP", "Latency"} {
		col := walk.NewTableViewColumn()
		_ = col.SetTitle(name)
		if i == 5 {
			_ = col.SetAlignment(walk.AlignFar)
		}
		if err = g.requestLog.Columns().Add(col); err != nil {
			return err
		}
	}
	g.logModel = &requestLogModel{}
	if err = g.requestLog.SetModel(g.logModel); err != nil {
		return err
	}
	g.requestLog.SizeChanged().Attach(g.resizeLogColumns)
	g.setupRequestContextMenu()
	g.resizeLogColumns()
	status, err := uiRow(mw, 16)
	if err != nil {
		return err
	}
	g.status, err = uiLabel(status, "Ready", false)
	if err != nil {
		return err
	}
	_ = g.status.SetEllipsisMode(walk.EllipsisEnd)
	_ = g.status.SetMinMaxSize(walk.Size{Width: 100, Height: 16}, walk.Size{Height: 16})
	if err = mw.SetMinMaxSize(compactWindowSize, compactWindowSize); err != nil {
		return err
	}
	if err = mw.SetSize(compactWindowSize); err != nil {
		return err
	}
	g.updateActions()
	return nil
}

func (g *guiState) resizeLogColumns() {
	if g.requestLog == nil {
		return
	}
	if g.columnsInitialized {
		return
	}
	width := g.requestLog.ClientBounds().Width - 20
	if width <= 0 {
		return
	}
	defaults := []int{60, max(120, width-60-60-82-100-64), 60, 82, 100, 64}
	for i, w := range defaults {
		_ = g.requestLog.Columns().At(i).SetWidth(w)
	}
	g.columnsInitialized = true
	g.scrollLogToEnd()
}

type requestLogModel struct {
	walk.TableModelBase
	rows []DNSRequestEntry
}

func (m *requestLogModel) RowCount() int { return len(m.rows) }
func (m *requestLogModel) Value(row, col int) interface{} {
	if row < 0 || row >= len(m.rows) {
		return ""
	}
	e := m.rows[row]
	switch col {
	case 0:
		return e.Time.Local().Format("15:04:05")
	case 1:
		return strings.TrimSuffix(e.Domain, ".")
	case 2:
		return e.Type
	case 3:
		return e.Result
	case 4:
		return strings.Join(e.AnswerIPs, ", ")
	case 5:
		return fmt.Sprintf("%d ms", e.LatencyMS)
	default:
		return ""
	}
}
