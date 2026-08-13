package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
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
	// rdnsTimeout bounds the reverse lookup, so a slow or unreachable PTR
	// authority cannot stall the first framebuffer update for long.
	rdnsTimeout = 700 * time.Millisecond
	// maxLoggedEncodings caps the encoding list kept for fingerprinting. The
	// order of the first handful identifies the client software; the tail is
	// pseudo-encodings nobody correlates on.
	maxLoggedEncodings = 32
)

// overlayConfig selects which lines the client-info banner carries.
type overlayConfig struct {
	showIP   bool
	showRDNS bool
	showTime bool
}

func (o overlayConfig) enabled() bool { return o.showIP || o.showRDNS || o.showTime }

// needsRDNS reports whether a reverse lookup has to happen for this connection.
func (o overlayConfig) needsRDNS() bool { return o.showRDNS }

// lines builds the banner text for one connection. rdns is passed in rather
// than resolved here so a connection performs at most one lookup.
func (o overlayConfig) lines(ip, rdns string, now time.Time) []string {
	var out []string
	if o.showIP {
		out = append(out, "IP:   "+ip)
	}
	if o.showRDNS {
		if rdns == "" {
			rdns = "(no PTR record)"
		}
		out = append(out, "Host: "+rdns)
	}
	if o.showTime {
		out = append(out, "Time: "+now.Format("2006-01-02 15:04:05 MST"))
	}
	return out
}

// lookupRDNS resolves the PTR record for ip, returning "" when there is none
// or the lookup does not finish in time. It is a variable so tests can stub
// the resolver and assert when a lookup actually happens.
var lookupRDNS = func(ip string) string {
	ctx, cancel := context.WithTimeout(context.Background(), rdnsTimeout)
	defer cancel()

	names, err := net.DefaultResolver.LookupAddr(ctx, ip)
	if err != nil || len(names) == 0 {
		return ""
	}
	return strings.TrimSuffix(names[0], ".")
}

// countingConn tallies everything written to the client so the connection
// record can report how much traffic the peer actually cost.
type countingConn struct {
	net.Conn
	written int64
}

func (c *countingConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	c.written += int64(n)
	return n, err
}

// connEvent accumulates one connection's story and is emitted as a single
// structured record when the connection ends. One event per connection beats
// a scatter of unrelated lines: it can be correlated, aggregated and shipped
// to Elasticsearch or Loki without a parser.
type connEvent struct {
	start time.Time

	peerIP   string
	peerPort int
	rdns     string

	clientVersion string
	securityType  int
	handshake     bool

	encodings    []int32
	encodingUsed string
	pixelBPP     int
	pixelDepth   int

	image     string
	updates   int
	bytesSent int64

	outcome string
}

// emit writes the connection record. Slices and optional fields are omitted
// when empty so the JSON stays small for the many connections that are just a
// scanner opening and dropping a socket.
func (e *connEvent) emit(log *slog.Logger) {
	attrs := []any{
		"peer_ip", e.peerIP,
		"peer_port", e.peerPort,
		"handshake", e.handshake,
		"outcome", e.outcome,
		"duration_ms", time.Since(e.start).Milliseconds(),
		"bytes_sent", e.bytesSent,
	}
	if e.rdns != "" {
		attrs = append(attrs, "rdns", e.rdns)
	}
	if e.clientVersion != "" {
		attrs = append(attrs, "client_version", e.clientVersion)
	}
	if e.handshake {
		attrs = append(attrs,
			"security_type", e.securityType,
			"image", e.image,
			"updates", e.updates,
			"pixel_bpp", e.pixelBPP,
			"pixel_depth", e.pixelDepth,
		)
	}
	if len(e.encodings) > 0 {
		attrs = append(attrs, "encodings", e.encodings)
	}
	if e.encodingUsed != "" {
		attrs = append(attrs, "encoding_used", e.encodingUsed)
	}
	log.Info("connection", attrs...)
}

// connLimiter caps how many clients are served at once. It is shared by every
// listener, because the resource being protected — memory held by per-client
// framebuffer copies — is process-wide, not per-port. A nil limiter is
// unlimited, so the zero value stays usable in tests.
type connLimiter struct {
	slots chan struct{}
}

func newConnLimiter(max int) *connLimiter {
	if max <= 0 {
		return nil
	}
	return &connLimiter{slots: make(chan struct{}, max)}
}

