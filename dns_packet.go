package main

import (
	"encoding/binary"
	"errors"
	"fmt"
)

type dnsDatagram struct {
	raw       []byte
	payload   []byte
	udpOffset int
	ipv6      bool
}

func parseDNSDatagram(raw []byte) (dnsDatagram, error) {
	p := dnsDatagram{raw: raw}
	if len(raw) < 20 {
		return p, errors.New("short IP packet")
	}
	end := 0
	switch raw[0] >> 4 {
	case 4:
		p.udpOffset = int(raw[0]&15) * 4
		end = int(binary.BigEndian.Uint16(raw[2:4]))
		if p.udpOffset < 20 || end < p.udpOffset || end > len(raw) || raw[9] != 17 {
			return p, errors.New("invalid IPv4 UDP packet")
		}
		if binary.BigEndian.Uint16(raw[6:8])&0x3fff != 0 {
			return p, errors.New("fragmented IPv4 packet")
		}
	case 6:
		if len(raw) < 40 {
			return p, errors.New("short IPv6 packet")
		}
		p.ipv6 = true
		end = 40 + int(binary.BigEndian.Uint16(raw[4:6]))
		if end > len(raw) {
			return p, errors.New("invalid IPv6 payload length")
		}
		p.udpOffset = 40
		next := raw[6]
		for steps := 0; next != 17; steps++ {
			if steps >= 8 || p.udpOffset+8 > end {
				return p, errors.New("invalid IPv6 extension chain")
			}
			ext := raw[p.udpOffset:]
			length := 0
			switch next {
			case 0, 43, 60: // hop-by-hop, routing, destination options
				length = (int(ext[1]) + 1) * 8
			case 44: // permit an atomic fragment, but not a fragmented datagram
				if binary.BigEndian.Uint16(ext[2:4])&0xfff9 != 0 {
					return p, errors.New("fragmented IPv6 packet")
				}
				length = 8
			case 51: // authentication header
				length = (int(ext[1]) + 2) * 4
			default:
				return p, fmt.Errorf("unsupported IPv6 next header %d", next)
			}
			next = ext[0]
			p.udpOffset += length
		}
	default:
		return p, errors.New("unsupported IP version")
	}
	if p.udpOffset+8 > end {
		return p, errors.New("short UDP header")
	}
	udp := raw[p.udpOffset:end]
	length := int(binary.BigEndian.Uint16(udp[4:6]))
	if binary.BigEndian.Uint16(udp[2:4]) != 53 || length < 20 || length > len(udp) {
		return p, errors.New("invalid UDP DNS query")
	}
	p.raw = raw[:end]
	p.payload = udp[8:length]
	return p, nil
}

func (p dnsDatagram) reply(wire []byte) ([]byte, error) {
	length := p.udpOffset + 8 + len(wire)
	if len(wire) < 12 || length > 65535 {
		return nil, errors.New("invalid DNS response length")
	}
	raw := make([]byte, length)
	copy(raw, p.raw[:p.udpOffset+8])
	if p.ipv6 {
		copy(raw[8:24], p.raw[24:40])
		copy(raw[24:40], p.raw[8:24])
		binary.BigEndian.PutUint16(raw[4:6], uint16(length-40))
		raw[7] = 64
	} else {
		copy(raw[12:16], p.raw[16:20])
		copy(raw[16:20], p.raw[12:16])
		binary.BigEndian.PutUint16(raw[2:4], uint16(length))
		raw[8] = 64
		raw[10], raw[11] = 0, 0
	}
	udp := raw[p.udpOffset:]
	copy(udp[2:4], p.raw[p.udpOffset:p.udpOffset+2])
	binary.BigEndian.PutUint16(udp[0:2], 53)
	binary.BigEndian.PutUint16(udp[4:6], uint16(8+len(wire)))
	udp[6], udp[7] = 0, 0
	copy(udp[8:], wire)
	return raw, nil
}
