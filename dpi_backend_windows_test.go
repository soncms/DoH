//go:build windows

package main

import (
	"strings"
	"testing"
)

func TestZapret2CaptureModes(t *testing.T) {
	for _, tc := range []struct {
		mode string
		want []string
		miss []string
	}{
		{mode: "default", want: []string{"--wf-tcp-out=80,443", "--wf-filter-lan=1", "--wf-filter-loopback=1"}, miss: []string{"--wf-udp-out=443"}},
		{mode: "tcp", want: []string{"--wf-tcp-out=1-52,54-65535", "--filter-tcp=1-65535"}, miss: []string{"--wf-udp-out=443"}},
		{mode: "tcp+udp", want: []string{"--wf-tcp-out=1-52,54-65535", "--wf-udp-out=443"}, miss: nil},
	} {
		args, err := zapret2Arguments(t.TempDir(), DPIConfig{CaptureMode: tc.mode})
		if err != nil {
			t.Fatalf("mode %q: %v", tc.mode, err)
		}
		joined := strings.Join(args, "\x00")
		for _, want := range tc.want {
			if !strings.Contains(joined, want) {
				t.Errorf("mode %q missing %q", tc.mode, want)
			}
		}
		for _, miss := range tc.miss {
			if strings.Contains(joined, miss) {
				t.Errorf("mode %q unexpectedly contains %q", tc.mode, miss)
			}
		}
	}
}
