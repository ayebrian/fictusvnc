package main

import (
	"encoding/binary"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

func testRotator(f *fb) *ImageRotator {
	return &ImageRotator{
		images:      []WeightedImageData{{fb: f, weight: 1, path: "test.png"}},
		totalWeight: 1,
	}
}

// handshakeClient runs the RFB 3.8 client side up to (and including) reading
// ServerInit, leaving conn positioned at the start of the message loop.
func handshakeClient(t *testing.T, conn net.Conn) {
	t.Helper()
	version := make([]byte, 12)
	mustRead(t, conn, version)

	if _, err := conn.Write([]byte("RFB 003.008\n")); err != nil {
		t.Fatalf("send version: %v", err)
	}

	secTypes := make([]byte, 2) // count + the single "None" type
	mustRead(t, conn, secTypes)
	if _, err := conn.Write([]byte{1}); err != nil { // pick None
		t.Fatalf("pick security type: %v", err)
	}

	mustRead(t, conn, make([]byte, 4)) // SecurityResult

	if _, err := conn.Write([]byte{1}); err != nil { // ClientInit, shared
		t.Fatalf("client init: %v", err)
	}

	head := make([]byte, 24) // width, height, pixel format, name length
	mustRead(t, conn, head)
	nameLen := binary.BigEndian.Uint32(head[20:24])
	mustRead(t, conn, make([]byte, nameLen))
}

func fbUpdateRequest(incremental byte) []byte {
	msg := make([]byte, 10)
	msg[0] = msgFramebufferUpdateReq
	msg[1] = incremental
	return msg
}

// startTestServer spins up a listener backed by a single-image rotator and
// returns a connected, fully handshaked client.
func startTestServer(t *testing.T, f *fb) net.Conn {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		c, err := ln.Accept()
		if err != nil {
			return
		}
		serveWithRotator(c, testRotator(f), "test", false)
	}()
	t.Cleanup(wg.Wait)

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { conn.Close() })

	handshakeClient(t, conn)
	return conn
}

// readRawUpdate consumes one raw-encoded FramebufferUpdate.
func readRawUpdate(t *testing.T, conn net.Conn, w, h int) {
	t.Helper()
	hdr := make([]byte, 4)
	mustRead(t, conn, hdr)
	if hdr[0] != 0 {
		t.Fatalf("msg type: got %d want 0", hdr[0])
	}
	if n := binary.BigEndian.Uint16(hdr[2:4]); n != 1 {
		t.Fatalf("num rects: got %d want 1", n)
	}
	rect := make([]byte, 12)
	mustRead(t, conn, rect)
	if e := binary.BigEndian.Uint32(rect[8:12]); e != encRaw {
		t.Fatalf("encoding: got %d want raw", e)
	}
	mustRead(t, conn, make([]byte, w*h*4))
}

// A static framebuffer has nothing to send for an incremental request. Answering
// one anyway makes clients re-request in a tight loop, so only the first update
// is unconditional.
func TestIncrementalRequestGetsNoUpdate(t *testing.T) {
	src := makeTestFB(64, 64)
	conn := startTestServer(t, src)

	// The first request is honoured even though it is flagged incremental, so
	// a client never sits in front of a blank screen.
	if _, err := conn.Write(fbUpdateRequest(1)); err != nil {
		t.Fatalf("write request: %v", err)
	}
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	readRawUpdate(t, conn, src.w, src.h)

	// Every later incremental request must be silently ignored.
	for i := 0; i < 3; i++ {
		if _, err := conn.Write(fbUpdateRequest(1)); err != nil {
			t.Fatalf("write request: %v", err)
		}
	}
	conn.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	var b [1]byte
	_, err := conn.Read(b[:])
	netErr, ok := err.(net.Error)
	if !ok || !netErr.Timeout() {
		t.Fatalf("expected no reply to incremental requests, got byte=%v err=%v", b[0], err)
	}

	// A full (non-incremental) request still works after that.
	if _, err := conn.Write(fbUpdateRequest(0)); err != nil {
		t.Fatalf("write request: %v", err)
	}
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	readRawUpdate(t, conn, src.w, src.h)
}

// An unknown message has an unknown length, so the stream cannot be resynced
// and the connection must be dropped rather than misparsed.
func TestUnknownMessageClosesConnection(t *testing.T) {
	conn := startTestServer(t, makeTestFB(64, 64))

	if _, err := conn.Write([]byte{200, 0, 0, 0}); err != nil {
		t.Fatalf("write: %v", err)
	}
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadFull(conn, make([]byte, 1)); err == nil {
		t.Fatal("expected the connection to be closed")
	}
}

// An oversized ClientCutText length must not be honoured: on 32-bit builds
// int(n) would go negative and desync the stream.
func TestOversizedCutTextClosesConnection(t *testing.T) {
	conn := startTestServer(t, makeTestFB(64, 64))

	msg := make([]byte, 8)
	msg[0] = msgClientCutText
	binary.BigEndian.PutUint32(msg[4:8], 0xFFFFFFFF)
	if _, err := conn.Write(msg); err != nil {
		t.Fatalf("write: %v", err)
	}
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadFull(conn, make([]byte, 1)); err == nil {
		t.Fatal("expected the connection to be closed")
	}
}

// A cut text within the cap is drained without disturbing the message loop.
func TestCutTextWithinLimitIsDrained(t *testing.T) {
	src := makeTestFB(64, 64)
	conn := startTestServer(t, src)

	msg := make([]byte, 8, 8+5)
	msg[0] = msgClientCutText
	binary.BigEndian.PutUint32(msg[4:8], 5)
	msg = append(msg, []byte("hello")...)
	if _, err := conn.Write(msg); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := conn.Write(fbUpdateRequest(0)); err != nil {
		t.Fatalf("write request: %v", err)
	}
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	readRawUpdate(t, conn, src.w, src.h)
}

func TestClientIPHandlesIPv6(t *testing.T) {
	tests := []struct{ addr, want string }{
		{"192.0.2.10:54321", "192.0.2.10"},
		{"[2001:db8::1]:54321", "2001:db8::1"},
		{"[::1]:5900", "::1"},
	}
	for _, tt := range tests {
		addr, err := net.ResolveTCPAddr("tcp", tt.addr)
		if err != nil {
			t.Fatalf("resolve %s: %v", tt.addr, err)
		}
		if got := clientIP(addr); got != tt.want {
			t.Errorf("clientIP(%s): got %q want %q", tt.addr, got, tt.want)
		}
	}
}

// Sequential rotation must hand a distinct image to each concurrent caller;
// a non-atomic load-then-increment repeats indices under load.
func TestSequentialRotationIsAtomic(t *testing.T) {
	const n = 64
	images := make([]WeightedImageData, n)
	for i := range images {
		images[i] = WeightedImageData{fb: &fb{w: 1, h: 1, data: make([]byte, 4)}, weight: 1}
	}
	r := &ImageRotator{images: images, totalWeight: n, mode: "sequential"}

	var wg sync.WaitGroup
	got := make([]*fb, n)
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			got[i] = r.GetImage()
		}(i)
	}
	wg.Wait()

	seen := make(map[*fb]bool, n)
	for _, f := range got {
		seen[f] = true
	}
	if len(seen) != n {
		t.Errorf("got %d distinct images across %d concurrent calls, want %d", len(seen), n, n)
	}
}
