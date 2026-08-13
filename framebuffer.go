package main

import (
	"encoding/binary"
	"image"
	"image/color"
	"image/draw"
	_ "image/jpeg"
	_ "image/png"
	"log/slog"
	"net"
	"os"
	"sync"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/font/gofont/gomono"
	"golang.org/x/image/font/opentype"
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

// overlayFont is the scalable face used for the banner. Go Mono keeps
// addresses and timestamps aligned and stays legible when scaled down. It
// ships inside golang.org/x/image, so this costs no extra dependency.
var overlayFont = sync.OnceValue(func() *opentype.Font {
	f, err := opentype.Parse(gomono.TTF)
	if err != nil {
		// Only reachable if the embedded font is corrupt; the caller falls
		// back to the fixed-size bitmap face.
		slog.Warn("failed to parse overlay font, falling back to bitmap face", "error", err)
		return nil
	}
	return f
})

// newOverlayFace builds a face at the requested pixel size, falling back to
// the fixed 7x13 bitmap face if the scalable font is unavailable.
func newOverlayFace(size float64) (font.Face, func()) {
	f := overlayFont()
	if f == nil {
		return basicfont.Face7x13, func() {}
	}
	// DPI 72 makes one point equal one pixel, so size is a pixel height.
	face, err := opentype.NewFace(f, &opentype.FaceOptions{
		Size:    size,
		DPI:     72,
		Hinting: font.HintingFull,
	})
	if err != nil {
		return basicfont.Face7x13, func() {}
	}
	return face, func() { face.Close() }
}

// overlayLayout is a measured banner: the face to draw with, the box to fill
// and the metrics needed to place each line.
type overlayLayout struct {
	face    font.Face
	release func()
	box     image.Rectangle
	pad     int
	lineH   int
	ascent  int
}

// layoutOverlay picks a font size proportional to the image, measures the
// widest line and returns a box just large enough to hold the text plus a
// small padding. The size is reduced until the box fits the image width, so
// long IPv6 addresses and hostnames never spill off the edge.
func layoutOverlay(w, h int, lines []string) overlayLayout {
	// Roughly 1/26th of the height reads well across wallpaper-sized images
	// and screenshots alike; the clamps keep tiny and huge images sane.
	size := float64(h) / 26
	size = min(max(size, 11), 40)

	for {
		face, release := newOverlayFace(size)
		m := face.Metrics()
		pad := max(int(size*0.45), 4)
		lineH := m.Height.Ceil()

		widest := 0
		for _, s := range lines {
			if adv := font.MeasureString(face, s).Ceil(); adv > widest {
				widest = adv
			}
		}

		boxW := widest + 2*pad
		boxH := lineH*len(lines) + 2*pad

		// Shrink rather than clip. 7px is the floor: below that the text is
		// unreadable anyway, so an overflowing box is the lesser evil.
		if boxW > w && size > 7 {
			release()
			size--
			continue
		}

		return overlayLayout{
			face:    face,
			release: release,
			box:     image.Rect(0, 0, min(boxW, w), min(boxH, h)),
			pad:     pad,
			lineH:   lineH,
			ascent:  m.Ascent.Ceil(),
		}
	}
}

// addIPOverlay draws a translucent banner carrying the given lines over the
// top-left of the image. The banner is sized to its contents, so it grows for
// an IPv6 address or an extra rDNS line and shrinks when it is just an IPv4.
func addIPOverlay(src *fb, lines ...string) *fb {
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

	if len(lines) > 0 {
		l := layoutOverlay(src.w, src.h, lines)
		defer l.release()

		draw.Draw(img, l.box, &image.Uniform{color.RGBA{0, 0, 0, 180}}, image.Point{}, draw.Over)

		d := &font.Drawer{Dst: img, Src: image.White, Face: l.face}
		for i, s := range lines {
			d.Dot = fixed.P(l.pad, l.pad+l.ascent+i*l.lineH)
			d.DrawString(s)
		}
	}

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
			v := (uint32(r)&uint32(pf.RMax))<<pf.RShift |
				(uint32(g)&uint32(pf.GMax))<<pf.GShift |
				(uint32(b)&uint32(pf.BMax))<<pf.BShift
			var full [4]byte
			if pf.BigEndian != 0 {
				// MSB first; drop the top (unused) byte, keep the low 3.
				binary.BigEndian.PutUint32(full[:], v)
				dst[0], dst[1], dst[2] = full[1], full[2], full[3]
			} else {
				binary.LittleEndian.PutUint32(full[:], v)
				dst[0], dst[1], dst[2] = full[0], full[1], full[2]
			}
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

// rawFramebufferBody converts the whole framebuffer into the client's pixel
// format. The frame never changes for the life of a connection, so the result
// can be cached and reused for every update request instead of re-converting.
func rawFramebufferBody(f *fb, pf pixelFormat) []byte {
	conv := converter(pf)
	bpp := int(pf.BPP / 8)
	body := make([]byte, f.w*f.h*bpp)

	i := 0
	for y := range f.h {
		for x := 0; x < f.w; x++ {
			off := y*f.w*4 + x*4
			r, g, b := f.data[off+2], f.data[off+1], f.data[off+0]
			i += conv(body[i:], r, g, b)
		}
	}
	return body[:i]
}

// writeRawFramebuffer emits a single Raw-encoded rectangle covering the whole
// frame from an already-converted body.
func writeRawFramebuffer(c net.Conn, w, h int, body []byte) error {
	if _, err := c.Write([]byte{0, 0}); err != nil {
		return err
	}
	if err := write16(c, 1); err != nil {
		return err
	}
	if err := write16(c, 0, 0, uint16(w), uint16(h)); err != nil {
		return err
	}
	if err := write32(c, 0); err != nil {
		return err
	}
	_, err := c.Write(body)
	return err
}

func sendFramebuffer(c net.Conn, f *fb, pf pixelFormat) error {
	return writeRawFramebuffer(c, f.w, f.h, rawFramebufferBody(f, pf))
}
