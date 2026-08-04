package main

import (
	"fmt"

	"github.com/BurntSushi/toml"
)

// appVersion can be overridden at build time with
// -ldflags "-X main.appVersion=...". build.sh injects a git-derived version in
// CI (e.g. 2.1.0-dev.gabc1234); a plain build reports the baseline below.
var appVersion = "2.1.0"

const (
	rfbVersion              = "RFB 003.008\n"
	msgSetPixelFormat       = 0
	msgSetEncodings         = 2
	msgFramebufferUpdateReq = 3
	msgEnableCU             = 150
	msgKeyEvent             = 4
	msgPointerEvent         = 5
	msgClientCutText        = 6
	defaultImageDir         = "images"

	encRaw  = 0
	encZRLE = 16

	zrleTileSize = 64

	// secTypeNone is the only security type this server offers.
	secTypeNone = 1

	// defaultMaxConnections bounds concurrent clients. Each connection can
	// hold a private framebuffer copy when the info banner is on, so an
	// unbounded server is trivially exhausted by a flood.
	defaultMaxConnections = 512
)

type Config struct {
	Global  GlobalConfig            `toml:"global"`
	Logging LoggingConfig           `toml:"logging"`
	Server  map[string]ServerConfig `toml:"server"`
}

type GlobalConfig struct {
	Name         string `toml:"name"`
	Branding     bool   `toml:"branding"`
	ShowClientIP bool   `toml:"show_client_ip"`
	ShowRDNS     bool   `toml:"show_rdns"`
	ShowTime     bool   `toml:"show_time"`

	// MaxConnections caps concurrent clients across every listener. 0 means
	// unlimited; when the key is absent it defaults to defaultMaxConnections.
	MaxConnections int `toml:"max_connections"`

	// Deprecated 2.0 spellings, still accepted so existing configs keep
	// working. Branding is the inverse of NoBrand.
	NoBrand bool `toml:"no_brand"`
	ShowIP  bool `toml:"show_ip"`
}

// overlay returns the banner settings implied by the global config.
func (g GlobalConfig) overlay() overlayConfig {
	return overlayConfig{
		showIP:   g.ShowClientIP,
		showRDNS: g.ShowRDNS,
		showTime: g.ShowTime,
	}
}

type WeightedImage struct {
	Path   string `toml:"path"`
	Weight int    `toml:"weight"`
}

type ServerConfig struct {
	Listen       string          `toml:"listen"`
	Name         string          `toml:"name"`
	Image        string          `toml:"image"`
	Images       []WeightedImage `toml:"images"`
	StartPort    int             `toml:"start_port"`
	EndPort      int             `toml:"end_port"`
	RotationMode string          `toml:"rotation_mode"` // "random", "sequential"

	// Deprecated 2.0 spelling of Name.
	ServerName string `toml:"server_name"`
}

// loadConfig decodes the TOML file and folds any deprecated keys into their
// current equivalents. Warnings are returned rather than logged, because the
// logger is not configured until this config has been read.
func loadConfig(path string) (Config, []string, error) {
	var cfg Config
	var warnings []string

	md, err := toml.DecodeFile(path, &cfg)
	if err != nil {
		return cfg, nil, err
	}
	warn := func(format string, args ...any) {
		warnings = append(warnings, fmt.Sprintf(format, args...))
	}

	// branding replaces no_brand and has the opposite polarity, so it only
	// defaults to true when neither key is present.
	switch {
	case md.IsDefined("global", "branding"):
		if md.IsDefined("global", "no_brand") {
			warn("both 'branding' and deprecated 'no_brand' are set, using 'branding'")
		}
	case md.IsDefined("global", "no_brand"):
		warn("'no_brand' is deprecated, use 'branding = %t'", !cfg.Global.NoBrand)
		cfg.Global.Branding = !cfg.Global.NoBrand
	default:
		cfg.Global.Branding = true
	}

	if !md.IsDefined("global", "show_client_ip") && md.IsDefined("global", "show_ip") {
		warn("'show_ip' is deprecated, use 'show_client_ip'")
		cfg.Global.ShowClientIP = cfg.Global.ShowIP
	}

	for id, s := range cfg.Server {
		if s.Name == "" && s.ServerName != "" {
			warn("server %q uses deprecated 'server_name', use 'name'", id)
			s.Name = s.ServerName
			cfg.Server[id] = s
		}
	}

	// An absent key gets the safe default; an explicit 0 means "unlimited"
	// and must survive.
	if !md.IsDefined("global", "max_connections") {
		cfg.Global.MaxConnections = defaultMaxConnections
	} else if cfg.Global.MaxConnections < 0 {
		warn("'max_connections' cannot be negative, treating %d as unlimited", cfg.Global.MaxConnections)
		cfg.Global.MaxConnections = 0
	}

	if cfg.Global.Name == "" {
		cfg.Global.Name = "FictusVNC"
	}
	return cfg, warnings, nil
}
