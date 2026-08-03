package main

import (
	"net"
	"testing"
	"time"
)

// solidFB builds a uniformly mid-grey framebuffer. A flat non-black base makes
// the darkened banner detectable at every pixel it covers.
func solidFB(w, h int) *fb {
	data := make([]byte, w*h*4)
	for i := 0; i+3 < len(data); i += 4 {
		data[i+0], data[i+1], data[i+2] = 0x80, 0x80, 0x80
	}
	return &fb{w, h, data}
}

// measureBanner returns the size of the darkened region anchored at the top
// left, by walking the first row and the first column until the pixels stop
// differing from the source.
func measureBanner(src, out *fb) (w, h int) {
	for x := 0; x < src.w; x++ {
		off := x * 4
		if out.data[off] == src.data[off] && out.data[off+1] == src.data[off+1] {
			break
		}
		w++
	}
	for y := 0; y < src.h; y++ {
		off := y * src.w * 4
		if out.data[off] == src.data[off] && out.data[off+1] == src.data[off+1] {
			break
		}
		h++
	}
	return w, h
}

// The banner must wrap its text, not sit at a hardcoded 360x22.
func TestOverlayBoxFitsItsText(t *testing.T) {
	src := solidFB(1280, 720)

	shortW, shortH := measureBanner(src, addIPOverlay(src, "IP:   10.0.0.1"))
	longW, longH := measureBanner(src, addIPOverlay(src, "IP:   2001:db8:85a3:8d3:1319:8a2e:370:7348"))

	if shortW == 0 || shortH == 0 {
		t.Fatal("no banner was drawn")
	}
	if longW <= shortW {
		t.Errorf("a longer address must widen the banner: short=%d long=%d", shortW, longW)
	}
	if longH != shortH {
		t.Errorf("one line of text must keep the same height: short=%d long=%d", shortH, longH)
	}
	if longW > src.w {
		t.Errorf("banner width %d exceeds the image width %d", longW, src.w)
	}
}

// Each extra line adds exactly one line height, so IP + rDNS + time stack.
func TestOverlayGrowsWithLineCount(t *testing.T) {
	src := solidFB(1280, 720)

	_, h1 := measureBanner(src, addIPOverlay(src, "IP:   10.0.0.1"))
	_, h2 := measureBanner(src, addIPOverlay(src, "IP:   10.0.0.1", "Host: scanner.example.com"))
	_, h3 := measureBanner(src, addIPOverlay(src,
		"IP:   10.0.0.1", "Host: scanner.example.com", "Time: 2026-08-03 21:53:12 UTC"))

	if h2 <= h1 || h3 <= h2 {
		t.Fatalf("banner height must grow per line: 1=%d 2=%d 3=%d", h1, h2, h3)
	}
	if (h3 - h2) != (h2 - h1) {
		t.Errorf("line spacing is uneven: %d vs %d", h2-h1, h3-h2)
	}
}

// The font tracks the image size, so the banner stays readable on a 4K
// wallpaper and does not swallow a thumbnail.
func TestOverlayScalesWithImageSize(t *testing.T) {
	line := "IP:   192.0.2.10"

	small := solidFB(640, 360)
	large := solidFB(3840, 2160)

	smallW, smallH := measureBanner(small, addIPOverlay(small, line))
	largeW, largeH := measureBanner(large, addIPOverlay(large, line))

	if largeH <= smallH || largeW <= smallW {
		t.Errorf("banner must scale up with the image: small=%dx%d large=%dx%d",
			smallW, smallH, largeW, largeH)
	}
	// It must stay an accent, not take over the picture.
	if largeH > large.h/4 {
		t.Errorf("banner height %d covers too much of a %dpx image", largeH, large.h)
	}
}

// A narrow image must shrink the text rather than let the banner run off the
// right edge.
func TestOverlayNeverExceedsImageWidth(t *testing.T) {
	for _, w := range []int{80, 160, 320, 640} {
		src := solidFB(w, 200)
		out := addIPOverlay(src, "IP:   2001:db8:85a3:8d3:1319:8a2e:370:7348",
			"Host: a-very-long-reverse-dns-name.example.com")
		gotW, _ := measureBanner(src, out)
		if gotW > w {
			t.Errorf("image width %d: banner width %d overflows", w, gotW)
		}
		if gotW == 0 {
			t.Errorf("image width %d: no banner drawn", w)
		}
	}
}

// Pixels outside the banner must survive untouched, in the right channel order.
func TestOverlayLeavesRestOfImageIntact(t *testing.T) {
	src := makeTestFB(400, 300)
	out := addIPOverlay(src, "IP:   203.0.113.7")

	if out.w != src.w || out.h != src.h {
		t.Fatalf("dimensions changed: %dx%d", out.w, out.h)
	}
	// Bottom-right corner is far from any plausible banner.
	off := (src.h-1)*src.w*4 + (src.w-1)*4
	for i := range 3 {
		if out.data[off+i] != src.data[off+i] {
			t.Fatalf("pixel outside the banner changed at byte %d: got %d want %d",
				i, out.data[off+i], src.data[off+i])
		}
	}
}

// An empty line list means no banner at all — the image passes through.
func TestOverlayWithNoLinesIsUnchanged(t *testing.T) {
	src := solidFB(200, 100)
	out := addIPOverlay(src)
	for i := range src.data {
		if i%4 == 3 {
			continue
		}
		if out.data[i] != src.data[i] {
			t.Fatalf("image was modified at byte %d", i)
		}
	}
}

func TestOverlayConfigLines(t *testing.T) {
	addr, err := net.ResolveTCPAddr("tcp", "[2001:db8::1]:54321")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	when := time.Date(2026, 8, 3, 21, 53, 12, 0, time.UTC)

	// rDNS is left off so the test makes no DNS queries.
	got := overlayConfig{showIP: true, showTime: true}.lines(addr, when)
	want := []string{"IP:   2001:db8::1", "Time: 2026-08-03 21:53:12 UTC"}

	if len(got) != len(want) {
		t.Fatalf("got %q want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d: got %q want %q", i, got[i], want[i])
		}
	}
}

func TestOverlayConfigEnabled(t *testing.T) {
	tests := []struct {
		cfg  overlayConfig
		want bool
	}{
		{overlayConfig{}, false},
		{overlayConfig{showIP: true}, true},
		{overlayConfig{showRDNS: true}, true},
		{overlayConfig{showTime: true}, true},
	}
	for _, tt := range tests {
		if got := tt.cfg.enabled(); got != tt.want {
			t.Errorf("%+v: got %v want %v", tt.cfg, got, tt.want)
		}
	}
}

// A disabled overlay must not reach the resolver at all.
func TestOverlayWithoutRDNSMakesNoLookup(t *testing.T) {
	addr, err := net.ResolveTCPAddr("tcp", "192.0.2.10:54321")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	for _, line := range (overlayConfig{showIP: true, showTime: true}).lines(addr, time.Now()) {
		if len(line) > 5 && line[:5] == "Host:" {
			t.Errorf("unexpected rDNS line with show_rdns off: %q", line)
		}
	}
}
