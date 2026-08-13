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
	cfg, _, err := loadConfig(writeConfig(t, `
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
	cfg, _, err := loadConfig(writeConfig(t, `
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
	cfg, _, err := loadConfig(writeConfig(t, `
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
	cfg, _, err := loadConfig(writeConfig(t, `
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

// An absent max_connections must fall back to the safe default, while an
// explicit 0 is a deliberate "unlimited" and has to survive.
func TestConfigMaxConnections(t *testing.T) {
	tests := []struct {
		name string
		body string
		want int
	}{
		{"absent uses the default", "[server.a]\nlisten = \":5900\"\n", defaultMaxConnections},
		{"explicit zero means unlimited", "[global]\nmax_connections = 0\n", 0},
		{"explicit value is kept", "[global]\nmax_connections = 32\n", 32},
		{"negative is clamped to unlimited", "[global]\nmax_connections = -1\n", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, _, err := loadConfig(writeConfig(t, tt.body))
			if err != nil {
				t.Fatalf("loadConfig: %v", err)
			}
			if cfg.Global.MaxConnections != tt.want {
				t.Errorf("max_connections: got %d want %d", cfg.Global.MaxConnections, tt.want)
			}
		})
	}
}

// Banner flags are set globally and overridden per server. The interesting
// case is an explicit false switching a globally enabled line back off — the
// reason the server fields are pointers.
func TestOverlayPerServerOverrides(t *testing.T) {
	cfg, _, err := loadConfig(writeConfig(t, `
[global]
show_client_ip = true
show_time = true

# Inherits everything from [global].
[server.inherits]
listen = ":5900"
image = "d.png"

# Turns the globally enabled lines back off.
[server.opts_out]
listen = ":5901"
image = "d.png"
show_client_ip = false
show_time = false

# Adds a line [global] leaves off, keeps the inherited ones.
[server.adds_rdns]
listen = ":5902"
image = "d.png"
show_rdns = true
`))
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}

	tests := []struct {
		server           string
		wantIP, wantRDNS bool
		wantTime         bool
	}{
		{"inherits", true, false, true},
		{"opts_out", false, false, false},
		{"adds_rdns", true, true, true},
	}
	for _, tt := range tests {
		t.Run(tt.server, func(t *testing.T) {
			got := cfg.Global.overlayFor(cfg.Server[tt.server])
			if got.showIP != tt.wantIP {
				t.Errorf("showIP: got %v want %v", got.showIP, tt.wantIP)
			}
			if got.showRDNS != tt.wantRDNS {
				t.Errorf("showRDNS: got %v want %v", got.showRDNS, tt.wantRDNS)
			}
			if got.showTime != tt.wantTime {
				t.Errorf("showTime: got %v want %v", got.showTime, tt.wantTime)
			}
		})
	}
}

// With no [global] section at all, a server can still enable a line on its own.
func TestOverlayServerOnlyWithoutGlobal(t *testing.T) {
	cfg, _, err := loadConfig(writeConfig(t, `
[server.a]
listen = ":5900"
image = "d.png"
show_client_ip = true
`))
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	got := cfg.Global.overlayFor(cfg.Server["a"])
	if !got.showIP {
		t.Error("a server-level show_client_ip must work without a [global] section")
	}
	if got.showRDNS || got.showTime {
		t.Error("unset lines must stay off")
	}
}

// A port range outside 1..65535, or an inverted one, must warn rather than be
// silently accepted (and then silently fail to bind, or drive a huge alloc).
func TestPortRangeValidationWarns(t *testing.T) {
	t.Run("above max warns", func(t *testing.T) {
		_, warnings, err := loadConfig(writeConfig(t, `
[server.a]
listen = ":5900"
image = "d.png"
start_port = 5900
end_port = 70000
`))
		if err != nil {
			t.Fatalf("loadConfig: %v", err)
		}
		if !hasWarning(warnings, "exceeds the maximum port") {
			t.Errorf("expected an out-of-range port warning; got %v", warnings)
		}
	})

	t.Run("inverted range warns", func(t *testing.T) {
		_, warnings, err := loadConfig(writeConfig(t, `
[server.a]
listen = ":5900"
image = "d.png"
start_port = 5912
end_port = 5910
`))
		if err != nil {
			t.Fatalf("loadConfig: %v", err)
		}
		if !hasWarning(warnings, "below start_port") {
			t.Errorf("expected an inverted-range warning; got %v", warnings)
		}
	})

	t.Run("valid range does not warn about bounds", func(t *testing.T) {
		_, warnings, err := loadConfig(writeConfig(t, `
[server.a]
listen = "0.0.0.0"
image = "d.png"
start_port = 5900
end_port = 5902
`))
		if err != nil {
			t.Fatalf("loadConfig: %v", err)
		}
		if hasWarning(warnings, "exceeds the maximum port") || hasWarning(warnings, "below start_port") {
			t.Errorf("a valid range must not warn about bounds; got %v", warnings)
		}
	})
}

func TestBoolOr(t *testing.T) {
	tr, fa := true, false
	if got := boolOr(nil, true); !got {
		t.Error("nil override must fall back to the global value")
	}
	if got := boolOr(nil, false); got {
		t.Error("nil override must fall back to the global value")
	}
	if got := boolOr(&fa, true); got {
		t.Error("an explicit false must beat a global true")
	}
	if got := boolOr(&tr, false); !got {
		t.Error("an explicit true must beat a global false")
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
		{
			name: "range at the max boundary is honoured",
			cfg:  ServerConfig{Listen: "0.0.0.0", StartPort: 65534, EndPort: 65535},
			want: []string{"0.0.0.0:65534", "0.0.0.0:65535"},
		},
		{
			name: "out-of-range end_port is not expanded",
			cfg:  ServerConfig{Listen: "0.0.0.0:5900", StartPort: 65530, EndPort: 70000},
			want: []string{"0.0.0.0:5900"},
		},
		{
			name: "an absurd end_port never allocates a giant slice",
			cfg:  ServerConfig{Listen: "0.0.0.0:5900", StartPort: 5900, EndPort: 2000000000},
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
