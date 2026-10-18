package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestLoadConfigURLOnlyDoesNotInheritDefaultBootstrap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sondns.json")
	if err := os.WriteFile(path, []byte(`{"upstreams":[{"url":"https://resolver.example/dns-query"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []Upstream{{Name: "resolver.example", Type: "DNS over HTTPS", URL: "https://resolver.example/dns-query"}}
	if !reflect.DeepEqual(config.Upstreams, want) {
		t.Fatalf("custom endpoint inherited default fields: got %+v, want %+v", config.Upstreams, want)
	}
	if err := Save(path, config); err != nil {
		t.Fatal(err)
	}
	reloaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(reloaded.Upstreams, want) {
		t.Fatalf("save/reload changed custom upstream: %+v", reloaded.Upstreams)
	}
}

func TestCurrentProviderRecognizesOnlyExactPresets(t *testing.T) {
	for _, tc := range []struct {
		urls []string
		want string
	}{
		{[]string{URLSonCMS}, ProviderSonCMS},
		{[]string{URLNextDNS}, ProviderNextDNS},
		{[]string{"https://dns.nextdns.io/my-profile"}, ProviderCustom},
		{[]string{"https://nextdns.attacker.example/dns-query"}, ProviderCustom},
		{[]string{"https://resolver.example/dns-query"}, ProviderCustom},
		{[]string{URLSonCMS, URLNextDNS}, ProviderCustom},
		{nil, ProviderCustom},
	} {
		var c Config
		for _, url := range tc.urls {
			c.Upstreams = append(c.Upstreams, Upstream{URL: url})
		}
		if got := c.CurrentProvider(); got != tc.want {
			t.Errorf("%v: got %q want %q", tc.urls, got, tc.want)
		}
	}
}

func TestUnmarshalConfigReplacesUpstreamFields(t *testing.T) {
	config := Default()
	config.Upstreams[0].DNSSEC = true
	data := `{"upstreams":[{"url":"https://resolver.example/dns-query"},{"url":"https://other.example/dns-query","name":"Custom","bootstrap":["192.0.2.2:443"],"dnssec":true}],"timeout":3000000000}`
	if err := json.Unmarshal([]byte(data), &config); err != nil {
		t.Fatal(err)
	}
	want := []Upstream{
		{URL: "https://resolver.example/dns-query"},
		{URL: "https://other.example/dns-query", Name: "Custom", Bootstrap: []string{"192.0.2.2:443"}, DNSSEC: true},
	}
	if !reflect.DeepEqual(config.Upstreams, want) {
		t.Fatalf("upstream fields were merged: got %+v, want %+v", config.Upstreams, want)
	}
	if config.Timeout != 3*time.Second {
		t.Fatalf("legacy numeric timeout = %s", config.Timeout)
	}
}

func TestUnmarshalConfigOmittedAndEmptyUpstreams(t *testing.T) {
	config := Default()
	want := config.Upstreams
	if err := json.Unmarshal([]byte(`{"timeout":"2s"}`), &config); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(config.Upstreams, want) || config.Timeout != 2*time.Second {
		t.Fatalf("omitted upstreams did not preserve defaults: %+v", config)
	}
	for _, data := range []string{`{"upstreams":[]}`, `{"upstreams":null}`} {
		config = Default()
		if err := json.Unmarshal([]byte(data), &config); err != nil {
			t.Fatal(err)
		}
		if err := config.Normalize(); err == nil {
			t.Fatalf("explicitly empty upstreams accepted: %s", data)
		}
	}
}

func TestNormalizeEncryptedUpstreamSchemes(t *testing.T) {
	for _, scheme := range []string{"tls", "quic"} {
		c := Config{Upstreams: []Upstream{{URL: scheme + "://resolver.example:853"}}}
		if err := c.Normalize(); err != nil {
			t.Fatalf("%s upstream rejected: %v", scheme, err)
		}
	}
}

func TestNormalizeInterceptionMode(t *testing.T) {
	for _, mode := range []string{"", "windivert", " WINDIVERT "} {
		c := Config{Upstreams: []Upstream{{URL: "https://resolver.example/dns-query"}}, Interception: mode}
		if err := c.Normalize(); err != nil {
			t.Fatalf("interception %q rejected: %v", mode, err)
		}
		if c.Interception != "windivert" {
			t.Fatalf("interception %q normalized to %q", mode, c.Interception)
		}
	}
	for _, mode := range []string{"off", " OFF ", "invalid"} {
		c := Config{Upstreams: []Upstream{{URL: "https://resolver.example/dns-query"}}, Interception: mode}
		if err := c.Normalize(); err == nil {
			t.Fatalf("disabled or invalid interception mode %q was accepted", mode)
		}
	}
}

func TestNormalizeDPIBackendIsFailOpen(t *testing.T) {
	c := Config{Upstreams: []Upstream{{URL: "https://resolver.example/dns-query"}}, DPI: DPIConfig{Enabled: true}}
	if err := c.Normalize(); err != nil {
		t.Fatal(err)
	}
	if c.DPI.Backend != "embedded-zapret2" || !c.DPI.FailOpen {
		t.Fatalf("unexpected DPI defaults: %+v", c.DPI)
	}
}

func TestDefaultConfigDisablesEmbeddedDPI(t *testing.T) {
	c := Default()
	if c.DPI.Enabled || c.DPI.Backend != "embedded-zapret2" || !c.DPI.FailOpen {
		t.Fatalf("unexpected default DPI configuration: %+v", c.DPI)
	}
}

func TestNormalizePreservesExplicitDPIEnablement(t *testing.T) {
	c := Config{Upstreams: []Upstream{{URL: "https://resolver.example/dns-query"}}, DPI: DPIConfig{Enabled: true}}
	if err := c.Normalize(); err != nil {
		t.Fatal(err)
	}
	if !c.DPI.Enabled {
		t.Fatal("explicit DPI enablement was reset during normalization")
	}
}

func TestNormalizeDPICaptureModes(t *testing.T) {
	for _, mode := range []string{"", "default", "TCP", "tcp+udp"} {
		c := Config{Upstreams: []Upstream{{URL: "https://resolver.example/dns-query"}}, DPI: DPIConfig{CaptureMode: mode}}
		if err := c.Normalize(); err != nil {
			t.Fatalf("capture mode %q rejected: %v", mode, err)
		}
		if c.DPI.CaptureMode == "" {
			t.Fatal("capture mode was not defaulted")
		}
	}
	bad := Config{Upstreams: []Upstream{{URL: "https://resolver.example/dns-query"}}, DPI: DPIConfig{CaptureMode: "udp-all"}}
	if err := bad.Normalize(); err == nil {
		t.Fatal("unknown capture mode accepted")
	}
}

func TestNormalizeRejectsUnknownDPIBackend(t *testing.T) {
	c := Config{Upstreams: []Upstream{{URL: "https://resolver.example/dns-query"}}, DPI: DPIConfig{Backend: "native"}}
	if err := c.Normalize(); err == nil {
		t.Fatal("unknown DPI backend accepted")
	}
}

func TestLegacyConfigMigratesLogAndStartupWithoutWriting(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "not-created.json")
	if _, err := Load(missing); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("opening GUI defaults created a config")
	}
	path := filepath.Join(t.TempDir(), "config.json")
	original := []byte(`{"upstreams":[{"url":"https://dns.google/dns-query"}],"log_file":"diagnostic.log"}`)
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.ServiceAutoStart || c.LogFile != "diagnostic.log" {
		t.Fatalf("bad defaults: %+v", c)
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(original) {
		t.Fatal("opening config rewrote the file")
	}
	c.ServiceAutoStart = true
	if err := Save(path, c); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil || !loaded.ServiceAutoStart {
		t.Fatal("startup field did not roundtrip")
	}
}
