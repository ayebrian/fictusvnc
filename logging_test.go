package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestParseLevel(t *testing.T) {
	tests := []struct {
		in      string
		want    slog.Level
		wantErr bool
	}{
		{"", slog.LevelInfo, false},
		{"info", slog.LevelInfo, false},
		{"DEBUG", slog.LevelDebug, false},
		{" warn ", slog.LevelWarn, false},
		{"warning", slog.LevelWarn, false},
		{"error", slog.LevelError, false},
		{"trace", 0, true},
	}
	for _, tt := range tests {
		got, err := parseLevel(tt.in)
		if (err != nil) != tt.wantErr {
			t.Errorf("parseLevel(%q): err=%v wantErr=%v", tt.in, err, tt.wantErr)
			continue
		}
		if err == nil && got != tt.want {
			t.Errorf("parseLevel(%q): got %v want %v", tt.in, got, tt.want)
		}
	}
}

func TestSetupLoggingRejectsBadFormat(t *testing.T) {
	if _, _, err := setupLogging(LoggingConfig{Format: "xml"}); err == nil {
		t.Fatal("expected an error for an unknown format")
	}
}

func TestSetupLoggingWritesToFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fictusvnc.log")
	log, closer, err := setupLogging(LoggingConfig{Output: path, Format: "json"})
	if err != nil {
		t.Fatalf("setupLogging: %v", err)
	}
	defer closer.Close()

	log.Info("hello", "answer", 42)

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	var rec map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(data), &rec); err != nil {
		t.Fatalf("log line is not JSON: %v (%q)", err, data)
	}
	if rec["msg"] != "hello" || rec["answer"] != float64(42) {
		t.Errorf("unexpected record: %v", rec)
	}
}

// logrotate renames the file out from under the process and signals it. After
// reopening, writes must land in a fresh file at the original path rather than
// in the rotated-away inode.
func TestReopenWriterSurvivesRotation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")

	w, err := newReopenWriter(path)
	if err != nil {
		t.Fatalf("newReopenWriter: %v", err)
	}
	defer w.Close()

	if _, err := w.Write([]byte("before\n")); err != nil {
		t.Fatalf("write: %v", err)
	}

	rotated := filepath.Join(dir, "app.log.1")
	if err := os.Rename(path, rotated); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if err := w.reopen(); err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if _, err := w.Write([]byte("after\n")); err != nil {
		t.Fatalf("write after reopen: %v", err)
	}

	fresh, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read new file: %v", err)
	}
	if string(fresh) != "after\n" {
		t.Errorf("new file: got %q want %q", fresh, "after\n")
	}

	old, err := os.ReadFile(rotated)
	if err != nil {
		t.Fatalf("read rotated file: %v", err)
	}
	if string(old) != "before\n" {
		t.Errorf("rotated file: got %q want %q", old, "before\n")
	}
}

func TestReopenWriterIsConcurrencySafe(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.log")
	w, err := newReopenWriter(path)
	if err != nil {
		t.Fatalf("newReopenWriter: %v", err)
	}
	defer w.Close()

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 20 {
				w.Write([]byte("line\n"))
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range 5 {
			w.reopen()
		}
	}()
	wg.Wait()
}

// captureLogs runs a full client session against the server and returns the
// structured records it produced.
func captureLogs(t *testing.T, run func(conn net.Conn, log *slog.Logger)) []map[string]any {
	t.Helper()

	var mu sync.Mutex
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&lockedWriter{mu: &mu, w: &buf}, &slog.HandlerOptions{Level: slog.LevelDebug}))

	run(nil, log)

	mu.Lock()
	defer mu.Unlock()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("log line is not JSON: %v (%q)", err, line)
		}
		out = append(out, rec)
	}
	return out
}

