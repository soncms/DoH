//go:build windows && !console

package main

import (
	"errors"
	"strings"
	"testing"

	"golang.org/x/sys/windows/svc"
)

func TestProviderApplyFailureAndOfflineSemantics(t *testing.T) {
	for _, scenario := range []string{"running", "stopped", "rejected", "timeout", "save_failed", "rollback_failed", "query_failed"} {
		t.Run(scenario, func(t *testing.T) {
			previous := DefaultConfig()
			candidate := previous.Clone()
			candidate.Upstreams = []Upstream{PredefinedUpstream(ProviderNextDNS)}
			var saved []Config
			called := false
			got, message, err := applyDNSProvider(previous, candidate, func(c Config) error {
				if scenario == "save_failed" || scenario == "rollback_failed" && len(saved) == 1 {
					return errors.New("disk failure")
				}
				saved = append(saved, c.Clone())
				return nil
			}, func() (serviceSnapshot, error) {
				if scenario == "query_failed" {
					return serviceSnapshot{}, errors.New("service belongs to another folder")
				}
				return serviceSnapshot{Exists: true, State: map[bool]svc.State{true: svc.Stopped, false: svc.Running}[scenario == "stopped"]}, nil
			}, func(dnsIPCRequest) (dnsIPCResponse, error) {
				called = true
				if scenario == "rejected" || scenario == "rollback_failed" {
					return dnsIPCResponse{}, &ipcResponseError{message: "rejected"}
				}
				if scenario == "timeout" {
					return dnsIPCResponse{}, errors.New("deadline exceeded")
				}
				return dnsIPCResponse{OK: true}, nil
			})
			want := ProviderNextDNS
			if scenario == "rejected" || scenario == "save_failed" || scenario == "query_failed" {
				want = ProviderSonCMS
			}
			if got.CurrentProvider() != want {
				t.Fatalf("provider=%s want=%s", got.CurrentProvider(), want)
			}
			if (scenario == "running" || scenario == "stopped") != (err == nil) {
				t.Fatalf("unexpected error: %v", err)
			}
			if scenario == "stopped" && (!strings.Contains(message, "next service start") || called) {
				t.Fatalf("offline handling: called=%v message=%s", called, message)
			}
			if scenario == "timeout" && !strings.Contains(err.Error(), "unconfirmed") {
				t.Fatal("timeout falsely reported success or offline")
			}
			if scenario == "rejected" && (len(saved) != 2 || saved[1].CurrentProvider() != ProviderSonCMS) {
				t.Fatal("server rejection did not roll back disk settings")
			}
			if scenario == "save_failed" && called {
				t.Fatal("failed save still changed live provider")
			}
			if scenario == "query_failed" && (called || len(saved) != 0) {
				t.Fatal("failed ownership/status check still saved settings or called IPC")
			}
		})
	}
}