// acquire takes a slot without blocking, reporting whether one was free.
func (l *connLimiter) acquire() bool {
	if l == nil {
		return true
	}
	select {
	case l.slots <- struct{}{}:
		return true
	default:
		return false
	}
}

func (l *connLimiter) release() {
	if l != nil {
		<-l.slots
	}
}

func (l *connLimiter) capacity() int {
	if l == nil {
		return 0
	}
	return cap(l.slots)
}

type vncServer struct {
	ln      net.Listener
	rotator *ImageRotator
	name    string
	log     *slog.Logger
	overlay overlayConfig
	limiter *connLimiter
}

// newVNCServer binds the listener up front so bind failures are reported
// before the process claims to be running.
func newVNCServer(addr string, rotator *ImageRotator, serverName string, overlay overlayConfig, limiter *connLimiter, log *slog.Logger) (*vncServer, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("failed to listen on %s: %w", addr, err)
	}

	// Peek at the first image for logging without advancing rotation state.
	firstImg := rotator.images[0].fb
	log = log.With("server", serverName, "listen", addr)
	log.Info("listening",
		"images", len(rotator.images),
		"rotation_mode", rotator.modeName(),
		"first_image_width", firstImg.w,
		"first_image_height", firstImg.h,
	)

	return &vncServer{
		ln:      ln,
		rotator: rotator,
		name:    serverName,
		log:     log,
		overlay: overlay,
		limiter: limiter,
	}, nil
}

func (s *vncServer) close() { s.ln.Close() }

func (s *vncServer) serve() {
	for {
		c, err := s.ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			s.log.Error("accept failed", "error", err)
			time.Sleep(time.Second)
			continue
		}

		if !s.limiter.acquire() {
			// Still worth a record: a flood that trips the cap is exactly
			// what an operator wants to see, and it aggregates with every
			// other connection outcome.
			ev := &connEvent{start: time.Now(), outcome: "connection_limit"}
			ev.peerIP, ev.peerPort = splitPeer(c.RemoteAddr())
			ev.emit(s.log)
			c.Close()
			continue
		}

		go func() {
			defer s.limiter.release()
			serveWithRotator(c, s.rotator, s.name, s.log, s.overlay)
		}()
	}
}

// clientIP returns the remote host without its port, handling IPv6 literals.
func clientIP(addr net.Addr) string {
	host, _ := splitPeer(addr)
	return host
}

// splitPeer separates a remote address into host and port, tolerating an
// address that carries no port at all.
func splitPeer(addr net.Addr) (string, int) {
	host, port, err := net.SplitHostPort(addr.String())
	if err != nil {
		return addr.String(), 0
	}
	n, _ := strconv.Atoi(port)
	return host, n
}

func serveWithRotator(c net.Conn, rotator *ImageRotator, serverName string, log *slog.Logger, overlay overlayConfig) {
	cc := &countingConn{Conn: c}
	ev := &connEvent{start: time.Now(), outcome: "closed"}
	ev.peerIP, ev.peerPort = splitPeer(c.RemoteAddr())

	defer func() {
		if r := recover(); r != nil {
			ev.outcome = "panic"
			log.Error("recovered from panic while serving a client",
				"peer_ip", ev.peerIP, "panic", fmt.Sprint(r))
		}
		c.Close()
		ev.bytesSent = cc.written
		ev.emit(log)
	}()

	log.Debug("client connected", "peer_ip", ev.peerIP, "peer_port", ev.peerPort)
	ev.outcome = session(cc, rotator, serverName, log, overlay, ev)
}

