package main

import (
	"encoding/binary"
	"net"
	"testing"

	"github.com/miekg/dns"
)

func TestDNSDatagramIPv4Reply(t *testing.T) {
	query := new(dns.Msg)
	query.SetQuestion("example.com.", dns.TypeA)
	wire, err := query.Pack()
	if err != nil {
		t.Fatal(err)
	}
	raw := make([]byte, 20+8+len(wire))
	raw[0], raw[8], raw[9] = 0x45, 64, 17
	binary.BigEndian.PutUint16(raw[2:4], uint16(len(raw)))
	binary.BigEndian.PutUint16(raw[20:22], 5353)
	binary.BigEndian.PutUint16(raw[22:24], 53)
	binary.BigEndian.PutUint16(raw[24:26], uint16(8+len(wire)))
	copy(raw[12:16], net.IPv4(192, 168, 1, 20).To4())
	copy(raw[16:20], net.IPv4(192, 168, 1, 1).To4())
	copy(raw[28:], wire)

	datagram, err := parseDNSDatagram(raw)
	if err != nil {
		t.Fatal(err)
	}
	if datagram.source != "192.168.1.20:5353" || datagram.dest != "192.168.1.1:53" {
		t.Fatalf("packet endpoints = %q -> %q", datagram.source, datagram.dest)
	}
	response, err := datagram.reply(wire)
	if err != nil {
		t.Fatal(err)
	}
	if got := net.IP(response[12:16]).String(); got != "192.168.1.1" {
		t.Fatalf("response source = %s", got)
	}
	if got := net.IP(response[16:20]).String(); got != "192.168.1.20" {
		t.Fatalf("response destination = %s", got)
	}
	if got := binary.BigEndian.Uint16(response[20:22]); got != 53 {
		t.Fatalf("response source port = %d", got)
	}
	if got := binary.BigEndian.Uint16(response[22:24]); got != 5353 {
		t.Fatalf("response destination port = %d", got)
	}
}

func TestDNSDatagramIPv6Reply(t *testing.T) {
	query := new(dns.Msg)
	query.SetQuestion("example.com.", dns.TypeAAAA)
	wire, err := query.Pack()
	if err != nil {
		t.Fatal(err)
	}
	raw := make([]byte, 40+8+len(wire))
	raw[0], raw[6], raw[7] = 0x60, 17, 64
	binary.BigEndian.PutUint16(raw[4:6], uint16(8+len(wire)))
	binary.BigEndian.PutUint16(raw[40:42], 5353)
	binary.BigEndian.PutUint16(raw[42:44], 53)
	binary.BigEndian.PutUint16(raw[44:46], uint16(8+len(wire)))
	copy(raw[8:24], net.ParseIP("2001:db8::20").To16())
	copy(raw[24:40], net.ParseIP("2001:db8::1").To16())
	copy(raw[48:], wire)

	datagram, err := parseDNSDatagram(raw)
	if err != nil {
		t.Fatal(err)
	}
	if datagram.source != "[2001:db8::20]:5353" || datagram.dest != "[2001:db8::1]:53" {
		t.Fatalf("packet endpoints = %q -> %q", datagram.source, datagram.dest)
	}
	response, err := datagram.reply(wire)
	if err != nil {
		t.Fatal(err)
	}
	if got := net.IP(response[8:24]).String(); got != "2001:db8::1" {
		t.Fatalf("response source = %s", got)
	}
	if got := net.IP(response[24:40]).String(); got != "2001:db8::20" {
		t.Fatalf("response destination = %s", got)
	}
}
