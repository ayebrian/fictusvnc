package main

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"testing"
)

// encodeZRLETilesSolidRaw is the original ZRLE encoder (first commit): every
// tile is either "solid" or "raw". Kept here only to measure the bandwidth
// gain from adding palette/RLE subencodings.
func encodeZRLETilesSolidRaw(f *fb, enc cpixelEncoder) []byte {
	out := &bytes.Buffer{}
	var px [3]byte
	for ty := 0; ty < f.h; ty += zrleTileSize {
		th := min(zrleTileSize, f.h-ty)
		for tx := 0; tx < f.w; tx += zrleTileSize {
			tw := min(zrleTileSize, f.w-tx)

			first := pixelKey(f, tx, ty)
			solid := true
			for y := ty; y < ty+th && solid; y++ {
				for x := tx; x < tx+tw; x++ {
					if pixelKey(f, x, y) != first {
						solid = false
						break
					}
				}
			}
			if solid {
				out.WriteByte(1)
				enc.packKey(px[:], first)
				out.Write(px[:])
				continue
			}
			out.WriteByte(0)
			for y := ty; y < ty+th; y++ {
				for x := tx; x < tx+tw; x++ {
					enc.packKey(px[:], pixelKey(f, x, y))
					out.Write(px[:])
				}
			}
		}
	}
	return out.Bytes()
}

// zlibFrameSize returns the wire size of one framebuffer update for a ZRLE
// payload: a fresh continuous stream, one Write, one Z_SYNC_FLUSH.
func zlibFrameSize(payload []byte) int {
	var buf bytes.Buffer
	zw := zlib.NewWriter(&buf)
	zw.Write(payload)
	zw.Flush()
	return buf.Len()
}

func TestZRLEBandwidth(t *testing.T) {
	images := []string{"images/default.png", "vncwindow.png", "banner.png"}
	pf := pixelFormat{32, 24, 0, 1, 255, 255, 255, 16, 8, 0, [3]byte{}}
	enc, ok := newCPIXELEncoder(pf)
	if !ok {
		t.Fatal("pixel format not ZRLE-capable")
	}

	fmt.Printf("%-22s %10s %14s %14s %12s %10s\n",
		"image", "raw(enc0)", "zrle solid+raw", "zrle full", "vs raw", "vs s+raw")
	for _, name := range images {
		f, err := loadImage(name)
		if err != nil {
			t.Logf("skip %s: %v", name, err)
			continue
		}
		raw := f.w * f.h * 4 // original Raw encoding (encoding 0), 32bpp, uncompressed
		solidRaw := zlibFrameSize(encodeZRLETilesSolidRaw(f, enc))
		full := zlibFrameSize(encodeZRLETiles(f, enc))

		fmt.Printf("%-22s %10d %14d %14d %11.1f%% %9.1f%%\n",
			fmt.Sprintf("%s %dx%d", name, f.w, f.h),
			raw, solidRaw, full,
			100*float64(full)/float64(raw),
			100*float64(full)/float64(solidRaw),
		)
	}
}
