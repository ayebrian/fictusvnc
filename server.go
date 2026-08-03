package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"strings"
	"time"
)

const (
	// readTimeout bounds how long a client may stay silent between messages.
	readTimeout = 30 * time.Second
	// writeTimeout bounds a single send. Without it a client that stops
	// reading (TCP zero-window) would pin a goroutine and its framebuffer
	// copy forever.
	writeTimeout = 60 * time.Second
	// maxCutTextLen caps ClientCutText so a bogus length cannot make the
	// server drain gigabytes, and cannot overflow int on 32-bit builds.
	maxCutTextLen = 1 << 20
	// rdnsTimeout bounds the reverse lookup. It runs before the handshake,
	// so a slow or unreachable PTR authority must not stall the connection.
	rdnsTimeout = 700 * time.Millisecond
)

// overlayConfig selects which lines the client-info banner carries.
type overlayConfig struct {
	showIP   bool
	showRDNS bool
	showTime bool
}

func (o overlayConfig) enabled() bool { return o.showIP || o.showRDNS || o.showTime }

// lines builds the banner text for one connection. The reverse lookup only
// runs when it is going to be displayed, so the default configuration makes
// no DNS traffic at all.
func (o overlayConfig) lines(addr net.Addr, now time.Time) []string {
	var out []string
	ip := clientIP(addr)
	if o.showIP {
		out = append(out, "IP:   "+ip)
	}
	if o.showRDNS {
		host := lookupRDNS(ip)
		if host == "" {
			host = "(no PTR record)"
		}
		out = append(out, "Host: "+host)
	}
	if o.showTime {
		out = append(out, "Time: "+now.Format("2006-01-02 15:04:05 MST"))
	}
	return out
}

// lookupRDNS resolves the PTR record for ip, returning "" when there is none
// or the lookup does not finish in time.
func lookupRDNS(ip string) string {
	ctx, cancel := context.WithTimeout(context.Background(), rdnsTimeout)
	defer cancel()

	names, err := net.DefaultResolver.LookupAddr(ctx, ip)
	if err != nil || len(names) == 0 {
		return ""
	}
	return strings.TrimSuffix(names[0], ".")
}

type vncServer struct {
	ln      net.Listener
	rotator *ImageRotator
	name    string
	overlay overlayConfig
}

// newVNCServer binds the listener up front so bind failures are reported
// before the process claims to be running.
func newVNCServer(addr string, rotator *ImageRotator, serverName string, overlay overlayConfig) (*vncServer, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("failed to listen on %s: %w", addr, err)
	}

	// Peek at the first image for logging without advancing rotation state.
	firstImg := rotator.images[0].fb
	log.Printf("[%s] Serving %d images with rotation on %s", serverName, len(rotator.images), addr)
	log.Printf("[%s] First image: %dx%d", serverName, firstImg.w, firstImg.h)

	return &vncServer{ln: ln, rotator: rotator, name: serverName, overlay: overlay}, nil
}

func (s *vncServer) close() { s.ln.Close() }

func (s *vncServer) serve() {
	for {
		c, err := s.ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			log.Printf("[%s] Accept error: %v", s.name, err)
			time.Sleep(time.Second)
			continue
		}
		go serveWithRotator(c, s.rotator, s.name, s.overlay)
	}
}

// clientIP returns the remote host without its port, handling IPv6 literals.
func clientIP(addr net.Addr) string {
	if host, _, err := net.SplitHostPort(addr.String()); err == nil {
		return host
	}
	return addr.String()
}

