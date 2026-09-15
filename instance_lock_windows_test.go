//go:build windows

package main

import (
	"testing"
	"time"
)

func TestSonDNSInstanceMutex(t *testing.T) {
	name := "Local\\SonDNS.Test." + time.Now().Format("20060102150405.000000000")
	first, ok, err := acquireProcessMutex(name)
	if err != nil || !ok {
		t.Fatalf("first mutex: %v %v", ok, err)
	}
	defer first.Close()
	second, ok, err := acquireProcessMutex(name)
	if err != nil {
		t.Fatal(err)
	}
	if ok || second != nil {
		t.Fatal("second process acquired mutex")
	}
}
