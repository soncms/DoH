//go:build windows

package main

import (
	"testing"
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
		"third_party/zapret2/WinDivert.dll":                                         embeddedWinDivertDLL,
		"third_party/zapret2/WinDivert64.sys":                                       embeddedWinDivertSYS,
		"third_party/zapret2/winws2.exe":                                            embeddedZapret2WinWS,
		"third_party/zapret2/cygwin1.dll":                                           embeddedZapret2Cygwin,
		"third_party/zapret2/lua/zapret-lib.lua":                                    embeddedZapret2LibLua,
		"third_party/zapret2/lua/zapret-antidpi.lua":                                embeddedZapret2AntiDPILua,
		"third_party/zapret2/windivert.filter/windivert_part.discord_media.txt":     embeddedZapret2DiscordFilter,
		"third_party/zapret2/windivert.filter/windivert_part.stun.txt":              embeddedZapret2STUNFilter,
		"third_party/zapret2/windivert.filter/windivert_part.wireguard.txt":         embeddedZapret2WireguardFilter,
		"third_party/zapret2/windivert.filter/windivert_part.quic_initial_ietf.txt": embeddedZapret2QUICFilter,
	} {
		if err := verifyRuntimeAsset(name, data); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestRuntimeAssetRejectsTampering(t *testing.T) {
	data := append([]byte(nil), embeddedZapret2WinWS...)
	data[0] ^= 0xff
	if err := verifyRuntimeAsset("third_party/zapret2/winws2.exe", data); err == nil {
		t.Fatal("tampered runtime asset was accepted")
	}
}

func TestNewDPIBackendDisabled(t *testing.T) {
	backend, err := NewDPIBackend(Config{DPI: DPIConfig{Enabled: false}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if backend != nil {
		t.Fatal("disabled DPI unexpectedly created a backend")
	}
}
