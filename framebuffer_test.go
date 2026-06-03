package main

import (
	"encoding/binary"
	"net"
	"testing"
)

// TestRawFramebufferRoundTrip verifies the (allocation-free) converter and
// sendFramebuffer still emit a correct 32bpp Raw rectangle.
func TestRawFramebufferRoundTrip(t *testing.T) {
	pf := pixelFormat{32, 24, 0, 1, 255, 255, 255, 16, 8, 0, [3]byte{}}
	src := makeTestFB(40, 24)

	srv, cli := net.Pipe()
	go func() {
		_ = sendFramebuffer(srv, src, pf)
		srv.Close()
	}()

	hdr := make([]byte, 4)
	mustRead(t, cli, hdr) // msg-type, padding, num-rects
	if n := binary.BigEndian.Uint16(hdr[2:4]); n != 1 {
		t.Fatalf("num rects: got %d want 1", n)
	}
	rect := make([]byte, 12)
	mustRead(t, cli, rect)
	w := int(binary.BigEndian.Uint16(rect[4:6]))
	h := int(binary.BigEndian.Uint16(rect[6:8]))
	if w != src.w || h != src.h {
		t.Fatalf("dimensions: got %dx%d want %dx%d", w, h, src.w, src.h)
	}
	if e := binary.BigEndian.Uint32(rect[8:12]); e != encRaw {
		t.Fatalf("encoding: got %d want %d", e, encRaw)
	}

	pix := make([]byte, 4)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			mustRead(t, cli, pix)
			// little-endian 32bpp: byte0=B, byte1=G, byte2=R.
			off := y*w*4 + x*4
			if pix[2] != src.data[off+2] || pix[1] != src.data[off+1] || pix[0] != src.data[off+0] {
				t.Fatalf("pixel (%d,%d): got R=%d G=%d B=%d want R=%d G=%d B=%d",
					x, y, pix[2], pix[1], pix[0],
					src.data[off+2], src.data[off+1], src.data[off+0])
			}
		}
	}
}

func TestIPOverlay(t *testing.T) {
	src := makeTestFB(120, 60) // band 0 (x<64) is solid 0x20,0x40,0x60
	out := addIPOverlay(src, "203.0.113.7")
	if out.w != src.w || out.h != src.h {
		t.Fatalf("dimensions changed: %dx%d", out.w, out.h)
	}

	// A pixel well below the 22px banner must keep the original colour.
	off := 40*src.w*4 + 10*4
	if out.data[off+0] != src.data[off+0] ||
		out.data[off+1] != src.data[off+1] ||
		out.data[off+2] != src.data[off+2] {
		t.Fatalf("pixel below banner was altered: got B=%d G=%d R=%d want B=%d G=%d R=%d",
			out.data[off+0], out.data[off+1], out.data[off+2],
			src.data[off+0], src.data[off+1], src.data[off+2])
	}

	// Inside the banner the background must be darkened (semi-transparent
	// black over the original), not left untouched and not channel-swapped.
	bo := 2*src.w*4 + 2*4                 // x=2,y=2 — banner background, before the text glyphs
	if out.data[bo+2] >= src.data[bo+2] { // R should drop
		t.Fatalf("banner background not darkened: R %d -> %d", src.data[bo+2], out.data[bo+2])
	}
}
