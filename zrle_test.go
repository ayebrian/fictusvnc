package main

import (
	"compress/zlib"
	"encoding/binary"
	"io"
	"net"
	"testing"
)

// makeTestFB builds a framebuffer in the internal BGRX layout where each
// vertical 64px band uses a different colour structure, so the encoder is
// forced through every ZRLE subencoding: solid, packed palette (2 and many
// colours), and gradient regions that fall back to plain RLE / raw.
func makeTestFB(w, h int) *fb {
	pal8 := [8][3]uint8{
		{0, 0, 0}, {255, 0, 0}, {0, 255, 0}, {0, 0, 255},
		{255, 255, 0}, {0, 255, 255}, {255, 0, 255}, {128, 128, 128},
	}
	data := make([]byte, w*h*4)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			off := y*w*4 + x*4
			var r, g, b uint8
			switch (x / 64) % 4 {
			case 0: // solid
				r, g, b = 0x20, 0x40, 0x60
			case 1: // two-colour checkerboard
				if (x+y)%2 == 0 {
					r, g, b = 0xff, 0xff, 0xff
				}
			case 2: // 8-colour palette blocks
				c := pal8[((x/4)+(y/4))%8]
				r, g, b = c[0], c[1], c[2]
			default: // many-colour gradient
				r, g, b = uint8(x), uint8(y), uint8(x*y)
			}
			data[off+0] = b
			data[off+1] = g
			data[off+2] = r
			data[off+3] = 0
		}
	}
	return &fb{w, h, data}
}

func TestZRLERoundTrip(t *testing.T) {
	pf := pixelFormat{32, 24, 0, 1, 255, 255, 255, 16, 8, 0, [3]byte{}}
	enc, ok := newCPIXELEncoder(pf)
	if !ok {
		t.Fatal("default pixel format should be ZRLE-capable")
	}

	src := makeTestFB(256, 130) // 130 not a multiple of 64 → partial edge tiles
	z := newZRLEStream()

	srv, cli := net.Pipe()
	go func() {
		_ = sendFramebufferZRLE(srv, src, enc, z)
		srv.Close()
	}()

	got := decodeZRLEUpdate(t, cli)
	if got.w != src.w || got.h != src.h {
		t.Fatalf("dimensions: got %dx%d want %dx%d", got.w, got.h, src.w, src.h)
	}
	for i := range src.data {
		if i%4 == 3 {
			continue // alpha/pad byte not meaningful
		}
		if got.data[i] != src.data[i] {
			px := i / 4
			t.Fatalf("pixel %d (x=%d y=%d) byte %d: got %#02x want %#02x",
				px, px%src.w, px/src.w, i%4, got.data[i], src.data[i])
		}
	}
}

