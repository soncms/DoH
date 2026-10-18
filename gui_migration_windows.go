//go:build windows && !console

package main

import (
	"fmt"
	"path/filepath"

	"github.com/lxn/walk"
	"golang.org/x/sys/windows/svc"
)

func migrationPreview(p serviceMigrationPlan) string {
	startup := "manual"
	if p.Settings.ServiceAutoStart {
		startup = "automatic"
	}
	state := "stopped; migration will leave it stopped"
	if p.State == svc.Running {
		state = "running; DNS will be interrupted while switching"
	}
	message := fmt.Sprintf("Move the existing SonDNS service to this portable EXE?\n\nFrom: %s\nTo: %s\nConfig: %s\n\nImport the saved upstreams, DPI/capture settings and %s startup.\nService: %s.\n\nBackup location: %s\nThe old EXE, config, runtime and service/tray settings will be backed up before stopping DNS. The new EXE uses its own embedded runtime.\n", p.SourceExe, p.TargetExe, p.TargetConfig, startup, state, filepath.Join(filepath.Dir(p.TargetExe), "backups", "migration-<unique>"))
	if p.RecoveryLimit != "" {
		message += "\n" + p.RecoveryLimit + "\n"
	}
	return message + "\nA crash or power loss requires manual recovery using the backup report. Continue?"
}

func (g *guiState) migrateService() {
	if g.busy || !g.requireAdminGUI() {
		return
	}
	if err := validatePortableServiceFolder(portableRoot()); err != nil {
		g.offerSecurePortableCopy(g.config.Clone(), err)
		return
	}
	p, err := inspectServiceMigration(g.configPath)
	if err != nil {
		g.setStatus(err.Error(), true)
		walk.MsgBox(g.mw, "Migration unavailable", err.Error(), walk.MsgBoxOK|walk.MsgBoxIconWarning)
		return
	}
	if walk.MsgBox(g.mw, "Migrate SonDNS service", migrationPreview(p), walk.MsgBoxYesNo|walk.MsgBoxIconQuestion|walk.MsgBoxDefButton2) != walk.DlgCmdYes {
		return
	}
	g.runActionWithFailure("Migrating service…", func() (string, error) {
		backup, err := migrateExistingService(p)
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
