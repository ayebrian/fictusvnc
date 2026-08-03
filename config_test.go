package main

import (
	"os"
	"path/filepath"
	"testing"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func TestConfigCurrentKeys(t *testing.T) {
	cfg, err := loadConfig(writeConfig(t, `
[global]
name = "Acme"
branding = false
show_client_ip = true

[server.a]
listen = ":5900"
name = "Front Desk"
image = "default.png"
`))
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.Global.Name != "Acme" {
		t.Errorf("name: got %q want %q", cfg.Global.Name, "Acme")
	}
	if cfg.Global.Branding {
		t.Error("branding: got true want false")
	}
	if !cfg.Global.ShowClientIP {
		t.Error("show_client_ip: got false want true")
	}
	if got := cfg.Server["a"].Name; got != "Front Desk" {
		t.Errorf("server name: got %q want %q", got, "Front Desk")
	}
}

// The 2.0 spellings must keep working: no_brand is the inverse of branding,
// show_ip maps to show_client_ip and server_name to name.
func TestConfigDeprecatedKeys(t *testing.T) {
	cfg, err := loadConfig(writeConfig(t, `
[global]
no_brand = true
show_ip = true

[server.a]
listen = ":5900"
server_name = "Legacy"
image = "default.png"
`))
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.Global.Branding {
		t.Error("no_brand = true should yield branding = false")
	}
	if !cfg.Global.ShowClientIP {
		t.Error("show_ip = true should yield show_client_ip = true")
	}
	if got := cfg.Server["a"].Name; got != "Legacy" {
		t.Errorf("server_name should populate name: got %q", got)
	}
}

// Branding is opt-out, so a config that mentions neither key gets it enabled.
func TestConfigBrandingDefaultsOn(t *testing.T) {
	cfg, err := loadConfig(writeConfig(t, `
[server.a]
listen = ":5900"
image = "default.png"
`))
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if !cfg.Global.Branding {
		t.Error("branding should default to true")
	}
	if cfg.Global.Name != "FictusVNC" {
		t.Errorf("global name default: got %q", cfg.Global.Name)
	}
}

// The new key wins when a config carries both spellings.
func TestConfigNewKeyBeatsDeprecated(t *testing.T) {
	cfg, err := loadConfig(writeConfig(t, `
[global]
branding = true
no_brand = true
`))
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if !cfg.Global.Branding {
		t.Error("explicit branding = true should win over no_brand")
	}
}

func TestListenAddrs(t *testing.T) {
	tests := []struct {
		name string
		cfg  ServerConfig
		want []string
	}{
		{
			name: "single address",
			cfg:  ServerConfig{Listen: "0.0.0.0:5900"},
			want: []string{"0.0.0.0:5900"},
		},
		{
			name: "port range on bare host",
			cfg:  ServerConfig{Listen: "0.0.0.0", StartPort: 5910, EndPort: 5912},
			want: []string{"0.0.0.0:5910", "0.0.0.0:5911", "0.0.0.0:5912"},
		},
		{
			name: "port range strips the port already in listen",
			cfg:  ServerConfig{Listen: "127.0.0.1:1", StartPort: 5900, EndPort: 5901},
			want: []string{"127.0.0.1:5900", "127.0.0.1:5901"},
		},
		{
			name: "port range on bare IPv6 literal",
			cfg:  ServerConfig{Listen: "::1", StartPort: 5900, EndPort: 5900},
			want: []string{"[::1]:5900"},
		},
		{
			name: "port range without listen binds all interfaces",
			cfg:  ServerConfig{StartPort: 5900, EndPort: 5900},
			want: []string{":5900"},
		},
		{
			name: "nothing configured",
			cfg:  ServerConfig{},
			want: nil,
		},
		{
			name: "inverted range is not a range",
			cfg:  ServerConfig{Listen: "0.0.0.0:5900", StartPort: 5910, EndPort: 5900},
			want: []string{"0.0.0.0:5900"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := listenAddrs(tt.cfg)
			if len(got) != len(tt.want) {
				t.Fatalf("got %v want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("got %v want %v", got, tt.want)
				}
			}
		})
	}
}
