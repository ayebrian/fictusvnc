package main

import (
	"log"

	"github.com/BurntSushi/toml"
)

const (
	appVersion              = "2.1.0"
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
)

type Config struct {
	Global GlobalConfig            `toml:"global"`
	Server map[string]ServerConfig `toml:"server"`
}

type GlobalConfig struct {
	Name         string `toml:"name"`
	Branding     bool   `toml:"branding"`
	ShowClientIP bool   `toml:"show_client_ip"`

	// Deprecated 2.0 spellings, still accepted so existing configs keep
	// working. Branding is the inverse of NoBrand.
	NoBrand bool `toml:"no_brand"`
	ShowIP  bool `toml:"show_ip"`
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
// current equivalents, warning once per key so old configs keep working.
func loadConfig(path string) (Config, error) {
	var cfg Config
	md, err := toml.DecodeFile(path, &cfg)
	if err != nil {
		return cfg, err
	}

	// branding replaces no_brand and has the opposite polarity, so it only
	// defaults to true when neither key is present.
	switch {
	case md.IsDefined("global", "branding"):
		if md.IsDefined("global", "no_brand") {
			log.Printf("[WARN] config: both 'branding' and deprecated 'no_brand' set — using 'branding'")
		}
	case md.IsDefined("global", "no_brand"):
		log.Printf("[WARN] config: 'no_brand' is deprecated, use 'branding = %t'", !cfg.Global.NoBrand)
		cfg.Global.Branding = !cfg.Global.NoBrand
	default:
		cfg.Global.Branding = true
	}

	if !md.IsDefined("global", "show_client_ip") && md.IsDefined("global", "show_ip") {
		log.Printf("[WARN] config: 'show_ip' is deprecated, use 'show_client_ip'")
		cfg.Global.ShowClientIP = cfg.Global.ShowIP
	}

	for id, s := range cfg.Server {
		if s.Name == "" && s.ServerName != "" {
			log.Printf("[WARN] config: server %q uses deprecated 'server_name', use 'name'", id)
			s.Name = s.ServerName
			cfg.Server[id] = s
		}
	}

	if cfg.Global.Name == "" {
		cfg.Global.Name = "FictusVNC"
	}
	return cfg, nil
}
