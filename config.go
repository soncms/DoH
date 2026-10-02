package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Config is deliberately JSON so the service has no third-party config parser
// and can be edited from a shell or deployment script.
type Config struct {
	Listen              string        `json:"listen"`
	Interception        string        `json:"interception"`
	DPI                 DPIConfig     `json:"dpi_bypass,omitempty"`
	Upstreams           []Upstream    `json:"upstreams"`
	BootstrapDNS        []string      `json:"bootstrap_dns,omitempty"`
	Timeout             time.Duration `json:"timeout"`
	LogFile             string        `json:"log_file"`
	ServiceAutoStart    bool          `json:"service_auto_start"`
	ConnectionRetention time.Duration `json:"connection_retention"`
}

// DPIConfig controls the embedded zapret2/winws2 compatibility backend. The
// backend is shipped as data inside SonDNS.exe and extracted on demand; an
// unavailable or failed backend never prevents DNS interception from starting.
type DPIConfig struct {
	Enabled     bool     `json:"enabled"`
	Backend     string   `json:"backend,omitempty"`
	CaptureMode string   `json:"capture_mode,omitempty"`
	Executable  string   `json:"executable,omitempty"`
	Arguments   []string `json:"arguments,omitempty"`
	Targets     []string `json:"targets,omitempty"`
	Exclude     []string `json:"exclude,omitempty"`
	FailOpen    bool     `json:"fail_open"`
	AutoRestart bool     `json:"auto_restart"`
}

type Upstream struct {
	Name      string   `json:"name,omitempty"`
	Type      string   `json:"type,omitempty"`
	URL       string   `json:"url"`
	Bootstrap []string `json:"bootstrap,omitempty"`
	DNSSEC    bool     `json:"dnssec,omitempty"`
}

const (
	ProviderSonCMS  = "SonCMS"
	ProviderNextDNS = "NextDNS"

	URLSonCMS  = "https://dns.soncms.net/dns-query/sondns"
	URLNextDNS = "https://dns.nextdns.io/2e2e3c"
)

var (
	DefaultBootstrapDNS = []string{
		"1.1.1.1:53",
		"1.0.0.1:53",
		"[2606:4700:4700::1111]:53",
		"[2606:4700:4700::1001]:53",
	}
	BootstrapNextDNS = []string{
		"45.90.28.0:443", "45.90.30.0:443",
		"[2a07:a8c0::]:443", "[2a07:a8c1::]:443",
	}
)

func PredefinedUpstream(name string) Upstream {
	switch name {
	case ProviderNextDNS:
		return Upstream{
			Name:      ProviderNextDNS,
			Type:      "DNS over HTTPS",
			URL:       URLNextDNS,
			Bootstrap: append([]string(nil), BootstrapNextDNS...),
		}
	default:
		return Upstream{
			Name: ProviderSonCMS,
			Type: "DNS over HTTPS",
			URL:  URLSonCMS,
		}
	}
}

func (c *Config) CurrentProvider() string {
	if len(c.Upstreams) > 0 {
		u := strings.ToLower(c.Upstreams[0].URL)
		if strings.Contains(u, "nextdns") {
			return ProviderNextDNS
		}
	}
	return ProviderSonCMS
}

