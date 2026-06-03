package main

import (
	"encoding/binary"
	"image"
	"image/color"
	"image/draw"
	_ "image/jpeg"
	_ "image/png"
	"net"
	"os"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
)

type fb struct {
	w, h int
	data []byte
}

func loadImage(path string) (*fb, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	img, _, err := image.Decode(f)
	if err != nil {
		return nil, err
	}

	r := img.Bounds()
	buf := make([]byte, r.Dx()*r.Dy()*4)
	for y := 0; y < r.Dy(); y++ {
		for x := 0; x < r.Dx(); x++ {
			off := y*r.Dx()*4 + x*4
			rr, gg, bb, _ := img.At(x+r.Min.X, y+r.Min.Y).RGBA()
			buf[off+0] = byte(bb >> 8)
			buf[off+1] = byte(gg >> 8)
			buf[off+2] = byte(rr >> 8)
			buf[off+3] = 0
		}
	}
	return &fb{r.Dx(), r.Dy(), buf}, nil
}

func addIPOverlay(src *fb, txt string) *fb {
	img := image.NewNRGBA(image.Rect(0, 0, src.w, src.h))
	// src is stored BGRX; load it into the NRGBA buffer as RGBA with opaque
	// alpha so that draw.Over composites the banner over the real pixels
	// (a transparent base would turn the banner into a solid black bar).
	for i := 0; i+3 < len(src.data); i += 4 {
		img.Pix[i+0] = src.data[i+2] // R
		img.Pix[i+1] = src.data[i+1] // G
		img.Pix[i+2] = src.data[i+0] // B
		img.Pix[i+3] = 255           // A
	}

	banner := image.Rect(0, 0, 360, 22)
	draw.Draw(img, banner, &image.Uniform{color.RGBA{0, 0, 0, 180}}, image.Point{}, draw.Over)

	d := &font.Drawer{
		Dst:  img,
		Src:  image.White,
		Face: basicfont.Face7x13,
		Dot:  fixed.P(6, 16),
	}
	d.DrawString("Your IP: " + txt)

	// Convert back to BGRX; the alpha byte is unused by sendFramebuffer.
	out := make([]byte, len(img.Pix))
	for i := 0; i+3 < len(img.Pix); i += 4 {
		out[i+0] = img.Pix[i+2] // B
		out[i+1] = img.Pix[i+1] // G
		out[i+2] = img.Pix[i+0] // R
		out[i+3] = 0
	}
	return &fb{src.w, src.h, out}
}

// converter returns a function that writes the pixel (r,g,b) in the client's
// pixel format into dst and returns the number of bytes written. Writing into
// a caller-owned buffer avoids a per-pixel allocation on the hot path.
func converter(pf pixelFormat) func(dst []byte, r, g, b uint8) int {
	switch pf.BPP {
	case 32:
		return func(dst []byte, r, g, b uint8) int {
			out := (uint32(r)&uint32(pf.RMax))<<pf.RShift |
				(uint32(g)&uint32(pf.GMax))<<pf.GShift |
				(uint32(b)&uint32(pf.BMax))<<pf.BShift
			if pf.BigEndian != 0 {
				binary.BigEndian.PutUint32(dst, out)
			} else {
				binary.LittleEndian.PutUint32(dst, out)
			}
			return 4
		}
	case 24:
		return func(dst []byte, r, g, b uint8) int {
			dst[0], dst[1], dst[2] = r, g, b
			return 3
		}
	case 8:
		if pf.RMax == 7 && pf.GMax == 7 && pf.BMax == 3 && pf.RShift == 5 && pf.GShift == 2 && pf.BShift == 0 {
			return func(dst []byte, r, g, b uint8) int {
				dst[0] = ((r >> 5) << 5) | ((g >> 5) << 2) | (b >> 6)
				return 1
			}
		}
	}
	return func(dst []byte, r, g, b uint8) int {
		y := (uint32(r)*30 + uint32(g)*59 + uint32(b)*11 + 50) / 100
		dst[0] = uint8(y)
		return 1
	}
}

func sendFramebuffer(c net.Conn, f *fb, pf pixelFormat) error {
	_, err := c.Write([]byte{0, 0})
	if err != nil {
		return err
	}

	err = write16(c, 1)
	if err != nil {
		return err
	}

	err = write16(c, 0, 0, uint16(f.w), uint16(f.h))
	if err != nil {
		return err
	}

	err = write32(c, 0)
	if err != nil {
		return err
	}

	conv := converter(pf)
	bpp := int(pf.BPP / 8)
	line := make([]byte, f.w*bpp)

	for y := range f.h {
		i := 0
		for x := 0; x < f.w; x++ {
			off := y*f.w*4 + x*4
			r, g, b := f.data[off+2], f.data[off+1], f.data[off+0]
			i += conv(line[i:], r, g, b)
		}
		_, err := c.Write(line[:i])
		if err != nil {
			return err
		}
	}

	return nil
}
