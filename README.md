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
- 🛠 Unified configuration via `config.toml`
- 📶 Multi-instance support (multiple ports/images)
- 🎲 **Image rotation with weighted random selection**
- 📋 **Sequential rotation mode**
- 🏷️ **Named server configurations**
- 💾 Cross-platform: Linux, Windows, macOS, ARM64
- 📉 Lightweight: ~2.8MB binary

## ⚙️ Features

- 🖼 Serve static JPG & PNG as framebuffer
- 🖥 Supports RealVNC / UltraVNC / TightVNC clients
- 🛠 Configurable via `servers.toml`
- 📶 Multi-instance support (multiple ports/images)
- � **Image rotation with weighted random selection**
- ⏱️ **Time-based and sequential rotation modes**
- 🏷️ **Named server configurations**
- �💾 Cross-platform: Linux, Windows, macOS, ARM64
- 📉 Lightweight: ~2.8MB binary

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
no_brand = false
show_ip = false

# Simple single-image setup (no rotation)
[server.desktop]
listen = ":5900"
image = "default.png"
server_name = "My Desktop"

# Example with rotation
[server.office]
listen = "127.0.0.1:5901"
server_name = "Office Computer"
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
server_name = "Test Server"
image = "desktop.png"
```

### Configuration with Global Options

```toml
[global]
name = "My VNC Server"
show_ip = true

[server.my_server]
listen = "0.0.0.0:5900"
server_name = "Test Server"
image = "desktop.png"
```

### Advanced Configuration with Image Rotation

```toml
[global]
name = "FictusVNC"
no_brand = false
show_ip = true

# Server with rotation enabled
[server.random_rotation]
listen = "0.0.0.0:5900"
server_name = "Random Server"
rotation_mode = "random"
images = [
  {path = "normal_desktop.png", weight = 60},
  {path = "busy_desktop.png", weight = 30},
  {path = "error_screen.png", weight = 10}
]

# Server with sequential rotation
[server.sequential_boot]
listen = "0.0.0.0:5901"
server_name = "Boot Sequence"
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
server_name = "Static Server"
image = "desktop.png"
```

### Port Ranges

```toml
[server.multi_port]
listen = "0.0.0.0"
start_port = 5900
end_port = 5905
server_name = "Multi-Port Server"
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
- Each image can have a `weight` parameter (**default: 1 if not specified**)
- Higher weights make images more likely to be selected
- **Example:** weights 50, 30, 20 = 50%, 30%, 20% probability
- **Example:** no weights specified = equal distribution (all images have weight 1)
- Perfect for simulating realistic desktop scenarios with varying frequencies

### Sequential Mode Details

- **Only active when using `images` array**
- Images cycle in the order defined in configuration
- Each new VNC connection gets the next image in sequence
- Ideal for simulating boot processes or step-by-step scenarios

## Configuration Options

### Global Section
| Parameter  | Type    | Description                                       | Default       |
| ---------- | ------- | ------------------------------------------------- | ------------- |
| `name`     | string  | Default server name (if not specified per server) | `"FictusVNC"` |
| `no_brand` | boolean | Disable "FictusVNC -" prefix in server names      | `false`       |
| `show_ip`  | boolean | Display client IP address on images               | `false`       |

### Server Section

| Parameter       | Type   | Description                                             | Default         |
| --------------- | ------ | ------------------------------------------------------- | --------------- |
| `listen`        | string | Listen address and port                                 | Required        |
| `server_name`   | string | Display name for the server                             | Server key name |
| `image`         | string | **Single image path (default mode)**                    | -               |
| `images`        | array  | **Array of images for rotation (optional)**             | -               |
| `rotation_mode` | string | `"random"` or `"sequential"` (ignored if using `image`) | `"random"`      |
| `start_port`    | int    | Start of port range                                     | -               |
| `end_port`      | int    | End of port range                                       | -               |

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
show_ip = true
no_brand = true

[server.main]
listen = ":5900"
image = "desktop.png"
```

## Building from Source

```bash
git clone https://github.com/ayebrian/fictusvnc.git
cd fictusvnc
go build -o fictusvnc .
```

---

## License

This project is licensed under the terms specified in the [LICENSE](LICENSE) file.
