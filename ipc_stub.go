//go:build !windows

package main

import "errors"

type dnsIPCServer struct{}

type dnsEngineProxy interface {
	ClearCache()
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
	OK      bool              `json:"ok"`
	Version uint64            `json:"version,omitempty"`
	Entries []DNSRequestEntry `json:"entries,omitempty"`
	Error   string            `json:"error,omitempty"`
}

func callDNSIPC(dnsIPCRequest) (dnsIPCResponse, error) {
	return dnsIPCResponse{}, errors.New("SonDNS IPC is only available on Windows")
}
