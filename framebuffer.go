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
	copy(img.Pix, src.data)

	banner := image.Rect(0, 0, 360, 22)
	draw.Draw(img, banner, &image.Uniform{color.RGBA{0, 0, 0, 180}}, image.Point{}, draw.Over)

	d := &font.Drawer{
		Dst:  img,
		Src:  image.White,
		Face: basicfont.Face7x13,
		Dot:  fixed.P(6, 16),
	}
	d.DrawString("Your IP: " + txt)

	out := make([]byte, len(img.Pix))
	for i := 0; i < len(img.Pix); i += 4 {
		out[i+0] = img.Pix[i+0]
		out[i+1] = img.Pix[i+1]
		out[i+2] = img.Pix[i+2]
		out[i+3] = 0
	}
	return &fb{src.w, src.h, out}
}

func converter(pf pixelFormat) func(r, g, b uint8) []byte {
	switch pf.BPP {
	case 32:
		sh := []uint8{pf.RShift, pf.GShift, pf.BShift}
		return func(r, g, b uint8) []byte {
			c := []uint32{uint32(r), uint32(g), uint32(b)}
			out := uint32(0)
			for i := 0; i < 3; i++ {
				out |= (c[i] & uint32(pf.RMax)) << sh[i]
			}
			buf := make([]byte, 4)
			if pf.BigEndian != 0 {
				binary.BigEndian.PutUint32(buf, out)
			} else {
				binary.LittleEndian.PutUint32(buf, out)
			}
			return buf
		}
	case 24:
		return func(r, g, b uint8) []byte {
			return []byte{r, g, b}
		}
	case 8:
		if pf.RMax == 7 && pf.GMax == 7 && pf.BMax == 3 && pf.RShift == 5 && pf.GShift == 2 && pf.BShift == 0 {
			return func(r, g, b uint8) []byte {
				rr := r >> 5
				gg := g >> 5
				bb := b >> 6
				return []byte{(rr << 5) | (gg << 2) | bb}
			}
		}
	}
	return func(r, g, b uint8) []byte {
		y := (uint32(r)*30 + uint32(g)*59 + uint32(b)*11 + 50) / 100
		return []byte{uint8(y)}
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
			pix := conv(r, g, b)
			copy(line[i:], pix)
			i += len(pix)
		}
		_, err := c.Write(line[:i])
		if err != nil {
			return err
		}
	}

	return nil
}
