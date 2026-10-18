//go:build !console

package main

import (
	"net"
	"testing"
)

// Windows may reserve different port ranges for TCP and UDP. A port selected
// for one protocol is not necessarily usable for the other. Hold both sockets
// before returning, and release every partial attempt rather than skipping DNS
// coverage or changing system port reservations.
func listenTestDNSPair(t testing.TB) (net.PacketConn, net.Listener) {
	t.Helper()
	var lastErr error
	for range 32 {
		tcp, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		packet, err := net.ListenPacket("udp", tcp.Addr().String())
		if err == nil {
			t.Cleanup(func() { _ = packet.Close(); _ = tcp.Close() })
			return packet, tcp
		}
		lastErr = err
		_ = tcp.Close()
	}
	t.Fatalf("cannot allocate a loopback TCP/UDP pair: %v", lastErr)
	return nil, nil
}
