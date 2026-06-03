package main

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"math/bits"
	"net"
)

// zrleStream holds the per-connection zlib stream required by the ZRLE
// encoding. RFB mandates a single continuous zlib stream for the whole
// connection, so the writer is created once and reused for every
// framebuffer update, with a Z_SYNC_FLUSH after each update.
type zrleStream struct {
	buf bytes.Buffer
	zw  *zlib.Writer
}

func newZRLEStream() *zrleStream {
	z := &zrleStream{}
	z.zw = zlib.NewWriter(&z.buf)
	return z
}

// cpixelEncoder describes how to emit a ZRLE "compressed pixel" (CPIXEL)
// for a given pixel format. ZRLE only defines the 3-byte CPIXEL shortcut
// for 32bpp true-colour formats whose colour bits all fit in the least
// significant three bytes (the common case, including our default format).
type cpixelEncoder struct {
	pf        pixelFormat
	bigEndian bool
}

// newCPIXELEncoder returns an encoder and whether ZRLE can be used for the
// given pixel format. When it returns false, the caller must fall back to
// Raw encoding.
func newCPIXELEncoder(pf pixelFormat) (cpixelEncoder, bool) {
	if pf.BPP != 32 || pf.TrueColor != 1 {
		return cpixelEncoder{}, false
	}
	// All colour bits must fit in the least significant 3 bytes so the most
	// significant byte is the redundant one we can drop.
	maxBit := 0
	for _, c := range []struct {
		max   uint16
		shift uint8
	}{
		{pf.RMax, pf.RShift},
		{pf.GMax, pf.GShift},
		{pf.BMax, pf.BShift},
	} {
		top := int(c.shift) + bits.Len(uint(c.max))
		if top > maxBit {
			maxBit = top
		}
	}
	if maxBit > 24 {
		return cpixelEncoder{}, false
	}
	return cpixelEncoder{pf: pf, bigEndian: pf.BigEndian != 0}, true
}

// pack writes the 3-byte CPIXEL for the colour (r,g,b) into dst (len >= 3).
func (e cpixelEncoder) pack(dst []byte, r, g, b uint8) {
	v := (uint32(r)&uint32(e.pf.RMax))<<e.pf.RShift |
		(uint32(g)&uint32(e.pf.GMax))<<e.pf.GShift |
		(uint32(b)&uint32(e.pf.BMax))<<e.pf.BShift
	var full [4]byte
	if e.bigEndian {
		// MSB first; drop the top (zero) byte, keep the low 3.
		binary.BigEndian.PutUint32(full[:], v)
		dst[0], dst[1], dst[2] = full[1], full[2], full[3]
	} else {
		binary.LittleEndian.PutUint32(full[:], v)
		dst[0], dst[1], dst[2] = full[0], full[1], full[2]
	}
}

// sendFramebufferZRLE sends the framebuffer as a single ZRLE-encoded
// rectangle. The image is split into 64x64 tiles; flat tiles use the "solid"
// subencoding (1 CPIXEL) and the rest use "raw" (subencoding 0). Everything
// is fed through the connection's continuous zlib stream.
func sendFramebufferZRLE(c net.Conn, f *fb, enc cpixelEncoder, z *zrleStream) error {
	// FramebufferUpdate header: msg-type(0), padding, 1 rectangle.
	if _, err := c.Write([]byte{0, 0}); err != nil {
		return err
	}
	if err := write16(c, 1); err != nil {
		return err
	}
	if err := write16(c, 0, 0, uint16(f.w), uint16(f.h)); err != nil {
		return err
	}
	if err := write32(c, encZRLE); err != nil {
		return err
	}

	payload := encodeZRLETiles(f, enc)

	z.buf.Reset()
	if _, err := z.zw.Write(payload); err != nil {
		return err
	}
	// Z_SYNC_FLUSH so the client can inflate everything sent so far.
	if err := z.zw.Flush(); err != nil {
		return err
	}

	data := z.buf.Bytes()
	if err := write32(c, uint32(len(data))); err != nil {
		return err
	}
	_, err := c.Write(data)
	return err
}

// encodeZRLETiles builds the uncompressed ZRLE tile stream for the whole
// framebuffer.
func encodeZRLETiles(f *fb, enc cpixelEncoder) []byte {
	out := bytes.Buffer{}
	// Upper bound: subencoding byte + raw CPIXELs per tile.
	out.Grow(f.w*f.h*3 + (f.w/zrleTileSize+1)*(f.h/zrleTileSize+1) + 16)

	var px [3]byte
	for ty := 0; ty < f.h; ty += zrleTileSize {
		th := zrleTileSize
		if ty+th > f.h {
			th = f.h - ty
		}
		for tx := 0; tx < f.w; tx += zrleTileSize {
			tw := zrleTileSize
			if tx+tw > f.w {
				tw = f.w - tx
			}

			if r, g, b, solid := tileSolidColor(f, tx, ty, tw, th); solid {
				out.WriteByte(1) // solid subencoding
				enc.pack(px[:], r, g, b)
				out.Write(px[:])
				continue
			}

			out.WriteByte(0) // raw subencoding
			for y := ty; y < ty+th; y++ {
				row := y * f.w * 4
				for x := tx; x < tx+tw; x++ {
					off := row + x*4
					// fb stores BGRX: off+0=B, off+1=G, off+2=R.
					enc.pack(px[:], f.data[off+2], f.data[off+1], f.data[off+0])
					out.Write(px[:])
				}
			}
		}
	}
	return out.Bytes()
}

// tileSolidColor reports whether every pixel in the tile shares one colour,
// returning that colour.
func tileSolidColor(f *fb, tx, ty, tw, th int) (r, g, b uint8, solid bool) {
	first := ty*f.w*4 + tx*4
	r, g, b = f.data[first+2], f.data[first+1], f.data[first+0]
	for y := ty; y < ty+th; y++ {
		row := y * f.w * 4
		for x := tx; x < tx+tw; x++ {
			off := row + x*4
			if f.data[off+2] != r || f.data[off+1] != g || f.data[off+0] != b {
				return 0, 0, 0, false
			}
		}
	}
	return r, g, b, true
}