type lockedWriter struct {
	mu *sync.Mutex
	w  *bytes.Buffer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// The whole point of the connection record: one event carrying the client
// fingerprint, not a scatter of unrelated lines.
func TestConnectionEventCarriesClientFingerprint(t *testing.T) {
	src := makeTestFB(64, 64)

	records := captureLogs(t, func(_ net.Conn, log *slog.Logger) {
		srv, cli := net.Pipe()
		done := make(chan struct{})
		go func() {
			defer close(done)
			serveWithRotator(srv, testRotator(src), "test", log, overlayConfig{})
		}()

		// Client side: full RFB 3.8 handshake, then advertise encodings and
		// ask for one update.
		mustRead(t, cli, make([]byte, 12))
		cli.Write([]byte("RFB 003.008\n"))
		mustRead(t, cli, make([]byte, 2))
		cli.Write([]byte{1})
		mustRead(t, cli, make([]byte, 4))
		cli.Write([]byte{1})

		head := make([]byte, 24)
		mustRead(t, cli, head)
		mustRead(t, cli, make([]byte, binary.BigEndian.Uint32(head[20:24])))

		// SetEncodings: ZRLE, Raw, plus a pseudo-encoding, in the client's
		// own order.
		enc := []byte{msgSetEncodings, 0, 0, 3}
		for _, e := range []int32{encZRLE, encRaw, -239} {
			var b [4]byte
			binary.BigEndian.PutUint32(b[:], uint32(e))
			enc = append(enc, b[:]...)
		}
		cli.Write(enc)

		cli.Write(fbUpdateRequest(0))
		// Drain the whole update before hanging up. net.Pipe is unbuffered, so
		// leaving the zlib payload unread would block the server mid-write and
		// the update would never be counted as delivered.
		mustRead(t, cli, make([]byte, 16)) // update header + rectangle header
		zlen := make([]byte, 4)
		mustRead(t, cli, zlen)
		mustRead(t, cli, make([]byte, binary.BigEndian.Uint32(zlen)))
		cli.Close()
		<-done
	})

	var conn map[string]any
	for _, r := range records {
		if r["msg"] == "connection" {
			conn = r
		}
	}
	if conn == nil {
		t.Fatalf("no connection record emitted; got %v", records)
	}

	if conn["client_version"] != "RFB 003.008" {
		t.Errorf("client_version: got %v", conn["client_version"])
	}
	if conn["handshake"] != true {
		t.Errorf("handshake: got %v want true", conn["handshake"])
	}
	if conn["security_type"] != float64(1) {
		t.Errorf("security_type: got %v want 1", conn["security_type"])
	}
	if conn["encoding_used"] != "zrle" {
		t.Errorf("encoding_used: got %v want zrle", conn["encoding_used"])
	}
	if conn["image"] != "test.png" {
		t.Errorf("image: got %v want test.png", conn["image"])
	}
	if conn["updates"] != float64(1) {
		t.Errorf("updates: got %v want 1", conn["updates"])
	}
	if b, ok := conn["bytes_sent"].(float64); !ok || b <= 0 {
		t.Errorf("bytes_sent: got %v want > 0", conn["bytes_sent"])
	}
	if _, ok := conn["duration_ms"]; !ok {
		t.Error("duration_ms is missing")
	}

	// The encoding list must survive in the client's order — it is the
	// fingerprint an analyst correlates on.
	encs, ok := conn["encodings"].([]any)
	if !ok || len(encs) != 3 {
		t.Fatalf("encodings: got %v", conn["encodings"])
	}
	want := []float64{float64(encZRLE), float64(encRaw), -239}
	for i, w := range want {
		if encs[i] != w {
			t.Errorf("encodings[%d]: got %v want %v", i, encs[i], w)
		}
	}
}

// A scanner that opens a socket and drops it must still produce one record,
// marked as never having reached the handshake.
func TestConnectionEventForDroppedProbe(t *testing.T) {
	records := captureLogs(t, func(_ net.Conn, log *slog.Logger) {
		srv, cli := net.Pipe()
		done := make(chan struct{})
		go func() {
			defer close(done)
			serveWithRotator(srv, testRotator(makeTestFB(32, 32)), "test", log, overlayConfig{})
		}()
		mustRead(t, cli, make([]byte, 12)) // read the greeting, then vanish
		cli.Close()
		<-done
	})

	for _, r := range records {
		if r["msg"] != "connection" {
			continue
		}
		if r["handshake"] != false {
			t.Errorf("handshake: got %v want false", r["handshake"])
		}
		if r["outcome"] != "version_read_failed" {
			t.Errorf("outcome: got %v want version_read_failed", r["outcome"])
		}
		if _, ok := r["image"]; ok {
			t.Error("image should be omitted when the handshake never completed")
		}
		return
	}
	t.Fatalf("no connection record emitted; got %v", records)
}

func TestConnectionEventOutcomeForUnknownMessage(t *testing.T) {
	src := makeTestFB(32, 32)
	records := captureLogs(t, func(_ net.Conn, log *slog.Logger) {
		srv, cli := net.Pipe()
		done := make(chan struct{})
		go func() {
			defer close(done)
			serveWithRotator(srv, testRotator(src), "test", log, overlayConfig{})
		}()

		mustRead(t, cli, make([]byte, 12))
		cli.Write([]byte("RFB 003.008\n"))
		mustRead(t, cli, make([]byte, 2))
		cli.Write([]byte{1})
		mustRead(t, cli, make([]byte, 4))
		cli.Write([]byte{1})
		head := make([]byte, 24)
		mustRead(t, cli, head)
		mustRead(t, cli, make([]byte, binary.BigEndian.Uint32(head[20:24])))

		cli.Write([]byte{200})
		cli.SetReadDeadline(time.Now().Add(2 * time.Second))
		cli.Read(make([]byte, 1))
		cli.Close()
		<-done
	})

	for _, r := range records {
		if r["msg"] == "connection" {
			if r["outcome"] != "unknown_message" {
				t.Errorf("outcome: got %v want unknown_message", r["outcome"])
			}
			return
		}
	}
	t.Fatal("no connection record emitted")
}
