//go:build windows && !console

package gui

import (
	"github.com/lxn/walk"
	"sondns/internal/service"
)

func (g *guiState) migrateService() {
	if g.busy || !g.requireAdminGUI() {
		return
	}
	if err := service.ValidatePortableServiceFolder(service.PortableRoot()); err != nil {
		g.offerSecurePortableCopy(g.config.Clone(), err)
		return
	}
	p, err := service.InspectServiceMigration(g.configPath)
	if err != nil {
		g.setStatus(err.Error(), true)
		walk.MsgBox(g.mw, "Migration unavailable", err.Error(), walk.MsgBoxOK|walk.MsgBoxIconWarning)
		return
	}
	if walk.MsgBox(g.mw, "Migrate SonDNS service", service.MigrationPreview(p), walk.MsgBoxYesNo|walk.MsgBoxIconQuestion|walk.MsgBoxDefButton2) != walk.DlgCmdYes {
		return
	}
	g.runActionWithFailure("Migrating service…", func() (string, error) {
		backup, err := service.MigrateExistingService(p)
		return "Migration complete; backup: " + backup, err
	}, func() {
		g.config = p.Settings.Clone()
		g.autoDirty = false
		g.setDNSProvider(g.config.CurrentProvider())
		g.setDPIMode(g.config.DPI.Enabled, g.config.DPI.CaptureMode)
	}, func(err error) {
		g.setStatus("Migration failed; review the error details", true)
		walk.MsgBox(g.mw, "Migration failed", err.Error(), walk.MsgBoxOK|walk.MsgBoxIconError)
	})
}