// Accept both the human-friendly "5s" form and a numeric nanosecond value
// emitted by older builds.
func (c *Config) UnmarshalJSON(data []byte) error {
	type alias Config
	decoded := alias(*c)
	aux := struct {
		Timeout             json.RawMessage `json:"timeout"`
		ConnectionRetention json.RawMessage `json:"connection_retention"`
		Upstreams           json.RawMessage `json:"upstreams"`
		*alias
	}{alias: &decoded}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	if len(aux.Upstreams) != 0 {
		// Unmarshal reuses slice elements, retaining omitted fields. Decode an
		// explicitly supplied list from scratch so a custom URL cannot inherit
		// the default provider's bootstrap addresses, name, or DNSSEC flag.
		var upstreams []Upstream
		if err := json.Unmarshal(aux.Upstreams, &upstreams); err != nil {
			return err
		}
		decoded.Upstreams = upstreams
	}
	if len(aux.Timeout) != 0 && string(aux.Timeout) != "null" {
		var text string
		if json.Unmarshal(aux.Timeout, &text) == nil {
			d, err := time.ParseDuration(text)
			if err != nil {
				return fmt.Errorf("invalid timeout %q: %w", text, err)
			}
			decoded.Timeout = d
		} else {
			var nanos int64
			if err := json.Unmarshal(aux.Timeout, &nanos); err != nil {
				return fmt.Errorf("timeout must be a duration string: %w", err)
			}
			decoded.Timeout = time.Duration(nanos)
		}
	}
	if len(aux.ConnectionRetention) != 0 && string(aux.ConnectionRetention) != "null" {
		var text string
		if json.Unmarshal(aux.ConnectionRetention, &text) == nil {
			d, err := time.ParseDuration(text)
			if err != nil {
				return fmt.Errorf("invalid connection_retention %q: %w", text, err)
			}
			decoded.ConnectionRetention = d
		} else {
			var nanos int64
			if err := json.Unmarshal(aux.ConnectionRetention, &nanos); err != nil {
				return fmt.Errorf("connection_retention must be a duration string: %w", err)
			}
			decoded.ConnectionRetention = time.Duration(nanos)
		}
	}
	*c = Config(decoded)
	return nil
}

func (c Config) MarshalJSON() ([]byte, error) {
	type alias Config
	return json.Marshal(struct {
		Timeout             string `json:"timeout"`
		ConnectionRetention string `json:"connection_retention"`
		alias
	}{Timeout: c.Timeout.String(), ConnectionRetention: c.ConnectionRetention.String(), alias: alias(c)})
}

func DefaultConfig() Config {
	return Config{
		Listen:       "127.0.0.1:5300",
		Interception: "windivert",
		DPI: DPIConfig{
			Enabled:     true,
			Backend:     "embedded-zapret2",
			CaptureMode: "default",
			FailOpen:    true,
			AutoRestart: true,
		},
		Timeout:             5 * time.Second,
		ConnectionRetention: 7 * 24 * time.Hour,
		LogFile:             defaultLogPath(),
		Upstreams: []Upstream{
			PredefinedUpstream(ProviderSonCMS),
		},
		BootstrapDNS: append([]string(nil), DefaultBootstrapDNS...),
	}
}

func defaultLogPath() string {
	base := os.Getenv("ProgramData")
	if base == "" {
		base = "."
	}
	return filepath.Join(base, "SonDNS", "sondns.log")
}

