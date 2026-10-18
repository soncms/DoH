package config

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

	"github.com/ameshkov/dnsstamps"
)

// Config is deliberately JSON so the service has no third-party config parser
// and can be edited from a shell or deployment script.
type Config struct {
	Listen           string        `json:"listen"`
	Interception     string        `json:"interception"`
	DPI              DPIConfig     `json:"dpi_bypass,omitempty"`
	Upstreams        []Upstream    `json:"upstreams"`
	BootstrapDNS     []string      `json:"bootstrap_dns,omitempty"`
	Timeout          time.Duration `json:"timeout"`
	LogFile          string        `json:"log_file"`
	ServiceAutoStart bool          `json:"service_auto_start"`
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
	ProviderCustom  = "Custom (saved configuration)"

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
	if len(c.Upstreams) != 1 {
		return ProviderCustom
	}
	switch c.Upstreams[0].URL {
	case URLSonCMS:
		return ProviderSonCMS
	case URLNextDNS:
		return ProviderNextDNS
	default:
		return ProviderCustom
	}
}

// Accept both the human-friendly "5s" form and a numeric nanosecond value
// emitted by older builds.
func (c *Config) UnmarshalJSON(data []byte) error {
	type alias Config
	decoded := alias(*c)
	aux := struct {
		Timeout   json.RawMessage `json:"timeout"`
		Upstreams json.RawMessage `json:"upstreams"`
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
	*c = Config(decoded)
	return nil
}

func (c Config) MarshalJSON() ([]byte, error) {
	type alias Config
	return json.Marshal(struct {
		Timeout string `json:"timeout"`
		alias
	}{Timeout: c.Timeout.String(), alias: alias(c)})
}

func Default() Config {
	return Config{
		Listen:       "127.0.0.1:5300",
		Interception: "windivert",
		DPI: DPIConfig{
			Enabled:     false,
			Backend:     "embedded-zapret2",
			CaptureMode: "default",
			FailOpen:    true,
			AutoRestart: true,
		},
		Timeout: 5 * time.Second,
		LogFile: DefaultLogPath(),
		Upstreams: []Upstream{
			PredefinedUpstream(ProviderSonCMS),
		},
		BootstrapDNS: append([]string(nil), DefaultBootstrapDNS...),
	}
}

func DefaultLogPath() string {
	return filepath.Join("data", "sondns.log")
}

func (c *Config) Normalize() error {
	if err := c.normalizeNetwork(); err != nil {
		return err
	}
	items, err := NormalizeUpstreams(c.Upstreams)
	if err != nil {
		return err
	}
	c.Upstreams = items
	return c.DPI.normalize()
}

// Clone keeps candidate settings independent while an asynchronous GUI action
// saves, applies, or rolls them back.
func (c Config) Clone() Config {
	c.BootstrapDNS = append([]string(nil), c.BootstrapDNS...)
	c.Upstreams = append([]Upstream(nil), c.Upstreams...)
	for i := range c.Upstreams {
		c.Upstreams[i].Bootstrap = append([]string(nil), c.Upstreams[i].Bootstrap...)
	}
	c.DPI.Arguments = append([]string(nil), c.DPI.Arguments...)
	c.DPI.Targets = append([]string(nil), c.DPI.Targets...)
	c.DPI.Exclude = append([]string(nil), c.DPI.Exclude...)
	return c
}

func (c *Config) normalizeNetwork() error {
	if c.Listen == "" {
		c.Listen = "127.0.0.1:5300"
	}
	if _, _, err := net.SplitHostPort(c.Listen); err != nil {
		return fmt.Errorf("listen must be host:port: %w", err)
	}
	if c.Timeout <= 0 {
		c.Timeout = 5 * time.Second
	}
	if strings.TrimSpace(c.LogFile) == "" {
		c.LogFile = DefaultLogPath()
	}
	c.LogFile = LogPath(c.LogFile)
	if len(c.BootstrapDNS) == 0 {
		c.BootstrapDNS = append([]string(nil), DefaultBootstrapDNS...)
	}
	c.BootstrapDNS = normalizeBootstrapDNS(c.BootstrapDNS)
	c.Interception = strings.ToLower(strings.TrimSpace(c.Interception))
	if c.Interception == "" {
		c.Interception = "windivert"
	}
	if c.Interception != "windivert" {
		return fmt.Errorf("interception must be windivert")
	}
	return nil
}

// Both disk configuration and IPC updates pass through this validator before
// bootstrap lookup or creation of any upstream transport.
func NormalizeUpstreams(items []Upstream) ([]Upstream, error) {
	if len(items) == 0 {
		return nil, errors.New("at least one encrypted DNS upstream is required")
	}
	result := append([]Upstream(nil), items...)
	for i := range result {
		if err := normalizeUpstream(&result[i]); err != nil {
			return nil, fmt.Errorf("upstreams[%d]: %w", i, err)
		}
	}
	return result, nil
}

func normalizeUpstream(item *Upstream) error {
	u, err := url.Parse(strings.TrimSpace(item.URL))
	if err != nil || !supportedUpstreamScheme(u.Scheme) || (u.Scheme != "sdns" && u.Hostname() == "") {
		return errors.New("url must be an https://, tls://, quic://, or sdns:// endpoint")
	}
	u.Scheme = strings.ToLower(u.Scheme)
	if u.Scheme == "sdns" {
		stamp, err := dnsstamps.NewServerStampFromString(u.String())
		if err != nil {
			return fmt.Errorf("invalid DNS stamp: %w", err)
		}
		switch stamp.Proto {
		case dnsstamps.StampProtoTypeDNSCrypt, dnsstamps.StampProtoTypeDoH, dnsstamps.StampProtoTypeTLS, dnsstamps.StampProtoTypeDoQ:
		default:
			return errors.New("DNS stamp must use an encrypted protocol")
		}
	}
	if u.Scheme == "https" && u.Path == "" {
		u.Path = "/dns-query"
	}
	item.URL = u.String()
	if item.Type == "" {
		item.Type = map[string]string{"https": "DNS over HTTPS", "tls": "DNS over TLS", "quic": "DNS over QUIC", "sdns": "DNS stamp"}[u.Scheme]
	}
	if item.Name == "" {
		item.Name = u.Hostname()
		if item.Name == "" {
			item.Name = "DNS stamp"
		}
	}
	item.Bootstrap = append([]string(nil), item.Bootstrap...)
	if len(item.Bootstrap) == 0 {
		item.Bootstrap = BootstrapForHost(u.Hostname())
	}
	for _, addr := range item.Bootstrap {
		if _, _, err := net.SplitHostPort(addr); err != nil {
			return fmt.Errorf("bootstrap contains invalid address %q", addr)
		}
	}
	return nil
}

func (c *DPIConfig) normalize() error {
	c.Backend = strings.ToLower(strings.TrimSpace(c.Backend))
	if c.Backend == "" {
		c.Backend = "embedded-zapret2"
	}
	if c.Backend != "embedded-zapret2" && c.Backend != "external" {
		return fmt.Errorf("dpi_bypass.backend must be embedded-zapret2 or external")
	}
	c.CaptureMode = strings.ToLower(strings.TrimSpace(c.CaptureMode))
	if c.CaptureMode == "" {
		c.CaptureMode = "default"
	}
	if c.CaptureMode != "default" && c.CaptureMode != "tcp" && c.CaptureMode != "tcp+udp" {
		return fmt.Errorf("dpi_bypass.capture_mode must be default, tcp, or tcp+udp")
	}
	if !c.FailOpen {
		// Fail-open is mandatory for the first integration: a helper failure
		// must not turn into a system-wide connectivity outage.
		c.FailOpen = true
	}
	for i, target := range c.Targets {
		c.Targets[i] = strings.TrimSpace(target)
	}
	for i, target := range c.Exclude {
		c.Exclude[i] = strings.TrimSpace(target)
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
func BootstrapForHost(host string) []string {
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

func Load(path string) (Config, error) {
	c := Default()
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
	if err := c.Normalize(); err != nil {
		return Config{}, err
	}
	return c, nil
}

func Save(path string, c Config) error {
	c = c.Clone()
	if err := c.Normalize(); err != nil {
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
	f, err := os.CreateTemp(filepath.Dir(path), ".sondns-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(append(b, '\n')); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func LogPath(configured string) string {
	// An old absolute log path must not make the portable build write back into
	// ProgramData or the old installation. Keep that setting local in memory.
	if !filepath.IsLocal(configured) || strings.Contains(configured, ":") {
		return filepath.Join("data", "sondns.log")
	}
	return filepath.Clean(configured)
}

func DNSSECEnabled(c Config) bool {
	for _, u := range c.Upstreams {
		if u.DNSSEC {
			return true
		}
	}
	return false
}
