// main.go
// Minimal VNC server main entry point and basic utilities.

package main

import (
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
)

// Importing global variables from config.go
var (
	showVersion bool
)

func main() {
	configPath := flag.String("config", "", "Path to TOML config file (default: ./config.toml)")
	checkOnly := flag.Bool("check", false, "Validate the config, print a summary and exit without listening")
	flag.BoolVar(&showVersion, "version", false, "Show version and exit")
	flag.BoolVar(&showVersion, "v", false, "Show version and exit (shorthand)")
	flag.Parse()

	if showVersion {
		fmt.Printf("FictusVNC %s\n", appVersion)
		return
	}

	if *configPath == "" {
		exe, _ := os.Executable()
		dir := filepath.Dir(exe)
		*configPath = filepath.Join(dir, "config.toml")
	}

	if _, err := os.Stat(*configPath); err != nil {
		// The logger is not configured yet, so this one goes straight to
		// stderr in plain text.
		fmt.Fprintf(os.Stderr, "no configuration file found at %s\n", *configPath)
		fmt.Fprintf(os.Stderr, "create a config.toml file or pass --config\n")
		os.Exit(1)
	}

	cfg, warnings, err := loadConfig(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load %s: %v\n", *configPath, err)
		os.Exit(1)
	}

	// Image paths are resolved relative to the config file, not the working
	// directory, so the server behaves the same started by hand or by systemd.
	baseDir, err := filepath.Abs(filepath.Dir(*configPath))
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to resolve config directory: %v\n", err)
		os.Exit(1)
	}

	// --check reports to stdout in plain text and never touches the logger,
	// which would otherwise emit JSON into the middle of the summary.
	if *checkOnly {
		os.Exit(runCheck(os.Stdout, cfg, warnings, baseDir, *configPath))
	}

	log, closeLog, err := setupLogging(cfg.Logging)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to set up logging: %v\n", err)
		os.Exit(1)
	}
	if closeLog != nil {
		defer closeLog.Close()
	}

	// Deprecation notices were collected while the logger was still being
	// built; replay them now so they land in the configured stream.
	for _, w := range warnings {
		log.Warn("config", "detail", w)
	}

	log.Info("starting",
		"version", appVersion,
		"go_version", runtime.Version(),
		"config", *configPath,
	)

	// One limiter for the whole process: the memory it protects is shared by
	// every listener.
	limiter := newConnLimiter(cfg.Global.MaxConnections)

	var servers []*vncServer
	for serverID, s := range cfg.Server {
		rotator, err := NewImageRotator(s, baseDir)
		if err != nil {
			log.Error("failed to load images for server", "server_id", serverID, "error", err)
			continue
		}

		name := s.Name
		if name == "" {
			name = serverID
		}
		if cfg.Global.Branding {
			name = fmt.Sprintf("%s - %s", cfg.Global.Name, name)
		}

		addrs := listenAddrs(s)
		if len(addrs) == 0 {
			log.Error("server has no listen address or valid port range",
				"server_id", serverID, "server", name)
			continue
		}
		for _, addr := range addrs {
			srv, err := newVNCServer(addr, rotator, name, cfg.Global.overlayFor(s), limiter, log)
			if err != nil {
				log.Warn("failed to bind listener", "server", name, "listen", addr, "error", err)
				continue
			}
			servers = append(servers, srv)
		}
	}

	if len(servers) == 0 {
		log.Error("no servers could be started, check listen addresses and image paths")
		os.Exit(1)
	}

	for _, srv := range servers {
		go srv.serve()
	}
	log.Info("ready", "listeners", len(servers), "max_connections", limiter.capacity())

	// Block until the process is asked to stop, so a shutdown is clean and a
	// zero-listener config can never leave the process hanging silently.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	s := <-sig
	log.Info("shutting down", "signal", s.String())
	for _, srv := range servers {
		srv.close()
	}
}

// listenAddrs expands a server entry into the concrete addresses it should
// bind, honouring an optional start_port/end_port range.
func listenAddrs(s ServerConfig) []string {
	// A range is honoured only when it is well-formed and within 1..maxPort;
	// an out-of-range or inverted range falls through to the single Listen
	// address (or nothing), and never expands into a giant slice.
	if s.StartPort > 0 && s.EndPort >= s.StartPort && s.EndPort <= maxPort {
		host := s.Listen
		// Take the host part of Listen (handles IPv4, bare IPv6 and host:port)
		// and attach the ranged port.
		if h, _, err := net.SplitHostPort(s.Listen); err == nil {
			host = h
		}
		addrs := make([]string, 0, s.EndPort-s.StartPort+1)
		for port := s.StartPort; port <= s.EndPort; port++ {
			addrs = append(addrs, net.JoinHostPort(host, strconv.Itoa(port)))
		}
		return addrs
	}
	if s.Listen != "" {
		return []string{s.Listen}
	}
	return nil
}
