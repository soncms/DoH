//go:build !windows

package main

import "errors"

type dnsIPCServer struct{}

type dnsEngineProxy interface {
	SetUpstreams([]Upstream) error
}

func newDNSIPCServer(*DNSRequestRing, dnsEngineProxy) *dnsIPCServer {
	return &dnsIPCServer{}
}
func (*dnsIPCServer) start() {}
func (*dnsIPCServer) close() {}

type dnsIPCRequest struct {
	Op          string     `json:"op"`
	InstanceID  string     `json:"instance_id,omitempty"`
	Version     uint64     `json:"version,omitempty"`
	Upstreams   []Upstream `json:"upstreams,omitempty"`
	Enabled     *bool      `json:"enabled,omitempty"`
	CaptureMode string     `json:"capture_mode,omitempty"`
}

type dnsIPCState struct {
	DNSRunning    bool   `json:"dns_running"`
	DPIRunning    bool   `json:"dpi_running"`
	DPIEnabled    bool   `json:"dpi_enabled"`
	CaptureMode   string `json:"capture_mode"`
	Provider      string `json:"provider"`
	UptimeSeconds int64  `json:"uptime_seconds"`
	LastError     string `json:"last_error,omitempty"`
	AssetVerified bool   `json:"asset_verified"`
	AssetError    string `json:"asset_error,omitempty"`
}

type dnsIPCConfig struct {
	Provider         string     `json:"provider"`
	Upstreams        []Upstream `json:"upstreams"`
	DPIEnabled       bool       `json:"dpi_enabled"`
	CaptureMode      string     `json:"capture_mode"`
	ServiceAutoStart bool       `json:"service_auto_start"`
}

type dnsIPCDiagnostics struct {
	Version       string                   `json:"version"`
	BinarySHA256  string                   `json:"binary_sha256,omitempty"`
	SignerStatus  string                   `json:"signer_status,omitempty"`
	BuildInfoPath string                   `json:"build_info_path,omitempty"`
	BuildInfo     map[string]any           `json:"build_info,omitempty"`
	Assets        []runtimeAssetDiagnostic `json:"assets,omitempty"`
}

type dnsIPCResponse struct {
	OK              bool               `json:"ok"`
	Unchanged       bool               `json:"unchanged,omitempty"`
	InstanceID      string             `json:"instance_id,omitempty"`
	SchemaVersion   int                `json:"schema_version,omitempty"`
	Version         uint64             `json:"version,omitempty"`
	OverwrittenRows uint64             `json:"overwritten_rows,omitempty"`
	Entries         []DNSRequestEntry  `json:"entries,omitempty"`
	State           *dnsIPCState       `json:"state,omitempty"`
	Config          *dnsIPCConfig      `json:"config,omitempty"`
	Diagnostics     *dnsIPCDiagnostics `json:"diagnostics,omitempty"`
	NeedsRestart    bool               `json:"needs_restart,omitempty"`
	Code            string             `json:"code,omitempty"`
	Error           string             `json:"error,omitempty"`
}

func callDNSIPC(dnsIPCRequest) (dnsIPCResponse, error) {
	return dnsIPCResponse{}, errors.New("SonDNS IPC is only available on Windows")
}
