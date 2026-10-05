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
	"sort"
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

type runtimeAssetDiagnostic struct {
	Name     string `json:"name"`
	Expected string `json:"expected"`
	Actual   string `json:"actual"`
	Verified bool   `json:"verified"`
	Error    string `json:"error,omitempty"`
}

func runtimeAssetData(name string) []byte {
	switch name {
	case "third_party/zapret2/WinDivert.dll":
		return embeddedWinDivertDLL
	case "third_party/zapret2/WinDivert64.sys":
		return embeddedWinDivertSYS
	case "third_party/zapret2/winws2.exe":
		return embeddedZapret2WinWS
	case "third_party/zapret2/cygwin1.dll":
		return embeddedZapret2Cygwin
	case "third_party/zapret2/lua/zapret-lib.lua":
		return embeddedZapret2LibLua
	case "third_party/zapret2/lua/zapret-antidpi.lua":
		return embeddedZapret2AntiDPILua
	case "third_party/zapret2/windivert.filter/windivert_part.discord_media.txt":
		return embeddedZapret2DiscordFilter
	case "third_party/zapret2/windivert.filter/windivert_part.stun.txt":
		return embeddedZapret2STUNFilter
	case "third_party/zapret2/windivert.filter/windivert_part.wireguard.txt":
		return embeddedZapret2WireguardFilter
	case "third_party/zapret2/windivert.filter/windivert_part.quic_initial_ietf.txt":
		return embeddedZapret2QUICFilter
	default:
		return nil
	}
}

func runtimeAssetDiagnostics() []runtimeAssetDiagnostic {
	pins, err := loadRuntimeAssetPins()
	if err != nil {
		return []runtimeAssetDiagnostic{{Name: "manifest", Error: err.Error()}}
	}
	names := make([]string, 0, len(pins))
	for name := range pins {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]runtimeAssetDiagnostic, 0, len(names))
	for _, name := range names {
		data := runtimeAssetData(name)
		actual := ""
		if data != nil {
			actual = runtimeAssetHash(data)
		}
		item := runtimeAssetDiagnostic{Name: name, Expected: pins[name], Actual: actual, Verified: actual != "" && actual == pins[name]}
		if actual == "" {
			item.Error = "embedded asset unavailable"
		}
		result = append(result, item)
	}
	return result
}

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
	if existing, err := os.ReadFile(path); err == nil && bytes.Equal(existing, data) {
		if err := verifyRuntimeAsset(name, existing); err != nil {
			return fmt.Errorf("verify existing %s: %w", name, err)
		}
		if logger != nil {
			logger.Printf("asset verified: %s sha256=%s", name, runtimeAssetHash(existing))
		}
		return nil
	}
	if err := os.WriteFile(path, data, mode); err != nil {
		return err
	}
	if err := os.Chmod(path, mode); err != nil {
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
