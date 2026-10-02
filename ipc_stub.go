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
	Op        string     `json:"op"`
	Version   uint64     `json:"version,omitempty"`
	Upstreams []Upstream `json:"upstreams,omitempty"`
}
type dnsIPCResponse struct {
	OK              bool              `json:"ok"`
	SchemaVersion   int               `json:"schema_version,omitempty"`
	Version         uint64            `json:"version,omitempty"`
	OverwrittenRows uint64            `json:"overwritten_rows,omitempty"`
	Entries         []DNSRequestEntry `json:"entries,omitempty"`
	Error           string            `json:"error,omitempty"`
}

func callDNSIPC(dnsIPCRequest) (dnsIPCResponse, error) {
	return dnsIPCResponse{}, errors.New("SonDNS IPC is only available on Windows")
}
