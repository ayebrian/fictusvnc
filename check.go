package main

import (
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
)

// runCheck validates a config without binding a single port and prints a
// summary of what the server would actually do. Checking a live host's config
// otherwise means starting the process and fighting over ports with the
// instance already running.
//
// It returns the process exit code: non-zero when something would stop a
// server from starting. Warnings alone do not fail the check.
func runCheck(w io.Writer, cfg Config, warnings []string, baseDir, configPath string) int {
	fmt.Fprintf(w, "config: %s\n", configPath)

	for _, msg := range warnings {
		fmt.Fprintf(w, "  warning: %s\n", msg)
	}

	if len(cfg.Server) == 0 {
		fmt.Fprintf(w, "  error: no [server.*] sections defined\n")
		fmt.Fprintf(w, "\nconfig is NOT usable\n")
		return 1
	}

	var problems int
	// seen maps a listen address to the server that claimed it first, so a
	// collision is reported here instead of surfacing as a bind failure.
	seen := map[string]string{}
	listeners := 0

	fmt.Fprintf(w, "\nservers:\n")
	for _, id := range slices.Sorted(maps.Keys(cfg.Server)) {
		s := cfg.Server[id]

		name := s.Name
		if name == "" {
			name = id
		}
		if cfg.Global.Branding {
			name = fmt.Sprintf("%s - %s", cfg.Global.Name, name)
		}

		fmt.Fprintf(w, "  [%s] %s\n", id, name)

		addrs := listenAddrs(s)
		switch {
		case len(addrs) == 0:
			fmt.Fprintf(w, "      error: no listen address or valid port range\n")
			problems++
		case len(addrs) == 1:
			fmt.Fprintf(w, "      listen: %s\n", addrs[0])
		default:
			fmt.Fprintf(w, "      listen: %s … %s (%d ports)\n", addrs[0], addrs[len(addrs)-1], len(addrs))
		}
		for _, a := range addrs {
			if other, dup := seen[a]; dup {
				fmt.Fprintf(w, "      error: %s is already used by server %q\n", a, other)
				problems++
				continue
			}
			seen[a] = id
			listeners++
		}

		// Loading really decodes every image, so a corrupt or missing file is
		// caught here rather than at startup.
		rot, err := NewImageRotator(s, baseDir)
		if err != nil {
			fmt.Fprintf(w, "      error: %v\n", err)
			problems++
		} else {
			fmt.Fprintf(w, "      images: %d (%s)\n", len(rot.images), rot.modeName())
			for _, img := range rot.images {
				fmt.Fprintf(w, "        %-28s %4dx%-4d weight %d\n",
					img.path, img.fb.w, img.fb.h, img.weight)
			}
		}

		fmt.Fprintf(w, "      banner: %s\n", describeOverlay(cfg.Global.overlayFor(s)))
	}

	// Logging is otherwise validated only by setupLogging, which runs on a real
	// start, long after --check has exited. Mirror its checks so a config that
	// provably cannot boot is not reported as usable.
	if _, err := parseLevel(cfg.Logging.Level); err != nil {
		fmt.Fprintf(w, "  error: %v\n", err)
		problems++
	}
	switch strings.ToLower(strings.TrimSpace(cfg.Logging.Format)) {
	case "", "json", "text":
	default:
		fmt.Fprintf(w, "  error: unknown log format %q (want json or text)\n", cfg.Logging.Format)
		problems++
	}

	limit := "unlimited"
	if cfg.Global.MaxConnections > 0 {
		limit = fmt.Sprint(cfg.Global.MaxConnections)
	}
	output := cfg.Logging.Output
	if output == "" {
		output = "stdout"
	}
	format := cfg.Logging.Format
	if format == "" {
		format = "json"
	}
	level := cfg.Logging.Level
	if level == "" {
		level = "info"
	}
	fmt.Fprintf(w, "\nlisteners: %d\nmax_connections: %s\nlogging: %s/%s -> %s\n",
		listeners, limit, format, level, output)

	if problems > 0 {
		fmt.Fprintf(w, "\n%d problem(s) found — config is NOT usable\n", problems)
		return 1
	}
	if len(warnings) > 0 {
		fmt.Fprintf(w, "\nconfig is usable, with %d warning(s)\n", len(warnings))
		return 0
	}
	fmt.Fprintf(w, "\nconfig OK\n")
	return 0
}

// describeOverlay renders the resolved banner lines for one server.
func describeOverlay(o overlayConfig) string {
	var on []string
	if o.showIP {
		on = append(on, "client ip")
	}
	if o.showRDNS {
		on = append(on, "rdns")
	}
	if o.showTime {
		on = append(on, "time")
	}
	if len(on) == 0 {
		return "off"
	}
	return strings.Join(on, ", ")
}
