//go:build windows && !console

package service

import (
	"errors"
	"fmt"

	"golang.org/x/sys/windows/svc"
	appconfig "sondns/internal/config"
	"sondns/internal/control"
)

// Settings changes are serialized by runAction on the UI thread. A transport
// timeout leaves the saved candidate intact because the service may have applied
// it already; a definite server rejection can safely restore the previous file.
func ApplyDNSProvider(previous, candidate appconfig.Config, save func(appconfig.Config) error, query func() (ServiceSnapshot, error), call func(control.Request) (control.Response, error)) (appconfig.Config, string, error) {
	if err := candidate.Normalize(); err != nil {
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
	_, err = call(control.Request{Op: "set_upstream", Upstreams: candidate.Upstreams})
	if err == nil {
		return candidate, "DNS switched to " + provider, nil
	}
	var rejected *control.ResponseError
	if errors.As(err, &rejected) {
		if rollbackErr := save(previous); rollbackErr != nil {
			return candidate, "", fmt.Errorf("DNS change rejected; restoring settings failed: %w", errors.Join(err, rollbackErr))
		}
		return previous, "", fmt.Errorf("DNS change rejected: %w", err)
	}
	return candidate, "", fmt.Errorf("DNS provider saved; service application unconfirmed: %w", err)
}
