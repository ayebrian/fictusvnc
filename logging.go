package main

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
)

// LoggingConfig controls where and how the server writes its log stream.
type LoggingConfig struct {
	Level  string `toml:"level"`  // debug, info, warn, error
	Format string `toml:"format"` // json, text
	Output string `toml:"output"` // stdout, stderr, or a file path
}

func parseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "info":
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	}
	return 0, fmt.Errorf("unknown log level %q (want debug, info, warn or error)", s)
}

// reopenWriter writes to a log file that can be reopened while the process
// runs. logrotate renames the file and signals the process; without reopening,
// the server would keep writing to the rotated-away inode forever.
type reopenWriter struct {
	path string

	mu sync.Mutex
	f  *os.File
}

func newReopenWriter(path string) (*reopenWriter, error) {
	w := &reopenWriter{path: path}
	if err := w.reopen(); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *reopenWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.f.Write(p)
}

func (w *reopenWriter) reopen() error {
	f, err := os.OpenFile(w.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o640)
	if err != nil {
		return err
	}
	w.mu.Lock()
	old := w.f
	w.f = f
	w.mu.Unlock()
	if old != nil {
		old.Close()
	}
	return nil
}

func (w *reopenWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.f.Close()
}

// watchSIGHUP reopens the log file every time the process is signalled, so
// logrotate's default "rename then HUP" workflow works. The signal is never
// delivered on Windows, where the goroutine simply idles.
func (w *reopenWriter) watchSIGHUP(log *slog.Logger) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGHUP)
	go func() {
		for range ch {
			if err := w.reopen(); err != nil {
				log.Error("failed to reopen log file", "path", w.path, "error", err)
				continue
			}
			log.Info("reopened log file", "path", w.path)
		}
	}()
}

// setupLogging builds the process logger and installs it as the slog default.
// The returned closer releases the log file, if one was opened.
func setupLogging(cfg LoggingConfig) (*slog.Logger, io.Closer, error) {
	level, err := parseLevel(cfg.Level)
	if err != nil {
		return nil, nil, err
	}

	var (
		w      io.Writer
		closer io.Closer
		file   *reopenWriter
	)
	switch strings.ToLower(strings.TrimSpace(cfg.Output)) {
	case "", "stdout":
		w = os.Stdout
	case "stderr":
		w = os.Stderr
	default:
		file, err = newReopenWriter(cfg.Output)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to open log file: %w", err)
		}
		w, closer = file, file
	}

	opts := &slog.HandlerOptions{Level: level}
	var h slog.Handler
	switch strings.ToLower(strings.TrimSpace(cfg.Format)) {
	case "", "json":
		h = slog.NewJSONHandler(w, opts)
	case "text":
		h = slog.NewTextHandler(w, opts)
	default:
		if closer != nil {
			closer.Close()
		}
		return nil, nil, fmt.Errorf("unknown log format %q (want json or text)", cfg.Format)
	}

	log := slog.New(h)
	slog.SetDefault(log)

	if file != nil {
		file.watchSIGHUP(log)
	}
	return log, closer, nil
}
