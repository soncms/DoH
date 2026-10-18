//go:build windows

package service

import (
	"testing"

	resources "sondns"
	appconfig "sondns/internal/config"
)

func TestRuntimeAssetManifest(t *testing.T) {
	pins, err := loadRuntimeAssetPins()
	if err != nil {
		t.Fatal(err)
	}
	if len(pins) != 10 {
		t.Fatalf("runtime asset count = %d, want 10", len(pins))
	}
	for name, data := range map[string][]byte{
		"third_party/zapret2/WinDivert.dll":                                         resources.EmbeddedWinDivertDLL,
		"third_party/zapret2/WinDivert64.sys":                                       resources.EmbeddedWinDivertSYS,
		"third_party/zapret2/winws2.exe":                                            resources.EmbeddedZapret2WinWS,
		"third_party/zapret2/cygwin1.dll":                                           resources.EmbeddedZapret2Cygwin,
		"third_party/zapret2/lua/zapret-lib.lua":                                    resources.EmbeddedZapret2LibLua,
		"third_party/zapret2/lua/zapret-antidpi.lua":                                resources.EmbeddedZapret2AntiDPILua,
		"third_party/zapret2/windivert.filter/windivert_part.discord_media.txt":     resources.EmbeddedZapret2DiscordFilter,
		"third_party/zapret2/windivert.filter/windivert_part.stun.txt":              resources.EmbeddedZapret2STUNFilter,
		"third_party/zapret2/windivert.filter/windivert_part.wireguard.txt":         resources.EmbeddedZapret2WireguardFilter,
		"third_party/zapret2/windivert.filter/windivert_part.quic_initial_ietf.txt": resources.EmbeddedZapret2QUICFilter,
	} {
		if err := verifyRuntimeAsset(name, data); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestRuntimeAssetRejectsTampering(t *testing.T) {
	data := append([]byte(nil), resources.EmbeddedZapret2WinWS...)
	data[0] ^= 0xff
	if err := verifyRuntimeAsset("third_party/zapret2/winws2.exe", data); err == nil {
		t.Fatal("tampered runtime asset was accepted")
	}
}

func TestNewDPIBackendDisabled(t *testing.T) {
	backend, err := NewDPIBackend(appconfig.Config{DPI: appconfig.DPIConfig{Enabled: false}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if backend != nil {
		t.Fatal("disabled DPI unexpectedly created a backend")
	}
}
