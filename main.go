// main.go
// Minimal VNC server main entry point and basic utilities.

package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strconv"

	"github.com/BurntSushi/toml"
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

	var cfg Config
	if _, err := os.Stat(*configPath); err == nil {
		_, err := toml.DecodeFile(*configPath, &cfg)
		check(err)

		// Set defaults for global config
		if cfg.Global.Name == "" {
			cfg.Global.Name = "FictusVNC"
		}

		for serverID, s := range cfg.Server {
			// Create image rotator
			rotator, err := NewImageRotator(s)
			if err != nil {
				log.Printf("[ERROR] Failed to create image rotator for '%s': %v", serverID, err)
				continue
			}

			name := s.Name
			if name == "" {
				name = serverID
			}
			if name == "" {
				name = cfg.Global.Name
			}
			if !cfg.Global.NoBrand {
				name = fmt.Sprintf("FictusVNC - %s", name)
			}

			// Handle port range if specified
			if s.StartPort > 0 && s.EndPort > 0 && s.EndPort >= s.StartPort {
				for port := s.StartPort; port <= s.EndPort; port++ {
					addr := fmt.Sprintf(":%d", port)
					if s.Listen != "" {
						// Take the host part of Listen (handles IPv4, bare
						// IPv6 and host:port) and attach the ranged port.
						host := s.Listen
						if h, _, err := net.SplitHostPort(s.Listen); err == nil {
							host = h
						}
						addr = net.JoinHostPort(host, strconv.Itoa(port))
					}
					// Start server in a separate goroutine and handle errors
					go func(addr string, rot *ImageRotator) {
						if err := runVNCServerWithRotator(addr, rot, name, cfg.Global.ShowIP); err != nil {
							log.Printf("[WARN] Failed to start server %s on %s: %v", name, addr, err)
						}
					}(addr, rotator)
				}
			} else if s.Listen != "" {
				go func(rot *ImageRotator) {
					if err := runVNCServerWithRotator(s.Listen, rot, name, cfg.Global.ShowIP); err != nil {
						log.Printf("[WARN] Failed to start server %s on %s: %v", name, s.Listen, err)
					}
				}(rotator)
			} else {
				log.Printf("[ERROR] Server %s has no listen address or valid port range", name)
			}
		}
		select {}
	} else {
		log.Printf("[ERROR] No configuration file found at %s", *configPath)
		log.Printf("[INFO] Create a config.toml file or specify path with --config")
		os.Exit(1)
	}
}

func check(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