func (c *Config) normalize() error {
	if c.Listen == "" {
		c.Listen = "127.0.0.1:5300"
	}
	if _, _, err := net.SplitHostPort(c.Listen); err != nil {
		return fmt.Errorf("listen must be host:port: %w", err)
	}
	if c.Timeout <= 0 {
		c.Timeout = 5 * time.Second
	}
	if c.ConnectionRetention <= 0 {
		c.ConnectionRetention = 7 * 24 * time.Hour
	}
	if strings.TrimSpace(c.LogFile) == "" {
		c.LogFile = defaultLogPath()
	}
	if len(c.BootstrapDNS) == 0 {
		c.BootstrapDNS = append([]string(nil), DefaultBootstrapDNS...)
	}
	c.BootstrapDNS = normalizeBootstrapDNS(c.BootstrapDNS)
	if len(c.Upstreams) == 0 {
		return errors.New("at least one encrypted DNS upstream is required")
	}
	for i := range c.Upstreams {
		u, err := url.Parse(c.Upstreams[i].URL)
		if err != nil || !supportedUpstreamScheme(u.Scheme) || (u.Scheme != "sdns" && u.Hostname() == "") {
			return fmt.Errorf("upstreams[%d].url must be an https://, tls://, quic://, or sdns:// endpoint", i)
		}
		if u.Scheme == "https" && u.Path == "" {
			c.Upstreams[i].URL += "/dns-query"
		}
		if c.Upstreams[i].Type == "" {
			switch u.Scheme {
			case "tls":
				c.Upstreams[i].Type = "DNS over TLS"
			case "quic":
				c.Upstreams[i].Type = "DNS over QUIC"
			case "sdns":
				c.Upstreams[i].Type = "DNS stamp"
			default:
				c.Upstreams[i].Type = "DNS over HTTPS"
			}
		}
		if c.Upstreams[i].Name == "" {
			c.Upstreams[i].Name = u.Hostname()
			if c.Upstreams[i].Name == "" {
				c.Upstreams[i].Name = "DNS stamp"
			}
		}
		if len(c.Upstreams[i].Bootstrap) == 0 {
			c.Upstreams[i].Bootstrap = bootstrapForHost(u.Hostname())
		}
		for _, addr := range c.Upstreams[i].Bootstrap {
			if _, _, err := net.SplitHostPort(addr); err != nil {
				return fmt.Errorf("upstreams[%d].bootstrap contains invalid address %q", i, addr)
			}
		}
	}
	c.Interception = strings.ToLower(strings.TrimSpace(c.Interception))
	if c.Interception == "" {
		c.Interception = "windivert"
	}
	if c.Interception != "windivert" {
		return fmt.Errorf("interception must be windivert")
	}
	c.DPI.Backend = strings.ToLower(strings.TrimSpace(c.DPI.Backend))
	if c.DPI.Backend == "" {
		c.DPI.Backend = "embedded-zapret2"
	}
	if c.DPI.Backend != "embedded-zapret2" && c.DPI.Backend != "external" {
		return fmt.Errorf("dpi_bypass.backend must be embedded-zapret2 or external")
	}
	c.DPI.CaptureMode = strings.ToLower(strings.TrimSpace(c.DPI.CaptureMode))
	if c.DPI.CaptureMode == "" {
		c.DPI.CaptureMode = "default"
	}
	if c.DPI.CaptureMode != "default" && c.DPI.CaptureMode != "tcp" && c.DPI.CaptureMode != "tcp+udp" {
		return fmt.Errorf("dpi_bypass.capture_mode must be default, tcp, or tcp+udp")
	}
	if !c.DPI.FailOpen {
		// Fail-open is mandatory for the first integration: a helper failure
		// must not turn into a system-wide connectivity outage.
		c.DPI.FailOpen = true
	}
	for i, target := range c.DPI.Targets {
		c.DPI.Targets[i] = strings.TrimSpace(target)
	}
	for i, target := range c.DPI.Exclude {
		c.DPI.Exclude[i] = strings.TrimSpace(target)
	}
	return nil
}

func normalizeBootstrapDNS(list []string) []string {
	res := make([]string, 0, len(list))
	for _, s := range list {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, _, err := net.SplitHostPort(s); err != nil {
			if strings.Contains(s, ":") && !strings.HasPrefix(s, "[") {
				s = fmt.Sprintf("[%s]:53", s)
			} else {
				s = net.JoinHostPort(s, "53")
			}
		}
		res = append(res, s)
	}
	return res
}

func supportedUpstreamScheme(scheme string) bool {
	switch strings.ToLower(scheme) {
	case "https", "tls", "quic", "sdns":
		return true
	default:
		return false
	}
}

// bootstrapForHost keeps the first connection to a well-known DoH provider
// independent from the machine's DNS setting. This matters when WinDivert is
// enabled: resolving the DoH hostname through the same proxy would recurse.
func bootstrapForHost(host string) []string {
	switch strings.TrimSuffix(strings.ToLower(host), ".") {
	case "dns.nextdns.io":
		return append([]string(nil), BootstrapNextDNS...)
	case "cloudflare-dns.com", "one.one.one.one":
		return []string{"1.1.1.1:443", "1.0.0.1:443"}
	case "dns.google", "dns.google.com":
		return []string{"8.8.8.8:443", "8.8.4.4:443"}
	case "dns.quad9.net":
		return []string{"9.9.9.9:443", "149.112.112.112:443"}
	case "dns.adguard-dns.com":
		return []string{"94.140.14.14:443", "94.140.15.15:443"}
	default:
		return nil
	}
}

func LoadConfig(path string) (Config, error) {
	c := DefaultConfig()
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return Config{}, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return Config{}, fmt.Errorf("parse config %s: %w", path, err)
	}
	if err := c.normalize(); err != nil {
		return Config{}, err
	}
	return c, nil
}

func SaveConfig(path string, c Config) error {
	if err := c.normalize(); err != nil {
		return err
	}
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}
