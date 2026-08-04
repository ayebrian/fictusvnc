package main

import (
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"
)

func TestParseRFBVersion(t *testing.T) {
	tests := []struct {
		in                 string
		major, minor       int
		ok                 bool
		supported          bool
		supportedIsChecked bool
	}{
		{in: "RFB 003.008\n", major: 3, minor: 8, ok: true, supported: true, supportedIsChecked: true},
		{in: "RFB 003.007\n", major: 3, minor: 7, ok: true, supported: true, supportedIsChecked: true},
		{in: "RFB 003.003\n", major: 3, minor: 3, ok: true, supported: false, supportedIsChecked: true},
		{in: "RFB 004.001\n", major: 4, minor: 1, ok: true, supported: true, supportedIsChecked: true},
		// Malformed: wrong prefix, missing separators, non-digits, bad length.
		{in: "VNC 003.008\n", ok: false},
		{in: "RFB 003_008\n", ok: false},
		{in: "RFB 003.008 ", ok: false},
		{in: "RFB 0O3.008\n", ok: false},
		{in: "RFB 003.008", ok: false},
		{in: "", ok: false},
		{in: "\x00\x01\x02\x03\x04\x05\x06\x07\x08\x09\x0a\x0b", ok: false},
	}

	for _, tt := range tests {
		major, minor, ok := parseRFBVersion([]byte(tt.in))
		if ok != tt.ok {
			t.Errorf("parseRFBVersion(%q): ok=%v want %v", tt.in, ok, tt.ok)
			continue
		}
		if !ok {
			continue
		}
		if major != tt.major || minor != tt.minor {
			t.Errorf("parseRFBVersion(%q): got %d.%d want %d.%d", tt.in, major, minor, tt.major, tt.minor)
		}
		if tt.supportedIsChecked {
			if got := supportedRFBVersion(major, minor); got != tt.supported {
				t.Errorf("supportedRFBVersion(%d, %d): got %v want %v", major, minor, got, tt.supported)
			}
		}
	}
}

// startRawServer exposes a listener without running the client handshake, so
// tests can drive the protocol from the very first byte.
func startRawServer(t *testing.T, f *fb, max int) *vncServer {
	t.Helper()
	srv, err := newVNCServer("127.0.0.1:0", testRotator(f), "test",
		overlayConfig{}, newConnLimiter(max), testLogger())
	if err != nil {
		t.Fatalf("newVNCServer: %v", err)
	}
	t.Cleanup(srv.close)
	go srv.serve()
	return srv
}

