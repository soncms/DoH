//go:build windows && !console

package main

import (
	"errors"
	"fmt"

	"golang.org/x/sys/windows/svc"
)

// Settings changes are serialized by runAction on the UI thread. A transport
// timeout leaves the saved candidate intact because the service may have applied
// it already; a definite server rejection can safely restore the previous file.
func applyDNSProvider(previous, candidate Config, save func(Config) error, query func() (serviceSnapshot, error), call func(dnsIPCRequest) (dnsIPCResponse, error)) (Config, string, error) {
	if err := candidate.normalize(); err != nil {
		return previous, "", err
	}
	state, err := query()
	if err != nil {
		return previous, "", fmt.Errorf("check SonDNS service before changing DNS: %w", err)
	}
	if err := save(candidate); err != nil {
		return previous, "", fmt.Errorf("DNS server save failed: %w", err)
	}
	provider := candidate.CurrentProvider()
	if !state.Exists || state.State == svc.Stopped {
		return candidate, "DNS provider saved for next service start: " + provider, nil
	}
	_, err = call(dnsIPCRequest{Op: "set_upstream", Upstreams: candidate.Upstreams})
	if err == nil {
		return candidate, "DNS switched to " + provider, nil
	}
	var rejected *ipcResponseError
	if errors.As(err, &rejected) {
		if rollbackErr := save(previous); rollbackErr != nil {
			return candidate, "", fmt.Errorf("DNS change rejected; restoring settings failed: %w", errors.Join(err, rollbackErr))
		}
		return previous, "", fmt.Errorf("DNS change rejected: %w", err)
	}
	return candidate, "", fmt.Errorf("DNS provider saved; service application unconfirmed: %w", err)
}
