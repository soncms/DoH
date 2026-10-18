package packet

import (
	"bytes"
	"encoding/binary"
	"net"
	"strings"
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

	datagram, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	response, err := datagram.Reply(wire)
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

func TestDNSDatagramIPv6ProtectedHeadersBypass(t *testing.T) {
	query := new(dns.Msg)
	query.SetQuestion("sondns-test.invalid.", dns.TypeAAAA)
	wire, err := query.Pack()
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, reason string
		next         byte
		extension    []byte
	}{
		// RFC 4302: ICV binds the original packet and cannot authenticate a
		// synthetic DNS response with changed addresses, UDP header and data.
		{"authentication", "authentication", 51, []byte{17, 4, 0, 0, 0, 0, 0, 1, 0, 0, 0, 1, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}},
		// An active type-2 routing header's base destination is a care-of
		// address, not the logical DNS endpoint in the routing header.
		{"active-routing", "routing", 43, append([]byte{17, 2, 2, 1, 0, 0, 0, 0}, net.ParseIP("2001:db8::53").To16()...)},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw := make([]byte, 40+len(test.extension)+8+len(wire))
			raw[0], raw[6], raw[7] = 0x60, test.next, 64
			binary.BigEndian.PutUint16(raw[4:6], uint16(len(raw)-40))
			copy(raw[8:24], net.ParseIP("2001:db8::20").To16())
			copy(raw[24:40], net.ParseIP("2001:db8::1").To16())
			copy(raw[40:], test.extension)
			offset := 40 + len(test.extension)
			binary.BigEndian.PutUint16(raw[offset:offset+2], 5353)
			binary.BigEndian.PutUint16(raw[offset+2:offset+4], 53)
			binary.BigEndian.PutUint16(raw[offset+4:offset+6], uint16(8+len(wire)))
			copy(raw[offset+8:], wire)
			original := append([]byte(nil), raw...)
			packet, err := Parse(raw)
			if err == nil || !strings.Contains(err.Error(), test.reason) {
				t.Fatalf("protected IPv6 packet must bypass interception: %v", err)
			}
			if !bytes.Equal(packet.Raw, original) {
				t.Fatal("bypass must retain the complete original packet for reinjection")
			}
		})
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

	datagram, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	response, err := datagram.Reply(wire)
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

func TestDNSDatagramIPv6CompletedRoutingReply(t *testing.T) {
	query := new(dns.Msg)
	query.SetQuestion("sondns-test.invalid.", dns.TypeAAAA)
	wire, err := query.Pack()
	if err != nil {
		t.Fatal(err)
	}
	// Type 2, Segments Left=0: the base destination is the final endpoint.
	route := append([]byte{17, 2, 2, 0, 0, 0, 0, 0}, net.ParseIP("2001:db8::1").To16()...)
	offset := 40 + len(route)
	raw := make([]byte, offset+8+len(wire))
	raw[0], raw[6], raw[7] = 0x60, 43, 64
	binary.BigEndian.PutUint16(raw[4:6], uint16(len(raw)-40))
	copy(raw[8:24], net.ParseIP("2001:db8::20").To16())
	copy(raw[24:40], net.ParseIP("2001:db8::1").To16())
	copy(raw[40:], route)
	binary.BigEndian.PutUint16(raw[offset:offset+2], 5353)
	binary.BigEndian.PutUint16(raw[offset+2:offset+4], 53)
	binary.BigEndian.PutUint16(raw[offset+4:offset+6], uint16(8+len(wire)))
	copy(raw[offset+8:], wire)
	packet, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	answer := new(dns.Msg)
	answer.SetReply(query)
	answer.Answer = []dns.RR{&dns.AAAA{
		Hdr:  dns.RR_Header{Name: query.Question[0].Name, Rrtype: dns.TypeAAAA, Class: dns.ClassINET, Ttl: 60},
		AAAA: net.ParseIP("2001:db8::42"),
	}}
	responseWire, err := answer.Pack()
	if err != nil {
		t.Fatal(err)
	}
	response, err := packet.Reply(responseWire)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(response[40:offset], route) || response[6] != 43 ||
		!net.IP(response[8:24]).Equal(net.ParseIP("2001:db8::1")) ||
		!net.IP(response[24:40]).Equal(net.ParseIP("2001:db8::20")) ||
		binary.BigEndian.Uint16(response[4:6]) != uint16(len(response)-40) ||
		binary.BigEndian.Uint16(response[offset:offset+2]) != 53 ||
		binary.BigEndian.Uint16(response[offset+2:offset+4]) != 5353 ||
		binary.BigEndian.Uint16(response[offset+4:offset+6]) != uint16(8+len(responseWire)) ||
		!bytes.Equal(response[offset+8:], responseWire) {
		t.Fatal("completed IPv6 routing header did not retain a valid DNS reply envelope")
	}
}