func dial(t *testing.T, srv *vncServer) net.Conn {
	t.Helper()
	conn, err := net.Dial("tcp", srv.ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	return conn
}

// A greeting that is not RFB at all must end the connection instead of being
// parsed as if the rest of the stream were a handshake.
func TestMalformedVersionClosesConnection(t *testing.T) {
	srv := startRawServer(t, makeTestFB(32, 32), 0)
	conn := dial(t, srv)

	mustRead(t, conn, make([]byte, 12)) // server greeting
	if _, err := conn.Write([]byte("GET / HTTP/1.1")); err != nil {
		t.Fatalf("write: %v", err)
	}

	if _, err := io.ReadFull(conn, make([]byte, 1)); err == nil {
		t.Fatal("expected the connection to be closed after a malformed version")
	}
}

// RFB 3.3 uses a different security flow, so it is refused rather than served
// a 3.7+ negotiation it cannot parse.
func TestPreRFB37VersionIsRefused(t *testing.T) {
	srv := startRawServer(t, makeTestFB(32, 32), 0)
	conn := dial(t, srv)

	mustRead(t, conn, make([]byte, 12))
	if _, err := conn.Write([]byte("RFB 003.003\n")); err != nil {
		t.Fatalf("write: %v", err)
	}

	if _, err := io.ReadFull(conn, make([]byte, 1)); err == nil {
		t.Fatal("expected the connection to be closed for RFB 3.3")
	}
}

// Picking a security type that was never offered must produce a proper RFB 3.8
// failure result, not a silent success.
func TestUnofferedSecurityTypeIsRejected(t *testing.T) {
	srv := startRawServer(t, makeTestFB(32, 32), 0)
	conn := dial(t, srv)

	mustRead(t, conn, make([]byte, 12))
	if _, err := conn.Write([]byte("RFB 003.008\n")); err != nil {
		t.Fatalf("write: %v", err)
	}

	offered := make([]byte, 2)
	mustRead(t, conn, offered)
	if offered[0] != 1 || offered[1] != secTypeNone {
		t.Fatalf("security types: got %v want [1 %d]", offered, secTypeNone)
	}

	// 2 is VNC Auth, which this server does not offer.
	if _, err := conn.Write([]byte{2}); err != nil {
		t.Fatalf("write: %v", err)
	}

	var status [4]byte
	mustRead(t, conn, status[:])
	if got := binary.BigEndian.Uint32(status[:]); got != 1 {
		t.Fatalf("SecurityResult: got %d want 1 (failed)", got)
	}

	var reasonLen [4]byte
	mustRead(t, conn, reasonLen[:])
	reason := make([]byte, binary.BigEndian.Uint32(reasonLen[:]))
	mustRead(t, conn, reason)
	if len(reason) == 0 {
		t.Error("failure reason must not be empty")
	}

	if _, err := io.ReadFull(conn, make([]byte, 1)); err == nil {
		t.Fatal("expected the connection to be closed after a rejected security type")
	}
}

// The happy path must keep working: 3.8 plus the offered type reaches ServerInit.
func TestValidHandshakeStillSucceeds(t *testing.T) {
	src := makeTestFB(64, 64)
	srv := startRawServer(t, src, 0)
	conn := dial(t, srv)

	handshakeClient(t, conn)

	if _, err := conn.Write(fbUpdateRequest(0)); err != nil {
		t.Fatalf("write request: %v", err)
	}
	readRawUpdate(t, conn, src.w, src.h)
}

func TestConnLimiter(t *testing.T) {
	// A nil limiter never refuses, so the feature can be switched off.
	var unlimited *connLimiter
	for range 100 {
		if !unlimited.acquire() {
			t.Fatal("a nil limiter must always admit")
		}
	}
	unlimited.release()
	if got := unlimited.capacity(); got != 0 {
		t.Errorf("nil capacity: got %d want 0", got)
	}

	// max <= 0 also means unlimited.
	if newConnLimiter(0) != nil || newConnLimiter(-5) != nil {
		t.Error("max <= 0 should produce a nil (unlimited) limiter")
	}

	l := newConnLimiter(2)
	if !l.acquire() || !l.acquire() {
		t.Fatal("the first two slots must be free")
	}
	if l.acquire() {
		t.Fatal("the third acquire must be refused")
	}
	l.release()
	if !l.acquire() {
		t.Fatal("a released slot must become available again")
	}
	if got := l.capacity(); got != 2 {
		t.Errorf("capacity: got %d want 2", got)
	}
}

// Once the cap is reached, further clients are dropped immediately rather than
// each being handed a private framebuffer copy.
func TestConnectionLimitRefusesExtraClients(t *testing.T) {
	src := makeTestFB(32, 32)
	srv := startRawServer(t, src, 1)

	// The first client occupies the only slot and stays connected.
	first := dial(t, srv)
	handshakeClient(t, first)

	second := dial(t, srv)
	second.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadFull(second, make([]byte, 1)); err == nil {
		t.Fatal("expected the second client to be dropped without a greeting")
	}

	// Freeing the slot lets the next client in.
	first.Close()
	var third net.Conn
	for range 50 {
		third = dial(t, srv)
		third.SetReadDeadline(time.Now().Add(time.Second))
		if _, err := io.ReadFull(third, make([]byte, 12)); err == nil {
			return // greeted, so the slot was reclaimed
		}
		third.Close()
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the slot was never released after the first client disconnected")
}
