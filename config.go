package main

const (
	appVersion              = "2.0.0"
	rfbVersion              = "RFB 003.008\n"
	msgSetPixelFormat       = 0
	msgSetEncodings         = 2
	msgFramebufferUpdateReq = 3
	msgEnableCU             = 150
	defaultImageDir         = "images"
)

type Config struct {
	Global GlobalConfig            `toml:"global"`
	Server map[string]ServerConfig `toml:"server"`
}

type GlobalConfig struct {
	Name    string `toml:"name"`
	NoBrand bool   `toml:"no_brand"`
	ShowIP  bool   `toml:"show_ip"`
}

type WeightedImage struct {
	Path   string `toml:"path"`
	Weight int    `toml:"weight"`
}

type ServerConfig struct {
	Listen       string          `toml:"listen"`
	Image        string          `toml:"image"`
	Images       []WeightedImage `toml:"images"`
	Name         string          `toml:"server_name"`
	StartPort    int             `toml:"start_port"`
	EndPort      int             `toml:"end_port"`
	RotationMode string          `toml:"rotation_mode"` // "random", "sequential"
}
