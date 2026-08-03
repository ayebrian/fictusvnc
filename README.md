# FictusVNC Server


![FictusVNC](banner.png)

A minimal VNC server that serves static images with advanced rotation capabilities.

---

## July 8, 2025 Update
Shodan shadowbanned VNC services from their image feed (https://images.shodan.io/) and added official product recognition for FictusVNC: https://www.shodan.io/search?query=product:"FictusVNC"

UPDATE: Disable branding and don't mention FictusVNC in server names if you want to avoid being flagged.

Note: This affected ALL VNC services, not just FictusVNC.
Interestingly, it's now being classified as a honeypot - took them long enough to notice.

## ⚙️ Features

- 🖼 Serve static JPG & PNG as framebuffer
- 🖥 Supports RealVNC / UltraVNC / TightVNC clients
- 🗜 **ZRLE encoding** (with Raw fallback) — far less bandwidth per connection
- 🛠 Unified configuration via `config.toml`
- 📶 Multi-instance support (multiple ports/images)
- 🎲 **Image rotation with weighted random selection**
- 📋 **Sequential rotation mode**
- 🏷️ **Named server configurations**
- 💾 Cross-platform: Linux, Windows, macOS, ARM64
- 📉 Lightweight: ~3MB binary

---

## 🚀 Quick Start

- [⚙️ Run with config (`config.toml`)](#run-with-config)
- [🗂 Preview](#preview)

---

### ⚙️ Run with config

Create `config.toml`:

```toml
[global]
name = "FictusVNC"
branding = true
show_client_ip = false

# Simple single-image setup (no rotation)
[server.desktop]
listen = ":5900"
image = "default.png"
name = "My Desktop"

# Example with rotation
[server.office]
listen = "127.0.0.1:5901"
name = "Office Computer"
rotation_mode = "random"
images = [
  {path = "desktop_work.png", weight = 70},
  {path = "desktop_idle.png", weight = 25},
  {path = "desktop_error.png", weight = 5}
]
```

Then run:

```bash
./fictusvnc-linux-amd64 --config config.toml
```

---

### 🗂 Preview

![FictusVNC](vncwindow.png)

---

## Available Flags

| Flag              | Description                                      | Default Value     |
| ----------------- | ------------------------------------------------ | ----------------- |
| `--config`        | Path to TOML configuration file                  | `./config.toml`  |
| `--version`, `-v` | Show version and exit                            | `false`           |

**Note:** Starting with v2.0.0, all other options (`--name`, `--no-brand`, `--show-ip`) have been moved to the configuration file under the `[global]` section.

---

## Example Run with Flags

```bash
go run . --config config.toml
```

---

## Configuration

### Basic Configuration

```toml
# Minimal setup - single image, no global options
[server.my_server]
listen = "0.0.0.0:5900"
name = "Test Server"
image = "desktop.png"
```

### Configuration with Global Options

```toml
[global]
name = "My VNC Server"
show_client_ip = true

[server.my_server]
listen = "0.0.0.0:5900"
name = "Test Server"
image = "desktop.png"
```

### Advanced Configuration with Image Rotation

```toml
[global]
name = "FictusVNC"
branding = true
show_client_ip = true

# Server with rotation enabled
[server.random_rotation]
listen = "0.0.0.0:5900"
name = "Random Server"
rotation_mode = "random"
images = [
  {path = "normal_desktop.png", weight = 60},
  {path = "busy_desktop.png", weight = 30},
  {path = "error_screen.png", weight = 10}
]

# Server with sequential rotation
[server.sequential_boot]
listen = "0.0.0.0:5901"
name = "Boot Sequence"
rotation_mode = "sequential"
images = [
  {path = "boot_bios.png"},
  {path = "boot_loading.png"},
  {path = "login_screen.png"},
  {path = "desktop_ready.png"}
]

# Simple server without rotation (default mode)
[server.static]
listen = "0.0.0.0:5902"
name = "Static Server"
image = "desktop.png"
```

### Port Ranges

```toml
[server.multi_port]
listen = "0.0.0.0"
start_port = 5900
end_port = 5905
name = "Multi-Port Server"
image = "desktop.png"
```

## Rotation Modes

**Image rotation is DISABLED by default.** FictusVNC uses simple single-image mode unless you explicitly configure rotation.

- **Default behavior:** Use `image = "path.png"` for single static image
- **Optional rotation:** Use `images = [...]` array to enable rotation with `rotation_mode`

| Mode         | Description                                               | Example Use Case                    |
| ------------ | --------------------------------------------------------- | ----------------------------------- |
| `random`     | Weighted random selection based on image weights          | Honeypot with realistic error rates |
| `sequential` | Images shown in order, cycling through on each connection | Boot sequence simulation            |

### Random Mode Details

- **Only active when using `images` array**
- Each image can have a `weight` parameter (**default: 10 if not specified**)
- Higher weights make images more likely to be selected
- **Example:** weights 50, 30, 20 = 50%, 30%, 20% probability
- **Example:** no weights specified = equal distribution (every image gets the same default weight)
- Perfect for simulating realistic desktop scenarios with varying frequencies

### Sequential Mode Details

- **Only active when using `images` array**
- Images cycle in the order defined in configuration
- Each new VNC connection gets the next image in sequence
- Ideal for simulating boot processes or step-by-step scenarios

## Encodings

FictusVNC negotiates the client's preferred encoding automatically — no
configuration required.

- **ZRLE** (`encoding 16`) is used when the client advertises it. The
  framebuffer is split into 64×64 tiles and each tile is sent with the
  cheapest ZRLE subencoding (solid, packed palette, palette/plain RLE or raw),
  then the whole stream is zlib-compressed over a single per-connection
  stream. This cuts per-connection bandwidth dramatically — a typical static
  desktop drops from hundreds of KB (Raw) to a few KB.
- **Raw** (`encoding 0`) is the automatic fallback for clients that don't
  support ZRLE or that negotiate an unusual pixel format.

Standard `KeyEvent` / `PointerEvent` / `ClientCutText` messages are accepted
and discarded (the server is view-only — it never changes the displayed
image based on client input).

## Configuration Options

### Global Section
| Parameter  | Type    | Description                                       | Default       |
| ---------- | ------- | ------------------------------------------------- | ------------- |
| `name`     | string  | Brand prefix used when `branding` is enabled      | `"FictusVNC"` |
| `branding` | boolean | Prefix server names with the global `name`        | `true`        |
| `show_client_ip`  | boolean | Display client IP address on images               | `false`       |

### Server Section

| Parameter       | Type   | Description                                             | Default         |
| --------------- | ------ | ------------------------------------------------------- | --------------- |
| `listen`        | string | Listen address and port                                 | Required        |
| `name`   | string | Display name for the server                             | Server key name |
| `image`         | string | **Single image path (default mode)**                    | -               |
| `images`        | array  | **Array of images for rotation (optional)**             | -               |
| `rotation_mode` | string | `"random"` or `"sequential"` (ignored if using `image`) | `"random"`      |
| `start_port`    | int    | Start of port range                                     | -               |
| `end_port`      | int    | End of port range                                       | -               |

Image paths are resolved relative to the directory holding the config file
(inside its `images/` subdirectory), so the server behaves identically whether
it is started from a shell or by systemd. Absolute paths are used as-is.

### Renamed Keys (2.0 → 2.1)

The old spellings still work and log a deprecation warning, so existing configs
keep running unchanged.

| Old key       | New key          | Note                        |
| ------------- | ---------------- | --------------------------- |
| `show_ip`     | `show_client_ip` | Same meaning                |
| `no_brand`    | `branding`       | Inverted: `no_brand = true` becomes `branding = false` |
| `server_name` | `name`           | Matches the global `name` key |

### Image Object Structure

```toml
# DEFAULT MODE: Single static image (no rotation)
image = "desktop.png"

# OPTIONAL ROTATION MODE: Use images array
images = [
  # For weighted random rotation - specify weights
  {path = "normal_desktop.png", weight = 50},
  {path = "busy_desktop.png", weight = 30},
  {path = "error_screen.png", weight = 20}
]

# OR for equal weight distribution (weight = 1 by default)
images = [
  {path = "desktop1.png"},
  {path = "desktop2.png"},
  {path = "desktop3.png"}
]

# OR for sequential rotation (weights ignored anyway)
images = [
  {path = "boot_bios.png"},
  {path = "boot_loading.png"},
  {path = "desktop_ready.png"}
]
```

## Breaking Changes in v2.0.0

**Old format (v1.x):**

```toml
[[server]]
listen = ":5900"
image = "desktop.png"
```

**New format (v2.0.0+):**

```toml
[server.desktop]
listen = ":5900"
image = "desktop.png"
```

**Migration Guide:**

1. Replace `[[server]]` with `[server.any_name]` where `any_name` is your chosen server identifier
2. Rename configuration file from `servers.toml` to `config.toml`
3. **Move CLI flags to config file:** Add `[global]` section and move `--name`, `--no-brand`, `--show-ip` options there
4. Update command line: only `--config` and `--version` flags are supported now

**Example migration:**

**Old v1.x command:**

```bash
./fictusvnc --servers servers.toml --show-ip --no-brand
```

**New v2.0.0:**

```bash
./fictusvnc --config config.toml
```

With `config.toml`:

```toml
[global]
show_client_ip = true
branding = false

[server.main]
listen = ":5900"
image = "desktop.png"
```

## Building from Source

Single binary for the current platform:

```bash
go build -o fictusvnc .
```

All release targets (Linux / Windows / macOS, amd64 / arm64 / 386) into `build/`:

```bash
./build.sh
```

---

## License

This project is licensed under the terms specified in the [LICENSE](LICENSE) file.
