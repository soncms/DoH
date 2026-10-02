//go:build !windows

package main

import "time"

type ConnectionMonitor struct{}

func NewConnectionMonitor(*ConnectionStore, *DNSRequestRing, interface{ Printf(string, ...interface{}) }, time.Duration) *ConnectionMonitor {
	return nil
}
func (*ConnectionMonitor) Start()                                {}
func (*ConnectionMonitor) Shutdown()                             {}
func (*ConnectionMonitor) Recent(int) ([]ConnectionEntry, error) { return nil, nil }
