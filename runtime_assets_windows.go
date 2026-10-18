//go:build windows

package main

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

//go:embed assets/runtime-sha256.json
var runtimeAssetManifest []byte

var (
	runtimeAssetOnce sync.Once
	runtimeAssetErr  error
	runtimeAssetPins map[string]string
)

var errRuntimeAssetHashMismatch = errors.New("runtime asset hash mismatch")

func loadRuntimeAssetPins() (map[string]string, error) {
	runtimeAssetOnce.Do(func() {
		var document struct {
			Assets map[string]string `json:"assets"`
		}
		if err := json.Unmarshal(runtimeAssetManifest, &document); err != nil {
			runtimeAssetErr = fmt.Errorf("parse runtime asset manifest: %w", err)
			return
		}
		if len(document.Assets) == 0 {
			runtimeAssetErr = errors.New("runtime asset manifest is empty")
			return
		}
		runtimeAssetPins = make(map[string]string, len(document.Assets))
		for name, hash := range document.Assets {
			name = strings.TrimSpace(name)
			hash = strings.ToLower(strings.TrimSpace(hash))
			if name == "" || len(hash) != sha256.Size*2 {
				runtimeAssetErr = fmt.Errorf("invalid runtime asset manifest entry %q", name)
				return
			}
			if _, err := hex.DecodeString(hash); err != nil {
				runtimeAssetErr = fmt.Errorf("invalid runtime asset hash for %s: %w", name, err)
				return
			}
			runtimeAssetPins[name] = hash
		}
	})
	return runtimeAssetPins, runtimeAssetErr
}

func runtimeAssetHash(data []byte) string {
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

func verifyRuntimeAsset(name string, data []byte) error {
	pins, err := loadRuntimeAssetPins()
	if err != nil {
		return err
	}
	want, ok := pins[name]
	if !ok {
		return fmt.Errorf("%w: %s is not listed", errRuntimeAssetHashMismatch, name)
	}
	got := runtimeAssetHash(data)
	if got != want {
		return fmt.Errorf("%w: %s expected %s got %s", errRuntimeAssetHashMismatch, name, want, got)
	}
	return nil
}

func verifyRuntimeAssetFile(name, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return verifyRuntimeAsset(name, data)
}

func writeEmbeddedAsset(logger *log.Logger, name, path string, data []byte, mode os.FileMode) error {
	if err := verifyRuntimeAsset(name, data); err != nil {
		return fmt.Errorf("verify embedded %s: %w", name, err)
	}
	if err := writePortableFile(path, data, mode); err != nil {
		return err
	}
	if err := verifyRuntimeAssetFile(name, path); err != nil {
		return fmt.Errorf("verify extracted %s: %w", name, err)
	}
	if logger != nil {
		logger.Printf("asset verified: %s sha256=%s", name, runtimeAssetHash(data))
	}
	return nil
}

// The caller validates the parent directories against the portable root first.
// Write beside the destination, so extraction never uses the user's TEMP.
func writePortableFile(path string, data []byte, mode os.FileMode) error {
	if err := rejectPortableReparse(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if existing, err := os.ReadFile(path); err == nil && bytes.Equal(existing, data) {
		return nil
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".sondns-asset-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Chmod(f.Name(), mode); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