func serveWithRotator(c net.Conn, rotator *ImageRotator, serverName string, overlay overlayConfig) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[%s] Recovered from panic serving %s: %v", serverName, c.RemoteAddr(), r)
		}
		c.Close()
		log.Printf("[%s] Client disconnected", serverName)
	}()

	// Get image from rotator for this connection
	f := rotator.GetImageForConnection()

	clientFB := f
	if overlay.enabled() {
		clientFB = addIPOverlay(f, overlay.lines(c.RemoteAddr(), time.Now())...)
	}
	log.Printf("[%s] Client connected from %s", serverName, c.RemoteAddr())

	// Handshake steps are short; one deadline covers reads and writes.
	if err := c.SetDeadline(time.Now().Add(readTimeout)); err != nil {
		log.Printf("[%s] Failed to set deadline: %v", serverName, err)
		return
	}

	_, err := c.Write([]byte(rfbVersion))
	if err != nil {
		log.Printf("[%s] Failed to send version: %v", serverName, err)
		return
	}

	buf := make([]byte, 12)
	_, err = io.ReadFull(c, buf)
	if err != nil {
		log.Printf("[%s] Failed to read client version: %v", serverName, err)
		return
	}

	_, err = c.Write([]byte{1, 1})
	if err != nil {
		log.Printf("[%s] Failed to send auth: %v", serverName, err)
		return
	}

	buf = make([]byte, 1)
	_, err = io.ReadFull(c, buf)
	if err != nil {
		log.Printf("[%s] Failed to read auth selection: %v", serverName, err)
		return
	}

	_, err = c.Write(make([]byte, 4))
	if err != nil {
		log.Printf("[%s] Failed to send auth result: %v", serverName, err)
		return
	}

	buf = make([]byte, 1)
	_, err = io.ReadFull(c, buf)
	if err != nil {
		log.Printf("[%s] Failed to read client init: %v", serverName, err)
		return
	}

	pf := pixelFormat{32, 24, 0, 1, 255, 255, 255, 16, 8, 0, [3]byte{}}
	err = sendServerInit(c, clientFB.w, clientFB.h, pf, serverName)
	if err != nil {
		log.Printf("[%s] Failed to send server init: %v", serverName, err)
		return
	}

	var lastRejectedFormat string
	supportsZRLE := false
	sentUpdate := false
	zstream := newZRLEStream()

	for {
		err := c.SetReadDeadline(time.Now().Add(readTimeout))
		if err != nil {
			log.Printf("[%s] Failed to set read deadline: %v", serverName, err)
			return
		}

		msgType, err := read1(c)
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				log.Printf("[%s] Read timeout, closing connection", serverName)
			} else if err != io.EOF {
				log.Printf("[%s] Read error: %v", serverName, err)
			}
			return
		}

		switch msgType {
		case msgSetPixelFormat:
			_, err := readN(c, 3)
			if err != nil {
				log.Printf("[%s] Failed to read padding: %v", serverName, err)
				return
			}

			var buf [16]byte
			_, err = io.ReadFull(c, buf[:])
			if err != nil {
				log.Printf("[%s] Failed to read pixel format: %v", serverName, err)
				return
			}

			var want pixelFormat
			binary.Read(bytes.NewReader(buf[:]), binary.BigEndian, &want)
			formatID := fmt.Sprintf("%dbpp trueColor=%d", want.BPP, want.TrueColor)
			if want.TrueColor == 1 && (want.BPP == 32 || want.BPP == 24 || want.BPP == 8) {
				pf = want
				log.Printf("[%s] Client pixel format: %s", serverName, formatID)
				lastRejectedFormat = ""
			} else {
				if formatID != lastRejectedFormat {
					log.Printf("[%s] Unsupported format: %s — ignoring", serverName, formatID)
					lastRejectedFormat = formatID
				}
			}
		case msgSetEncodings:
			_, err := readN(c, 1)
			if err != nil {
				return
			}

			n, err := read16(c)
			if err != nil {
				return
			}

			encs := make([]byte, int(n)*4)
			if _, err = io.ReadFull(c, encs); err != nil {
				return
			}
			supportsZRLE = false
			for i := 0; i+4 <= len(encs); i += 4 {
				if int32(binary.BigEndian.Uint32(encs[i:i+4])) == encZRLE {
					supportsZRLE = true
				}
			}
			if supportsZRLE {
				log.Printf("[%s] Client supports ZRLE encoding", serverName)
			}
		case msgEnableCU:
			_, err := readN(c, 9)
			if err != nil {
				return
			}
		case msgFramebufferUpdateReq:
			// incremental(1) + x(2) + y(2) + w(2) + h(2)
			var req [9]byte
			if _, err := io.ReadFull(c, req[:]); err != nil {
				return
			}
			// The framebuffer never changes, so an incremental request has
			// nothing to answer. Replying anyway makes clients spin: they
			// re-request as soon as each update lands, burning CPU and
			// bandwidth for an image that is already on screen. The very
			// first update is always sent, even if flagged incremental, so a
			// client never ends up staring at a blank screen.
			if req[0] != 0 && sentUpdate {
				continue
			}
			sentUpdate = true

			if err := c.SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil {
				log.Printf("[%s] Failed to set write deadline: %v", serverName, err)
				return
			}
			if enc, ok := newCPIXELEncoder(pf); supportsZRLE && ok {
				err = sendFramebufferZRLE(c, clientFB, enc, zstream)
			} else {
				err = sendFramebuffer(c, clientFB, pf)
			}
			if err != nil {
				log.Printf("[%s] Failed to send framebuffer: %v", serverName, err)
				return
			}
		case msgKeyEvent:
			// down-flag(1) + padding(2) + key(4)
			if _, err := readN(c, 7); err != nil {
				return
			}
		case msgPointerEvent:
			// button-mask(1) + x(2) + y(2)
			if _, err := readN(c, 5); err != nil {
				return
			}
		case msgClientCutText:
			// padding(3) + length(4) + text(length)
			if _, err := readN(c, 3); err != nil {
				return
			}
			n, err := read32(c)
			if err != nil {
				return
			}
			if n > maxCutTextLen {
				log.Printf("[%s] ClientCutText too large (%d bytes), closing connection", serverName, n)
				return
			}
			if _, err := readN(c, int(n)); err != nil {
				return
			}
		default:
			// The message length is unknown, so the stream cannot be resynced;
			// anything read from here on would be misinterpreted.
			log.Printf("[%s] Unknown message type %d, closing connection", serverName, msgType)
			return
		}
	}
}
