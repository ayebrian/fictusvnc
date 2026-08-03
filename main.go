// main.go
// Minimal VNC server main entry point and basic utilities.

package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
)

// Importing global variables from config.go
var (
	showVersion bool
)

func main() {
	configPath := flag.String("config", "", "Path to TOML config file (default: ./config.toml)")
	flag.BoolVar(&showVersion, "version", false, "Show version and exit")
	flag.BoolVar(&showVersion, "v", false, "Show version and exit (shorthand)")
	flag.Parse()

	if showVersion {
		fmt.Printf("FictusVNC %s\n", appVersion)
		return
	}

	log.Printf("[INFO] FictusVNC %s starting…", appVersion)

	if *configPath == "" {
		exe, _ := os.Executable()
		dir := filepath.Dir(exe)
		*configPath = filepath.Join(dir, "config.toml")
	}

	if _, err := os.Stat(*configPath); err != nil {
		log.Printf("[ERROR] No configuration file found at %s", *configPath)
		log.Printf("[INFO] Create a config.toml file or specify path with --config")
		os.Exit(1)
	}

	cfg, err := loadConfig(*configPath)
	check(err)

	// Image paths are resolved relative to the config file, not the working
	// directory, so the server behaves the same started by hand or by systemd.
	baseDir, err := filepath.Abs(filepath.Dir(*configPath))
	check(err)

	var servers []*vncServer
	for serverID, s := range cfg.Server {
		rotator, err := NewImageRotator(s, baseDir)
		if err != nil {
			log.Printf("[ERROR] Failed to create image rotator for '%s': %v", serverID, err)
			continue
		}

		name := s.Name
		if name == "" {
			name = serverID
		}
		if cfg.Global.Branding {
			name = fmt.Sprintf("%s - %s", cfg.Global.Name, name)
		}

		for _, addr := range listenAddrs(s) {
			srv, err := newVNCServer(addr, rotator, name, cfg.Global.overlay())
			if err != nil {
				log.Printf("[WARN] Failed to start server %s on %s: %v", name, addr, err)
				continue
			}
			servers = append(servers, srv)
		}
		if len(listenAddrs(s)) == 0 {
			log.Printf("[ERROR] Server %s has no listen address or valid port range", name)
		}
	}

	if len(servers) == 0 {
		log.Printf("[ERROR] No servers could be started — check listen addresses and image paths")
		os.Exit(1)
	}

	for _, srv := range servers {
		go srv.serve()
	}
	log.Printf("[INFO] %d listener(s) active", len(servers))

	// Block until the process is asked to stop, so a shutdown is clean and a
	// zero-listener config can never leave the process hanging silently.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	log.Printf("[INFO] Shutting down")
	for _, srv := range servers {
		srv.close()
	}
}

// listenAddrs expands a server entry into the concrete addresses it should
// bind, honouring an optional start_port/end_port range.
func listenAddrs(s ServerConfig) []string {
	if s.StartPort > 0 && s.EndPort > 0 && s.EndPort >= s.StartPort {
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

func check(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
