//go:build windows && !console

package service

import (
	"errors"
	"strings"
	"testing"

	"golang.org/x/sys/windows/svc"
	appconfig "sondns/internal/config"
	"sondns/internal/control"
)

func TestProviderApplyFailureAndOfflineSemantics(t *testing.T) {
	for _, scenario := range []string{"running", "stopped", "rejected", "timeout", "save_failed", "rollback_failed", "query_failed"} {
		t.Run(scenario, func(t *testing.T) {
			previous := appconfig.Default()
			candidate := previous.Clone()
			candidate.Upstreams = []appconfig.Upstream{appconfig.PredefinedUpstream(appconfig.ProviderNextDNS)}
			var saved []appconfig.Config
			called := false
			got, message, err := ApplyDNSProvider(previous, candidate, func(c appconfig.Config) error {
				if scenario == "save_failed" || scenario == "rollback_failed" && len(saved) == 1 {
					return errors.New("disk failure")
				}
				saved = append(saved, c.Clone())
				return nil
			}, func() (ServiceSnapshot, error) {
				if scenario == "query_failed" {
					return ServiceSnapshot{}, errors.New("service belongs to another folder")
				}
				return ServiceSnapshot{Exists: true, State: map[bool]svc.State{true: svc.Stopped, false: svc.Running}[scenario == "stopped"]}, nil
			}, func(control.Request) (control.Response, error) {
				called = true
				if scenario == "rejected" || scenario == "rollback_failed" {
					return control.Response{}, &control.ResponseError{Message: "rejected"}
				}
				if scenario == "timeout" {
					return control.Response{}, errors.New("deadline exceeded")
				}
				return control.Response{OK: true}, nil
			})
			want := appconfig.ProviderNextDNS
			if scenario == "rejected" || scenario == "save_failed" || scenario == "query_failed" {
				want = appconfig.ProviderSonCMS
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
			if scenario == "rejected" && (len(saved) != 2 || saved[1].CurrentProvider() != appconfig.ProviderSonCMS) {
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
