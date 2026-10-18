//go:build windows && !console

package service

import (
	"fmt"
	"path/filepath"

	"golang.org/x/sys/windows/svc"
)

func MigrationPreview(p serviceMigrationPlan) string {
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
