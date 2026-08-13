package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// hasWarning reports whether any warning contains all the given fragments.
func hasWarning(warnings []string, fragments ...string) bool {
	for _, w := range warnings {
		all := true
		for _, f := range fragments {
			if !strings.Contains(w, f) {
				all = false
				break
			}
		}
		if all {
			return true
		}
	}
	return false
}

// A key that matches no field is almost always a typo, and silently dropping
// it makes the option look broken. Every nesting level must be reported.
func TestUnknownKeysWarn(t *testing.T) {
	_, warnings, err := loadConfig(writeConfig(t, `
[global]
show_clientip = true

[logging]
levl = "debug"

[servers.oops]
listen = ":5900"

[server.a]
listen = ":5901"
imag = "d.png"
images = [{path = "d.png", wieght = 5}]
`))
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}

	for _, key := range []string{
		"global.show_clientip",
		"logging.levl",
		"servers.oops",
		"server.a.imag",
		"server.a.images.wieght",
	} {
		if !hasWarning(warnings, "unknown key", key) {
			t.Errorf("no warning for unknown key %q; got %v", key, warnings)
		}
	}
}

// Deprecated keys are real fields, so they must not be mistaken for typos.
func TestDeprecatedKeysAreNotReportedAsUnknown(t *testing.T) {
	_, warnings, err := loadConfig(writeConfig(t, `
[global]
no_brand = true
show_ip = true

[server.a]
listen = ":5900"
server_name = "Legacy"
image = "d.png"
`))
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if hasWarning(warnings, "unknown key") {
		t.Errorf("deprecated keys must not be reported as unknown; got %v", warnings)
	}
	// They should still produce their own deprecation notices.
	if !hasWarning(warnings, "no_brand") || !hasWarning(warnings, "show_ip") || !hasWarning(warnings, "server_name") {
		t.Errorf("expected deprecation warnings; got %v", warnings)
	}
}

func TestRotationModeWarning(t *testing.T) {
	for _, mode := range []string{"random", "sequential", ""} {
		body := "[server.a]\nlisten = \":5900\"\nimage = \"d.png\"\n"
		if mode != "" {
			body += "rotation_mode = \"" + mode + "\"\n"
		}
		_, warnings, err := loadConfig(writeConfig(t, body))
		if err != nil {
			t.Fatalf("loadConfig: %v", err)
		}
		if hasWarning(warnings, "rotation_mode") {
			t.Errorf("mode %q is valid but warned: %v", mode, warnings)
		}
	}

	_, warnings, err := loadConfig(writeConfig(t, `
[server.a]
listen = ":5900"
image = "d.png"
rotation_mode = "randam"
`))
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if !hasWarning(warnings, "rotation_mode", "randam") {
		t.Errorf("a misspelled rotation_mode must warn; got %v", warnings)
	}
}

// Settings that silently override one another have to say so.
func TestConflictWarnings(t *testing.T) {
	_, warnings, err := loadConfig(writeConfig(t, `
[server.both_images]
listen = ":5900"
image = "single.png"
images = [{path = "a.png"}]

[server.ranged]
listen = "0.0.0.0:9999"
start_port = 5910
end_port = 5912
image = "d.png"
`))
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if !hasWarning(warnings, "both_images", "'images' wins") {
		t.Errorf("expected an image/images conflict warning; got %v", warnings)
	}
	if !hasWarning(warnings, "ranged", "9999") {
		t.Errorf("expected a port-range/listen conflict warning; got %v", warnings)
	}
}

// A listen address without a port cannot conflict with a range.
func TestNoPortConflictWarningForBareHost(t *testing.T) {
	_, warnings, err := loadConfig(writeConfig(t, `
[server.ranged]
listen = "0.0.0.0"
start_port = 5910
end_port = 5912
image = "d.png"
`))
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if hasWarning(warnings, "is ignored") {
		t.Errorf("a bare host with a range must not warn; got %v", warnings)
	}
}

// Warnings are collected while iterating a map, so their order has to be
// pinned or the output would shuffle between runs.
func TestWarningOrderIsStable(t *testing.T) {
	body := `
[server.zeta]
listen = ":5900"
image = "d.png"
rotation_mode = "bogus"

[server.alpha]
listen = ":5901"
image = "d.png"
rotation_mode = "bogus"

[server.mid]
listen = ":5902"
image = "d.png"
rotation_mode = "bogus"
`
	var first []string
	for i := range 8 {
		_, warnings, err := loadConfig(writeConfig(t, body))
		if err != nil {
			t.Fatalf("loadConfig: %v", err)
		}
		if i == 0 {
			first = warnings
			continue
		}
		if len(warnings) != len(first) {
			t.Fatalf("warning count changed between runs: %d vs %d", len(warnings), len(first))
		}
		for j := range warnings {
			if warnings[j] != first[j] {
				t.Fatalf("warning order changed between runs:\n %q\n %q", first[j], warnings[j])
			}
		}
	}
	// Sorted by server id, so alpha precedes mid precedes zeta.
	if len(first) != 3 || !strings.Contains(first[0], "alpha") || !strings.Contains(first[2], "zeta") {
		t.Errorf("warnings are not sorted by server id: %v", first)
	}
}

// checkFixture writes a config plus a real image so NewImageRotator succeeds.
func checkFixture(t *testing.T, body string) (Config, []string, string, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, defaultImageDir), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	src, err := os.ReadFile("images/default.png")
	if err != nil {
		t.Fatalf("read fixture image: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, defaultImageDir, "d.png"), src, 0o644); err != nil {
		t.Fatalf("write fixture image: %v", err)
	}
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, warnings, err := loadConfig(path)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	return cfg, warnings, dir, path
}

