//go:build windows && !console

package service

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
	appconfig "sondns/internal/config"
)

type serviceMigrationPlan struct {
	Previous                        mgr.Config
	State                           svc.State
	SourceExe, SourceConfig         string
	SourceExeHash, SourceConfigHash string
	TargetExeHash                   string
	TargetExe, TargetConfig         string
	Settings                        appconfig.Config
	CanRestartSource                bool
	RecoveryLimit                   string
	Tray                            migrationTrayValue
}

func migrationHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func migrationSourcePaths(command string) (string, string, error) {
	args, err := windows.DecomposeCommandLine(command)
	if err != nil || len(args) < 3 || !strings.EqualFold(filepath.Base(args[0]), "SonDNS.exe") || args[1] != "run" {
		return "", "", errors.New("migration supports only a SonDNS.exe run service with an explicit local config")
	}
	exe := filepath.Clean(args[0])
	volume := filepath.VolumeName(exe)
	if !filepath.IsAbs(exe) || len(volume) != 2 || volume[1] != ':' {
		return "", "", errors.New("legacy service must use an absolute EXE path on a local drive")
	}
	var config string
	switch {
	case len(args) == 3 && strings.HasPrefix(args[2], "--config="):
		config = strings.TrimPrefix(args[2], "--config=")
	case len(args) == 4 && args[2] == "--config":
		config = args[3]
	default:
		return "", "", errors.New("legacy service command has unsupported or ambiguous arguments")
	}
	if config == "" {
		return "", "", errors.New("legacy service config path is empty")
	}
	config, err = PortableConfigPath(filepath.Dir(exe), config)
	return exe, config, err
}

// Pin each ancestor against rename/reparse replacement while reading a legacy
// file. Its directory may be user-writable, so path checks alone are insufficient.
// These handles do not change ACLs and are released immediately after the read.
func readMigrationFile(root, path string, limit int64) ([]byte, error) {
	if limit < 0 || !filepath.IsAbs(root) {
		return nil, errors.New("migration reads require an absolute root and a nonnegative size limit")
	}
	path, err := PortableConfigPath(root, path)
	if err != nil {
		return nil, err
	}
	volume := filepath.VolumeName(path)
	if len(volume) != 2 || volume[1] != ':' {
		return nil, errors.New("migration reads require a local drive")
	}
	var ancestors []string
	for parent := filepath.Dir(path); ; parent = filepath.Dir(parent) {
		ancestors = append(ancestors, parent)
		if strings.EqualFold(parent, volume+`\`) {
			break
		}
	}
	var handles []windows.Handle
	defer func() {
		for i := len(handles) - 1; i >= 0; i-- {
			_ = windows.CloseHandle(handles[i])
		}
	}()
	for i := len(ancestors) - 1; i >= 0; i-- {
		name, err := windows.UTF16PtrFromString(ancestors[i])
		if err != nil {
			return nil, err
		}
		h, err := windows.CreateFile(name, windows.FILE_READ_ATTRIBUTES,
			windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING,
			windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
		if err != nil {
			return nil, err
		}
		handles = append(handles, h)
		var info windows.ByHandleFileInformation
		if err := windows.GetFileInformationByHandle(h, &info); err != nil {
			return nil, err
		}
		if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
			return nil, errors.New("legacy source contains a junction or symlink")
		}
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(h), path)
	defer file.Close()
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		return nil, err
	}
	if info.FileAttributes&(windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DIRECTORY) != 0 || info.NumberOfLinks != 1 {
		return nil, errors.New("legacy source must be a regular file with one link")
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("legacy source file exceeds the migration size limit")
	}
	return data, nil
}

func prepareServiceMigration(previous mgr.Config, state svc.State, targetExe, targetConfig string) (serviceMigrationPlan, error) {
	p := serviceMigrationPlan{Previous: cloneServiceConfig(previous), State: state, TargetExe: targetExe, TargetConfig: targetConfig}
	if state != svc.Running && state != svc.Stopped {
		return p, errors.New("wait until the old service is running or stopped before migration")
	}
	if previous.ServiceType != windows.SERVICE_WIN32_OWN_PROCESS ||
		(previous.ServiceStartName != "" && !strings.EqualFold(previous.ServiceStartName, "LocalSystem")) ||
		(previous.StartType != windows.SERVICE_AUTO_START && previous.StartType != windows.SERVICE_DEMAND_START) {
		return p, errors.New("migration requires a LocalSystem own-process SonDNS service with automatic or manual startup")
	}
	var err error
	p.SourceExe, p.SourceConfig, err = migrationSourcePaths(previous.BinaryPathName)
	if err != nil {
		return p, err
	}
	if strings.EqualFold(filepath.Clean(p.SourceExe), filepath.Clean(targetExe)) {
		return p, errors.New("this service is already bound to the current EXE")
	}
	if insideRoot(filepath.Dir(p.SourceExe), targetExe) || insideRoot(filepath.Dir(targetExe), p.SourceExe) {
		return p, errors.New("migration source and destination folders must not overlap")
	}
	p.TargetConfig, err = PortableConfigPath(filepath.Dir(targetExe), targetConfig)
	if err != nil {
		return p, err
	}
	raw, err := readMigrationFile(filepath.Dir(p.SourceExe), p.SourceConfig, 4<<20)
	if err != nil {
		return p, fmt.Errorf("read legacy config: %w", err)
	}
	settings := appconfig.Default()
	if err := json.Unmarshal(raw, &settings); err != nil {
		return p, fmt.Errorf("parse legacy config: %w", err)
	}
	if err := settings.Normalize(); err != nil {
		return p, err
	}
	if settings.DPI.Executable != "" || len(settings.DPI.Arguments) != 0 ||
		(settings.DPI.Backend != "" && settings.DPI.Backend != "embedded-zapret2") {
		return p, errors.New("legacy custom helper executable/arguments require manual review before migration")
	}
	settings.LogFile = appconfig.DefaultLogPath()
	settings.ServiceAutoStart = previous.StartType == windows.SERVICE_AUTO_START
	p.Settings = settings
	p.SourceConfigHash = migrationHash(raw)
	image, err := readMigrationFile(filepath.Dir(p.SourceExe), p.SourceExe, 128<<20)
	if err != nil {
		return p, fmt.Errorf("read legacy EXE: %w", err)
	}
	if !bytes.HasPrefix(image, []byte("MZ")) {
		return p, errors.New("legacy SonDNS.exe is not a Windows executable")
	}
	p.SourceExeHash = migrationHash(image)
	if err := ValidatePortableServiceFolder(filepath.Dir(targetExe)); err != nil {
		return p, err
	}
	image, err = readMigrationFile(filepath.Dir(targetExe), targetExe, 128<<20)
	if err != nil {
		return p, err
	}
	p.TargetExeHash = migrationHash(image)
	if err := ValidatePortableServiceFolder(filepath.Dir(p.SourceExe)); err == nil {
		p.CanRestartSource = true
	} else {
		p.RecoveryLimit = "If migration fails after stopping DNS, the old binding/settings will be restored but the old EXE will stay stopped because its folder is unsafe for SYSTEM. Use the protected backup and recovery report to repair it."
	}
	return p, nil
}
