package main

import (
	"encoding/binary"
	"io"
	"net"
)

type pixelFormat struct {
	BPP, Depth             uint8
	BigEndian, TrueColor   uint8
	RMax, GMax, BMax       uint16
	RShift, GShift, BShift uint8
	_                      [3]byte // padding
}

// parseRFBVersion decodes the fixed 12-byte "RFB xxx.yyy\n" handshake string.
// Anything that does not match exactly is rejected rather than guessed at: a
// malformed greeting means the peer is not speaking RFB, and continuing would
// misinterpret every byte that follows.
func parseRFBVersion(b []byte) (major, minor int, ok bool) {
	if len(b) != 12 || string(b[:4]) != "RFB " || b[7] != '.' || b[11] != '\n' {
		return 0, 0, false
	}
	digits := func(s []byte) (int, bool) {
		v := 0
		for _, c := range s {
			if c < '0' || c > '9' {
				return 0, false
			}
			v = v*10 + int(c-'0')
		}
		return v, true
	}
	major, ok = digits(b[4:7])
	if !ok {
		return 0, 0, false
	}
	minor, ok = digits(b[8:11])
	if !ok {
		return 0, 0, false
	}
	return major, minor, true
}

// supportedRFBVersion reports whether the client speaks a dialect that uses
// the 3.7+ security negotiation this server implements. RFB 3.3 puts the
// server in charge of picking the security type and has a different message
// flow, so it is refused rather than mis-served.
func supportedRFBVersion(major, minor int) bool {
	return major > 3 || (major == 3 && minor >= 7)
}

// writeSecurityFailure reports a failed handshake the way RFB 3.8 expects:
// status 1 followed by a length-prefixed reason.
func writeSecurityFailure(c net.Conn, reason string) error {
	if err := write32(c, 1); err != nil {
		return err
	}
	if err := write32(c, uint32(len(reason))); err != nil {
		return err
	}
	_, err := c.Write([]byte(reason))
	return err
}

func write16(c net.Conn, v ...uint16) error {
	for _, x := range v {
		err := binary.Write(c, binary.BigEndian, x)
		if err != nil {
			return err
		}
	}
	return nil
}

func write32(c net.Conn, v uint32) error {
	return binary.Write(c, binary.BigEndian, v)
}

func readN(c net.Conn, n int) (int64, error) {
	return io.CopyN(io.Discard, c, int64(n))
}

func read1(c net.Conn) (byte, error) {
	var b [1]byte
	_, err := io.ReadFull(c, b[:])
	return b[0], err
}

func read16(c net.Conn) (uint16, error) {
	var v uint16
	err := binary.Read(c, binary.BigEndian, &v)
	return v, err
}

func read32(c net.Conn) (uint32, error) {
	var v uint32
	err := binary.Read(c, binary.BigEndian, &v)
	return v, err
}

func sendServerInit(c net.Conn, w, h int, pf pixelFormat, name string) error {
	err := write16(c, uint16(w), uint16(h))
	if err != nil {
		return err
	}

	err = binary.Write(c, binary.BigEndian, pf)
	if err != nil {
		return err
	}

	err = write32(c, uint32(len(name)))
	if err != nil {
		return err
	}

	_, err = c.Write([]byte(name))
	return err
}
