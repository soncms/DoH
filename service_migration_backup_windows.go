//go:build windows && !console

package main

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

type migrationBackupFile struct {
	Source string
	Backup string
	SHA256 string
	Size   int
}

// Versioned recovery facts are committed before the destination config changes.
// A reader can verify both the legacy manifest and the exact previous config
// without trusting an in-memory Undo closure or re-serializing a future Config.
type migrationRecoveryMetadata struct {
	Ready               bool
	FilesManifestSHA256 string
	DestinationExisted  bool
	DestinationSHA256   string
	DestinationSize     int
	CandidateSHA256     string
	CandidateSize       int
}

type migrationJournal struct {
	SchemaVersion int
	Status        string
	UpdatedUTC    time.Time
	Error         string
	Plan          serviceMigrationPlan
	Recovery      migrationRecoveryMetadata
}

// Backup files are inert .bin copies, never executable inputs for the new
// installation. Logs and older backups are intentionally excluded.
func backupMigrationSource(p serviceMigrationPlan, directory string) ([]migrationBackupFile, error) {
	root := filepath.Dir(p.SourceExe)
	var files []migrationBackupFile
	var total int64
	copyOne := func(path, relative string, limit int64, expected string) error {
		if len(files) >= 2048 {
			return errors.New("legacy backup exceeds 2048 files")
		}
		remaining := int64(256<<20) - total
		if limit > remaining {
			limit = remaining
		}
		data, err := readMigrationFile(root, path, limit)
		if err != nil {
			return err
		}
		hash := migrationHash(data)
		if expected != "" && hash != expected {
			return fmt.Errorf("legacy file changed since preview: %s", path)
		}
		relative = filepath.Join("legacy", relative+".bin")
		if err := writeMigrationBackup(directory, relative, data); err != nil {
			return err
		}
		total += int64(len(data))
		files = append(files, migrationBackupFile{path, relative, hash, len(data)})
		return nil
	}
	if err := copyOne(p.SourceExe, "SonDNS.exe", 128<<20, p.SourceExeHash); err != nil {
		return nil, err
	}
	configRel, _ := filepath.Rel(root, p.SourceConfig)
	if err := copyOne(p.SourceConfig, configRel, 4<<20, p.SourceConfigHash); err != nil {
		return nil, err
	}
	lock := filepath.Join(root, "components.lock.json")
	if _, err := os.Lstat(lock); err == nil {
		if err := copyOne(lock, "components.lock.json", 4<<20, ""); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	// Read directory entries in bounded batches; file reads separately pin all
	// ancestors and reject reparse points/hard links against replacement races.
	entries := 0
	var visit func(string, int) error
	visit = func(path string, depth int) error {
		if depth > 64 {
			return errors.New("legacy runtime directory is too deep")
		}
		if err := rejectPortableReparse(path); err != nil {
			return err
		}
		dir, err := os.Open(path)
		if err != nil {
			return err
		}
		defer dir.Close()
		for {
			batch, readErr := dir.ReadDir(64)
			for _, entry := range batch {
				entries++
				if entries > 4096 {
					return errors.New("legacy runtime exceeds the directory entry limit")
				}
				child := filepath.Join(path, entry.Name())
				if err := rejectPortableReparse(child); err != nil {
					return err
				}
				if entry.IsDir() {
					if err := visit(child, depth+1); err != nil {
						return err
					}
				} else {
					relative, err := filepath.Rel(root, child)
					if err != nil {
						return err
					}
					if err := copyOne(child, relative, 128<<20, ""); err != nil {
						return err
					}
				}
			}
			if errors.Is(readErr, io.EOF) {
				return nil
			}
			if readErr != nil {
				return readErr
			}
		}
	}
	runtimeDir := filepath.Join(root, "runtime")
	if _, err := os.Lstat(runtimeDir); err == nil {
		if err := visit(runtimeDir, 0); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return files, nil
}

func writeMigrationBackup(root, relative string, data []byte) error {
	path, err := portableConfigPath(root, relative)
	if err != nil {
		return err
	}
	if err := ensurePortableDirectory(root, filepath.Dir(path)); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(data)
	if writeErr == nil {
		writeErr = f.Sync()
	}
	return errors.Join(writeErr, f.Close())
}

func atomicMigrationWrite(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".migration-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return errors.Join(err, closeErr)
	}
	return os.Rename(f.Name(), path)
}

func migrationConfigUndo(root, path string, original []byte, existed bool, candidateHash string) func() error {
	return func() error {
		current, err := readMigrationFile(root, path, 4<<20)
		if errors.Is(err, os.ErrNotExist) && !existed {
			return nil
		}
		if err != nil {
			return err
		}
		if existed && migrationHash(current) == migrationHash(original) {
			return nil
		}
		if migrationHash(current) != candidateHash {
			return errors.New("destination config changed concurrently; rollback kept the newer file")
		}
		if !existed {
			return os.Remove(path)
		}
		return atomicMigrationWrite(path, original)
	}
}

func stageServiceMigration(p serviceMigrationPlan) (migrationStage, error) {
	root := filepath.Dir(p.TargetExe)
	backups := filepath.Join(root, "backups")
	if err := ensurePortableDirectory(root, backups); err != nil {
		return migrationStage{}, err
	}
	directory := filepath.Join(backups, "migration-"+time.Now().UTC().Format("20060102T150405Z")+"-"+rand.Text())
	if err := createServiceDirectory(directory); err != nil {
		return migrationStage{}, err
	}
	stage := migrationStage{Directory: directory}
	recovery := migrationRecoveryMetadata{}
	stage.Journal = func(status string, cause error) error {
		message := ""
		if cause != nil {
			message = cause.Error()
		}
		data, err := json.MarshalIndent(migrationJournal{
			SchemaVersion: 1, Status: status, UpdatedUTC: time.Now().UTC(),
			Error: message, Plan: p, Recovery: recovery,
		}, "", "  ")
		if err != nil {
			return err
		}
		return atomicMigrationWrite(filepath.Join(directory, "journal.json"), append(data, '\n'))
	}
	fail := func(cause error) (migrationStage, error) {
		journalErr := stage.Journal("preparation-failed", cause)
		return migrationStage{}, fmt.Errorf("migration preparation failed; old service unchanged; partial backup: %s: %w", directory, errors.Join(cause, journalErr))
	}
	if err := stage.Journal("preparing", nil); err != nil {
		return fail(err)
	}
	files, err := backupMigrationSource(p, directory)
	if err != nil {
		return fail(err)
	}
	manifest, err := json.MarshalIndent(files, "", "  ")
	if err != nil {
		return fail(err)
	}
	manifest = append(manifest, '\n')
	if err := writeMigrationBackup(directory, "files.json", manifest); err != nil {
		return fail(err)
	}
	recovery.FilesManifestSHA256 = migrationHash(manifest)
	original, err := readMigrationFile(root, p.TargetConfig, 4<<20)
	existed := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fail(err)
	}
	if existed {
		if err := writeMigrationBackup(directory, "destination-config.json.bin", original); err != nil {
			return fail(err)
		}
		recovery.DestinationExisted = true
		recovery.DestinationSHA256 = migrationHash(original)
		recovery.DestinationSize = len(original)
	}
	instructions := fmt.Sprintf(`SonDNS migration recovery

Source EXE: %s
Source config: %s
Destination EXE: %s
Destination config: %s
Previous destination config existed: %t

Read journal.json first. "complete" means the transaction finished; "preparing",
"prepared", "switching" or "bound" after interruption requires manual inspection.
No automatic recovery runs after a crash or power loss.

The versioned journal contains the previous SCM configuration/state and exact
HKLM Run value/type. Recovery.Ready must be true before treating the backup as
complete. Recovery.FilesManifestSHA256 verifies files.json; that manifest maps
SHA-256 hashes and original paths to inert legacy .bin backups.
Recovery.DestinationExisted, DestinationSHA256 and DestinationSize describe the
exact previous destination-config.json.bin (including an empty file). If no
destination config existed, there is no such backup. CandidateSHA256 and
CandidateSize identify the config staged for the new installation. Verify sizes
and hashes before restoring anything. Old journals without versioned recovery
metadata require manual inspection; do not assume their backups are complete.
Inspect the current service binding/state before making any changes.
Do not overwrite concurrent administrator changes. Restore files only to a
protected folder; never execute .bin backups directly. Do not start the legacy
EXE as SYSTEM from an unsafe folder. A failed migration may leave DNS stopped.

Automatic restart of the legacy source was approved by folder checks: %t
%s

Backups may contain custom DNS endpoints or credentials. Keep them protected and
remove them deliberately after a successful migration and recovery review.
`, p.SourceExe, p.SourceConfig, p.TargetExe, p.TargetConfig, existed, p.CanRestartSource, p.RecoveryLimit)
	if err := writeMigrationBackup(directory, "RECOVERY.txt", []byte(instructions)); err != nil {
		return fail(err)
	}
	// Use only pinned embedded runtime, never legacy executable helpers.
	if _, err := ensureEmbeddedZapret2At(root, nil); err != nil {
		return fail(err)
	}
	if err := validateMigrationFiles(p); err != nil {
		return fail(err)
	}
	if err := ensurePortableDirectory(root, filepath.Dir(p.TargetConfig)); err != nil {
		return fail(err)
	}
	// Check that preparing the backup did not race a destination config edit.
	current, readErr := readMigrationFile(root, p.TargetConfig, 4<<20)
	if existed && (readErr != nil || migrationHash(current) != migrationHash(original)) ||
		!existed && !errors.Is(readErr, os.ErrNotExist) {
		return fail(errors.New("destination config changed while preparing migration"))
	}
	candidate, err := json.MarshalIndent(p.Settings, "", "  ")
	if err != nil {
		return fail(err)
	}
	candidate = append(candidate, '\n')
	recovery.CandidateSHA256, recovery.CandidateSize = migrationHash(candidate), len(candidate)
	recovery.Ready = true
	stage.Undo = migrationConfigUndo(root, p.TargetConfig, original, existed, migrationHash(candidate))
	if err := stage.Journal("prepared", nil); err != nil {
		return fail(err)
	}
	if err := atomicMigrationWrite(p.TargetConfig, candidate); err != nil {
		return fail(err)
	}
	return stage, nil
}
