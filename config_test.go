package main

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
	config, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []Upstream{{Name: "resolver.example", Type: "DNS over HTTPS", URL: "https://resolver.example/dns-query"}}
	if !reflect.DeepEqual(config.Upstreams, want) {
		t.Fatalf("custom endpoint inherited default fields: got %+v, want %+v", config.Upstreams, want)
	}
	if err := SaveConfig(path, config); err != nil {
		t.Fatal(err)
	}
	reloaded, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(reloaded.Upstreams, want) {
		t.Fatalf("save/reload changed custom upstream: %+v", reloaded.Upstreams)
	}
}

func TestUnmarshalConfigReplacesUpstreamFields(t *testing.T) {
	config := DefaultConfig()
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
	config := DefaultConfig()
	want := config.Upstreams
	if err := json.Unmarshal([]byte(`{"timeout":"2s"}`), &config); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(config.Upstreams, want) || config.Timeout != 2*time.Second {
		t.Fatalf("omitted upstreams did not preserve defaults: %+v", config)
	}
	for _, data := range []string{`{"upstreams":[]}`, `{"upstreams":null}`} {
		config = DefaultConfig()
		if err := json.Unmarshal([]byte(data), &config); err != nil {
			t.Fatal(err)
		}
		if err := config.normalize(); err == nil {
			t.Fatalf("explicitly empty upstreams accepted: %s", data)
		}
	}
}

func TestNormalizeEncryptedUpstreamSchemes(t *testing.T) {
	for _, scheme := range []string{"tls", "quic"} {
		c := Config{Upstreams: []Upstream{{URL: scheme + "://resolver.example:853"}}}
		if err := c.normalize(); err != nil {
			t.Fatalf("%s upstream rejected: %v", scheme, err)
		}
	}
}

func TestLegacyConfigMigratesLogAndStartupWithoutWriting(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "not-created.json")
	if _, err := LoadConfig(missing); err != nil {
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
	c, err := LoadConfig(path)
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
	if err := SaveConfig(path, c); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadConfig(path)
	if err != nil || !loaded.ServiceAutoStart {
		t.Fatal("startup field did not roundtrip")
	}
}