func TestCheckAcceptsGoodConfig(t *testing.T) {
	cfg, warnings, dir, path := checkFixture(t, `
[global]
show_client_ip = true

[server.a]
listen = "127.0.0.1:5900"
image = "d.png"

[server.b]
listen = "127.0.0.1:5901"
image = "d.png"
show_client_ip = false
`)
	var buf bytes.Buffer
	if code := runCheck(&buf, cfg, warnings, dir, path); code != 0 {
		t.Fatalf("exit code: got %d want 0\n%s", code, buf.String())
	}
	out := buf.String()
	for _, want := range []string{"config OK", "listeners: 2", "banner: client ip", "banner: off"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

// The check must catch what would otherwise only fail at bind time.
func TestCheckDetectsDuplicateListenAddress(t *testing.T) {
	cfg, warnings, dir, path := checkFixture(t, `
[server.a]
listen = "127.0.0.1:5900"
image = "d.png"

[server.b]
listen = "127.0.0.1:5900"
image = "d.png"
`)
	var buf bytes.Buffer
	if code := runCheck(&buf, cfg, warnings, dir, path); code != 1 {
		t.Fatalf("exit code: got %d want 1\n%s", code, buf.String())
	}
	if !strings.Contains(buf.String(), "already used by server") {
		t.Errorf("expected a duplicate-address error:\n%s", buf.String())
	}
}

func TestCheckDetectsMissingImage(t *testing.T) {
	cfg, warnings, dir, path := checkFixture(t, `
[server.a]
listen = "127.0.0.1:5900"
image = "absent.png"
`)
	var buf bytes.Buffer
	if code := runCheck(&buf, cfg, warnings, dir, path); code != 1 {
		t.Fatalf("exit code: got %d want 1\n%s", code, buf.String())
	}
	if !strings.Contains(buf.String(), "NOT usable") {
		t.Errorf("expected a failure verdict:\n%s", buf.String())
	}
}

func TestCheckReportsEmptyConfig(t *testing.T) {
	cfg, warnings, dir, path := checkFixture(t, "[global]\nname = \"x\"\n")
	var buf bytes.Buffer
	if code := runCheck(&buf, cfg, warnings, dir, path); code != 1 {
		t.Fatalf("exit code: got %d want 1\n%s", code, buf.String())
	}
	if !strings.Contains(buf.String(), "no [server.*] sections") {
		t.Errorf("expected an empty-config error:\n%s", buf.String())
	}
}

// Warnings are surfaced but must not by themselves make the config invalid.
func TestCheckPassesWithWarnings(t *testing.T) {
	cfg, warnings, dir, path := checkFixture(t, `
[global]
show_clientip = true

[server.a]
listen = "127.0.0.1:5900"
image = "d.png"
`)
	var buf bytes.Buffer
	if code := runCheck(&buf, cfg, warnings, dir, path); code != 0 {
		t.Fatalf("warnings must not fail the check: got %d\n%s", code, buf.String())
	}
	out := buf.String()
	if !strings.Contains(out, "warning: unknown key") || !strings.Contains(out, "1 warning(s)") {
		t.Errorf("warnings not surfaced:\n%s", out)
	}
}

// --check must reject a logging config that setupLogging would refuse at
// startup, instead of green-lighting a server that then fails to boot.
func TestCheckRejectsBadLogLevel(t *testing.T) {
	cfg, warnings, dir, path := checkFixture(t, `
[logging]
level = "verbose"

[server.a]
listen = "127.0.0.1:5900"
image = "d.png"
`)
	var buf bytes.Buffer
	if code := runCheck(&buf, cfg, warnings, dir, path); code != 1 {
		t.Fatalf("exit code: got %d want 1\n%s", code, buf.String())
	}
	if !strings.Contains(buf.String(), "unknown log level") {
		t.Errorf("expected an invalid-log-level error:\n%s", buf.String())
	}
}

func TestCheckRejectsBadLogFormat(t *testing.T) {
	cfg, warnings, dir, path := checkFixture(t, `
[logging]
format = "xml"

[server.a]
listen = "127.0.0.1:5900"
image = "d.png"
`)
	var buf bytes.Buffer
	if code := runCheck(&buf, cfg, warnings, dir, path); code != 1 {
		t.Fatalf("exit code: got %d want 1\n%s", code, buf.String())
	}
	if !strings.Contains(buf.String(), "unknown log format") {
		t.Errorf("expected an invalid-log-format error:\n%s", buf.String())
	}
}

// A valid, non-default logging section must still pass cleanly.
func TestCheckAcceptsValidLogging(t *testing.T) {
	cfg, warnings, dir, path := checkFixture(t, `
[logging]
level = "debug"
format = "text"

[server.a]
listen = "127.0.0.1:5900"
image = "d.png"
`)
	var buf bytes.Buffer
	if code := runCheck(&buf, cfg, warnings, dir, path); code != 0 {
		t.Fatalf("valid logging must pass: got %d\n%s", code, buf.String())
	}
}

func TestDescribeOverlay(t *testing.T) {
	tests := []struct {
		cfg  overlayConfig
		want string
	}{
		{overlayConfig{}, "off"},
		{overlayConfig{showIP: true}, "client ip"},
		{overlayConfig{showIP: true, showRDNS: true, showTime: true}, "client ip, rdns, time"},
		{overlayConfig{showTime: true}, "time"},
	}
	for _, tt := range tests {
		if got := describeOverlay(tt.cfg); got != tt.want {
			t.Errorf("describeOverlay(%+v): got %q want %q", tt.cfg, got, tt.want)
		}
	}
}
