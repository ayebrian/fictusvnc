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
	return writeFramebufferZRLE(c, f.w, f.h, encodeZRLETiles(f, enc), z)
}

// writeFramebufferZRLE sends a ZRLE rectangle from an already-built (pre-zlib)
// tile payload, feeding it through the connection's continuous zlib stream. The
// payload depends only on the frame and pixel format, both immutable for a
// connection, so the caller can cache it across update requests; the zlib write
// and flush still run every time to keep the stream continuous.
func writeFramebufferZRLE(c net.Conn, w, h int, payload []byte, z *zrleStream) error {
	// FramebufferUpdate header: msg-type(0), padding, 1 rectangle.
	if _, err := c.Write([]byte{0, 0}); err != nil {
		return err
	}
	if err := write16(c, 1); err != nil {
		return err
	}
	if err := write16(c, 0, 0, uint16(w), uint16(h)); err != nil {
		return err
	}
	if err := write32(c, encZRLE); err != nil {
		return err
	}

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
	out := &bytes.Buffer{}
	// Upper bound: subencoding byte + raw CPIXELs per tile.
	out.Grow(f.w*f.h*3 + (f.w/zrleTileSize+1)*(f.h/zrleTileSize+1) + 16)

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
			encodeTile(out, f, enc, tx, ty, tw, th)
		}
	}
	return out.Bytes()
}

// pixelKey packs a tile pixel's colour into a comparable 24-bit key.
func pixelKey(f *fb, x, y int) uint32 {
	off := y*f.w*4 + x*4
	// fb stores BGRX: off+0=B, off+1=G, off+2=R.
	return uint32(f.data[off+2])<<16 | uint32(f.data[off+1])<<8 | uint32(f.data[off+0])
}

func (e cpixelEncoder) packKey(dst []byte, key uint32) {
	e.pack(dst, byte(key>>16), byte(key>>8), byte(key))
}

type rleRun struct {
	key uint32
	n   int
}

// encodeTile picks the smallest ZRLE subencoding for one tile and writes it.
// It compares raw, solid, packed-palette, palette-RLE and plain-RLE by their
// pre-zlib byte cost — the heuristic real ZRLE encoders use.
func encodeTile(out *bytes.Buffer, f *fb, enc cpixelEncoder, tx, ty, tw, th int) {
	nPix := tw * th

	// Flatten the tile row-major and build the colour palette (capped at 128
	// entries — beyond that palette modes are not allowed).
	pixels := make([]uint32, 0, nPix)
	palIndex := make(map[uint32]int, 16)
	palette := make([]uint32, 0, 16)
	tooManyColors := false
	for y := ty; y < ty+th; y++ {
		for x := tx; x < tx+tw; x++ {
			k := pixelKey(f, x, y)
			pixels = append(pixels, k)
			if !tooManyColors {
				if _, ok := palIndex[k]; !ok {
					if len(palette) >= 128 {
						tooManyColors = true
					} else {
						palIndex[k] = len(palette)
						palette = append(palette, k)
					}
				}
			}
		}
	}

	var px [3]byte

	// Solid tile.
	if !tooManyColors && len(palette) == 1 {
		out.WriteByte(1)
		enc.packKey(px[:], palette[0])
		out.Write(px[:])
		return
	}

	// Run-length groups over the flattened sequence (runs may cross rows).
	runs := make([]rleRun, 0, nPix)
	for i := 0; i < len(pixels); {
		j := i + 1
		for j < len(pixels) && pixels[j] == pixels[i] {
			j++
		}
		runs = append(runs, rleRun{pixels[i], j - i})
		i = j
	}

	palSize := len(palette)
	usePalette := !tooManyColors && palSize >= 2

	// --- cost estimates (bytes before zlib) ---
	const maxCost = 1 << 30
	rawCost := nPix * 3

	packedCost := maxCost
	bpp := 0
	if usePalette && palSize <= 16 {
		switch {
		case palSize == 2:
			bpp = 1
		case palSize <= 4:
			bpp = 2
		default:
			bpp = 4
		}
		perRow := (tw*bpp + 7) / 8
		packedCost = palSize*3 + perRow*th
	}

	palRLECost := maxCost
	if usePalette && palSize <= 127 {
		c := palSize * 3
		for _, r := range runs {
			c++ // index byte
			if r.n > 1 {
				c += runLenBytes(r.n)
			}
		}
		palRLECost = c
	}

	plainRLECost := 0
	for _, r := range runs {
		plainRLECost += 3 + runLenBytes(r.n)
	}

	// Pick the cheapest.
	best := rawCost
	mode := 0 // raw
	if packedCost < best {
		best, mode = packedCost, 1
	}
	if palRLECost < best {
		best, mode = palRLECost, 2
	}
	if plainRLECost < best {
		mode = 3
	}

	switch mode {
	case 1: // packed palette
		out.WriteByte(byte(palSize))
		for _, k := range palette {
			enc.packKey(px[:], k)
			out.Write(px[:])
		}
		writePackedIndices(out, pixels, palIndex, tw, th, bpp)
	case 2: // palette RLE
		out.WriteByte(byte(128 + palSize))
		for _, k := range palette {
			enc.packKey(px[:], k)
			out.Write(px[:])
		}
		for _, r := range runs {
			idx := palIndex[r.key]
			if r.n == 1 {
				out.WriteByte(byte(idx)) // top bit clear → single pixel
			} else {
				out.WriteByte(byte(idx | 0x80))
				writeRunLength(out, r.n)
			}
		}
	case 3: // plain RLE
		out.WriteByte(128)
		for _, r := range runs {
			enc.packKey(px[:], r.key)
			out.Write(px[:])
			writeRunLength(out, r.n)
		}
	default: // raw
		out.WriteByte(0)
		for _, k := range pixels {
			enc.packKey(px[:], k)
			out.Write(px[:])
		}
	}
}

// writePackedIndices emits palette indices packed bpp bits per pixel,
// MSB-first, padded to a byte boundary at the end of each row.
func writePackedIndices(out *bytes.Buffer, pixels []uint32, palIndex map[uint32]int, tw, th, bpp int) {
	for y := 0; y < th; y++ {
		var cur byte
		nbits := 0
		for x := 0; x < tw; x++ {
			idx := palIndex[pixels[y*tw+x]]
			for b := bpp - 1; b >= 0; b-- {
				cur = (cur << 1) | byte((idx>>b)&1)
				nbits++
				if nbits == 8 {
					out.WriteByte(cur)
					cur, nbits = 0, 0
				}
			}
		}
		if nbits > 0 {
			out.WriteByte(cur << (8 - nbits))
		}
	}
}

// writeRunLength encodes (runLen-1) as a sequence of 255-valued bytes followed
// by a final byte holding the remainder, per the ZRLE RLE format.
func writeRunLength(out *bytes.Buffer, runLen int) {
	v := runLen - 1
	for v >= 255 {
		out.WriteByte(255)
		v -= 255
	}
	out.WriteByte(byte(v))
}

// runLenBytes is the number of bytes writeRunLength would emit for runLen.
func runLenBytes(runLen int) int {
	return (runLen-1)/255 + 1
}