// session runs one client to completion and returns the reason it ended. The
// outcome string is the field an operator groups by when asking "what are the
// scanners actually doing", so the values are coarse and stable.
func session(c net.Conn, rotator *ImageRotator, serverName string, log *slog.Logger, overlay overlayConfig, ev *connEvent) string {
	f, imagePath := rotator.GetImageForConnection()
	ev.image = imagePath

	// The overlay costs a full framebuffer copy — ~8 MB at 1080p — plus a PTR
	// lookup when show_rdns is on, so it is built lazily on the first update
	// request. Scanners that connect and drop, the overwhelming majority of
	// traffic on an exposed honeypot, never pay for it, and the greeting is
	// never delayed by DNS. The overlay keeps the image dimensions, so
	// ServerInit can be answered from the original frame.
	clientFB := f
	overlayBuilt := !overlay.enabled()
	getFB := func() *fb {
		if !overlayBuilt {
			if overlay.needsRDNS() {
				ev.rdns = lookupRDNS(ev.peerIP)
			}
			clientFB = addIPOverlay(f, overlay.lines(ev.peerIP, ev.rdns, time.Now())...)
			overlayBuilt = true
		}
		return clientFB
	}

	// Handshake steps are short; one deadline covers reads and writes.
	if err := c.SetDeadline(time.Now().Add(readTimeout)); err != nil {
		log.Error("failed to set deadline", "error", err)
		return "setup_error"
	}

	if _, err := c.Write([]byte(rfbVersion)); err != nil {
		return "version_write_failed"
	}

	version := make([]byte, 12)
	if _, err := io.ReadFull(c, version); err != nil {
		return "version_read_failed"
	}
	ev.clientVersion = strings.TrimSpace(string(version))

	major, minor, ok := parseRFBVersion(version)
	if !ok {
		log.Debug("malformed RFB version greeting", "raw", ev.clientVersion)
		return "malformed_version"
	}
	if !supportedRFBVersion(major, minor) {
		// Pre-3.7 clients expect the server to choose the security type over
		// a different message flow; there is no correct reply to send them
		// here, so hang up rather than desync.
		log.Debug("unsupported RFB version", "major", major, "minor", minor)
		return "unsupported_version"
	}

	if _, err := c.Write([]byte{1, secTypeNone}); err != nil {
		return "security_write_failed"
	}

	var sec [1]byte
	if _, err := io.ReadFull(c, sec[:]); err != nil {
		return "security_read_failed"
	}
	ev.securityType = int(sec[0])
	rfb38 := usesRFB38Handshake(major, minor)
	if sec[0] != secTypeNone {
		// Only "None" was offered. RFB 3.8 reports the refusal with a
		// reason-string SecurityResult; 3.7 has no such message, so the
		// connection is simply dropped. Either way it ends here.
		log.Debug("client picked a security type that was not offered", "type", sec[0])
		if rfb38 {
			writeSecurityFailure(c, "Unsupported security type")
		}
		return "bad_security_type"
	}

	// RFB 3.8 always sends a SecurityResult, even for None; 3.7 goes straight to
	// ClientInit. Sending the extra 4 bytes to a 3.7 client shifts every
	// following field by four bytes and desyncs the whole session.
	if rfb38 {
		if _, err := c.Write(make([]byte, 4)); err != nil {
			return "security_result_write_failed"
		}
	}

	var shared [1]byte
	if _, err := io.ReadFull(c, shared[:]); err != nil {
		return "client_init_read_failed"
	}

	pf := pixelFormat{32, 24, 0, 1, 255, 255, 255, 16, 8, 0, [3]byte{}}
	ev.pixelBPP, ev.pixelDepth = int(pf.BPP), int(pf.Depth)
	if err := sendServerInit(c, f.w, f.h, pf, serverName); err != nil {
		return "server_init_failed"
	}
	ev.handshake = true

	var lastRejectedFormat string
	supportsZRLE := false
	sentUpdate := false
	zstream := newZRLEStream()

	// The framebuffer is immutable for the life of a connection, so its encoded
	// bytes are computed once per (frame, pixel format, encoding) and reused.
	// Without this, a client that spams non-incremental update requests forces a
	// full re-encode of an unchanging image on every request.
	var (
		encBody   []byte
		encFB     *fb
		encPF     pixelFormat
		encZRLEd  bool
		encCached bool
	)

	for {
		if err := c.SetReadDeadline(time.Now().Add(readTimeout)); err != nil {
			log.Error("failed to set read deadline", "error", err)
			return "setup_error"
		}

		msgType, err := read1(c)
		if err != nil {
			var netErr net.Error
			switch {
			case errors.As(err, &netErr) && netErr.Timeout():
				return "idle_timeout"
			case errors.Is(err, io.EOF):
				return "client_eof"
			default:
				return "read_error"
			}
		}

		switch msgType {
		case msgSetPixelFormat:
			if _, err := readN(c, 3); err != nil {
				return "read_error"
			}

			var buf [16]byte
			if _, err := io.ReadFull(c, buf[:]); err != nil {
				return "read_error"
			}

			var want pixelFormat
			binary.Read(bytes.NewReader(buf[:]), binary.BigEndian, &want)
			formatID := fmt.Sprintf("%dbpp trueColor=%d", want.BPP, want.TrueColor)
			if want.TrueColor == 1 && (want.BPP == 32 || want.BPP == 24 || want.BPP == 8) {
				pf = want
				ev.pixelBPP, ev.pixelDepth = int(pf.BPP), int(pf.Depth)
				log.Debug("client pixel format", "bpp", want.BPP, "depth", want.Depth)
				lastRejectedFormat = ""
			} else if formatID != lastRejectedFormat {
				log.Debug("ignoring unsupported pixel format", "bpp", want.BPP, "true_color", want.TrueColor)
				lastRejectedFormat = formatID
			}

		case msgSetEncodings:
			if _, err := readN(c, 1); err != nil {
				return "read_error"
			}

			n, err := read16(c)
			if err != nil {
				return "read_error"
			}

			encs := make([]byte, int(n)*4)
			if _, err = io.ReadFull(c, encs); err != nil {
				return "read_error"
			}
			supportsZRLE = false
			// The encoding list, in the client's own order, is the single
			// best fingerprint of which VNC software is on the other end.
			ev.encodings = ev.encodings[:0]
			for i := 0; i+4 <= len(encs); i += 4 {
				e := int32(binary.BigEndian.Uint32(encs[i : i+4]))
				if e == encZRLE {
					supportsZRLE = true
				}
				if len(ev.encodings) < maxLoggedEncodings {
					ev.encodings = append(ev.encodings, e)
				}
			}
			log.Debug("client encodings", "count", n, "zrle", supportsZRLE)

		case msgEnableCU:
			if _, err := readN(c, 9); err != nil {
				return "read_error"
			}

		case msgFramebufferUpdateReq:
			// incremental(1) + x(2) + y(2) + w(2) + h(2)
			var req [9]byte
			if _, err := io.ReadFull(c, req[:]); err != nil {
				return "read_error"
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
				log.Error("failed to set write deadline", "error", err)
				return "setup_error"
			}
			fbOut := getFB()
			enc, encOK := newCPIXELEncoder(pf)
			useZRLE := supportsZRLE && encOK

			// Rebuild the encoded body only when the frame, the negotiated
			// pixel format or the chosen encoding actually changes; otherwise
			// reuse the cached bytes. (pixelFormat is comparable; its blank
			// padding field is ignored by ==.)
			if !encCached || encFB != fbOut || encPF != pf || encZRLEd != useZRLE {
				if useZRLE {
					encBody = encodeZRLETiles(fbOut, enc)
				} else {
					encBody = rawFramebufferBody(fbOut, pf)
				}
				encFB, encPF, encZRLEd, encCached = fbOut, pf, useZRLE, true
			}

			if useZRLE {
				ev.encodingUsed = "zrle"
				err = writeFramebufferZRLE(c, fbOut.w, fbOut.h, encBody, zstream)
			} else {
				ev.encodingUsed = "raw"
				err = writeRawFramebuffer(c, fbOut.w, fbOut.h, encBody)
			}
			if err != nil {
				return "update_write_failed"
			}
			ev.updates++

		case msgKeyEvent:
			// down-flag(1) + padding(2) + key(4)
			if _, err := readN(c, 7); err != nil {
				return "read_error"
			}

		case msgPointerEvent:
			// button-mask(1) + x(2) + y(2)
			if _, err := readN(c, 5); err != nil {
				return "read_error"
			}

		case msgClientCutText:
			// padding(3) + length(4) + text(length)
			if _, err := readN(c, 3); err != nil {
				return "read_error"
			}
			n, err := read32(c)
			if err != nil {
				return "read_error"
			}
			if n > maxCutTextLen {
				log.Debug("oversized ClientCutText, closing", "length", n)
				return "cut_text_too_large"
			}
			if _, err := readN(c, int(n)); err != nil {
				return "read_error"
			}

		default:
			// The message length is unknown, so the stream cannot be resynced;
			// anything read from here on would be misinterpreted.
			log.Debug("unknown message type, closing", "type", msgType)
			return "unknown_message"
		}
	}
}