// decodeZRLEUpdate reads one FramebufferUpdate carrying a single ZRLE
// rectangle from r and reconstructs it into a BGRX framebuffer.
func decodeZRLEUpdate(t *testing.T, r io.Reader) *fb {
	t.Helper()
	hdr := make([]byte, 4)
	mustRead(t, r, hdr) // msg-type, padding, num-rects(2)
	if hdr[0] != 0 {
		t.Fatalf("msg type: got %d want 0", hdr[0])
	}
	if n := binary.BigEndian.Uint16(hdr[2:4]); n != 1 {
		t.Fatalf("num rects: got %d want 1", n)
	}

	rect := make([]byte, 12)
	mustRead(t, r, rect)
	w := int(binary.BigEndian.Uint16(rect[4:6]))
	h := int(binary.BigEndian.Uint16(rect[6:8]))
	if e := binary.BigEndian.Uint32(rect[8:12]); e != encZRLE {
		t.Fatalf("encoding: got %d want %d", e, encZRLE)
	}

	lenBuf := make([]byte, 4)
	mustRead(t, r, lenBuf)
	zlen := int(binary.BigEndian.Uint32(lenBuf))
	zdata := make([]byte, zlen)
	mustRead(t, r, zdata)

	zr, err := zlib.NewReader(newByteReader(zdata))
	if err != nil {
		t.Fatalf("zlib: %v", err)
	}
	// ZRLE uses a continuous, sync-flushed stream: read exactly the bytes the
	// tile geometry demands, never to EOF (which would expect a final block).
	readExact := func(n int) []byte {
		b := make([]byte, n)
		if _, err := io.ReadFull(zr, b); err != nil {
			t.Fatalf("inflate %d bytes: %v", n, err)
		}
		return b
	}

	out := &fb{w, h, make([]byte, w*h*4)}
	readCPIXEL := func() (r, g, b uint8) {
		c := readExact(3)
		v := uint32(c[0]) | uint32(c[1])<<8 | uint32(c[2])<<16
		return uint8(v >> 16), uint8(v >> 8), uint8(v)
	}
	setPx := func(x, y int, r, g, b uint8) {
		off := y*w*4 + x*4
		out.data[off+0] = b
		out.data[off+1] = g
		out.data[off+2] = r
	}
	// readRunLen mirrors the ZRLE length format: sum 255-bytes until one is
	// < 255, run length = sum + 1.
	readRunLen := func() int {
		sum := 0
		for {
			b := int(readExact(1)[0])
			sum += b
			if b < 255 {
				return sum + 1
			}
		}
	}

	type rgb struct{ r, g, b uint8 }
	readPalette := func(n int) []rgb {
		p := make([]rgb, n)
		for i := range p {
			p[i].r, p[i].g, p[i].b = readCPIXEL()
		}
		return p
	}

	for ty := 0; ty < h; ty += zrleTileSize {
		th := min(zrleTileSize, h-ty)
		for tx := 0; tx < w; tx += zrleTileSize {
			tw := min(zrleTileSize, w-tx)
			// place writes the n-th pixel of the tile in row-major order.
			place := func(local int, r, g, b uint8) {
				setPx(tx+local%tw, ty+local/tw, r, g, b)
			}
			nPix := tw * th
			sub := readExact(1)[0]
			switch {
			case sub == 0: // raw
				for i := 0; i < nPix; i++ {
					r, g, b := readCPIXEL()
					place(i, r, g, b)
				}
			case sub == 1: // solid
				r, g, b := readCPIXEL()
				for i := 0; i < nPix; i++ {
					place(i, r, g, b)
				}
			case sub >= 2 && sub <= 16: // packed palette
				pal := readPalette(int(sub))
				bpp := 1
				switch {
				case sub <= 2:
					bpp = 1
				case sub <= 4:
					bpp = 2
				default:
					bpp = 4
				}
				for y := 0; y < th; y++ {
					perRow := (tw*bpp + 7) / 8
					rowBytes := readExact(perRow)
					bit := 0
					for x := 0; x < tw; x++ {
						idx := 0
						for k := 0; k < bpp; k++ {
							byteIdx := bit / 8
							boffset := 7 - bit%8
							idx = (idx << 1) | int((rowBytes[byteIdx]>>uint(boffset))&1)
							bit++
						}
						place(y*tw+x, pal[idx].r, pal[idx].g, pal[idx].b)
					}
				}
			case sub == 128: // plain RLE
				pos := 0
				for pos < nPix {
					r, g, b := readCPIXEL()
					n := readRunLen()
					for k := 0; k < n; k++ {
						place(pos, r, g, b)
						pos++
					}
				}
			case sub >= 130: // palette RLE
				pal := readPalette(int(sub) - 128)
				pos := 0
				for pos < nPix {
					ib := int(readExact(1)[0])
					n := 1
					idx := ib
					if ib&0x80 != 0 {
						idx = ib & 0x7f
						n = readRunLen()
					}
					for k := 0; k < n; k++ {
						place(pos, pal[idx].r, pal[idx].g, pal[idx].b)
						pos++
					}
				}
			default:
				t.Fatalf("unexpected subencoding %d", sub)
			}
		}
	}
	return out
}

func mustRead(t *testing.T, r io.Reader, b []byte) {
	t.Helper()
	if _, err := io.ReadFull(r, b); err != nil {
		t.Fatalf("read: %v", err)
	}
}

type byteReader struct {
	b []byte
	i int
}

func newByteReader(b []byte) *byteReader { return &byteReader{b: b} }

func (r *byteReader) Read(p []byte) (int, error) {
	if r.i >= len(r.b) {
		return 0, io.EOF
	}
	n := copy(p, r.b[r.i:])
	r.i += n
	return n, nil
}
