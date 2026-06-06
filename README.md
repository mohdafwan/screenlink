# screenlink

A from-scratch **remote desktop** (AnyDesk-style screen view + control) for
Linux/Wayland. Companion to `termlink` (the terminal-only remote shell).

On Wayland the only way to capture the real desktop is the
`xdg-desktop-portal` ScreenCast API, which yields a **PipeWire** video stream.
`capture.py` drives that (and the one-time "Share your screen?" permission),
GStreamer encodes each frame to JPEG, and the Go host streams them to a browser.
The browser also captures the viewer's mouse/keyboard and ships them back over a
WebSocket, where the host replays them through the kernel's **uinput** device.

## Status

- [x] **Phase 1 — live screen in the browser (view only)** ✅
- [x] **Phase 2 — mouse/keyboard control (browser → host via uinput)** ✅
- [x] **Phase 3 — connect over the internet via a relay (AnyDesk-style ID)** ✅
- [ ] Phase 4 — bandwidth: downscale / delta / H.264 instead of full JPEG frames

## Requirements (already present on this machine)

- A Wayland session with `xdg-desktop-portal-gnome` + `pipewire`
- `gstreamer` with `pipewiresrc` + `jpegenc`
- `python3` with `gi` (PyGObject) and `dbus`
- Go (local install at `~/.go-sdk`)
- For **control** (Phase 2): write access to `/dev/uinput` — see below.

## Enabling remote control (Phase 2)

Injecting input needs write access to `/dev/uinput`. It's `root:input` (mode
660) by default, so the one-time fix is to join the `input` group:

```sh
sudo usermod -aG input "$USER"     # one time
```

Then either **log out and back in**, or launch the host with the group applied
immediately (no relogin):

```sh
sg input -c 'make run'             # or: sg input -c './bin/screenlink host --addr :8087'
```

Without uinput access the host still runs, but **view-only** — it logs the
reason and the viewer shows "remote control unavailable". You can also force
view-only with `--no-input`.

In the browser: **click the screen to take control**, then your mouse, scroll
wheel, and keyboard drive the remote desktop. Press **Esc** to release control
(so local browser shortcuts work again).

## Build & run

```sh
make build                 # -> bin/screenlink (+ copies capture.py next to it)
make run                   # serves on :8087

# or directly:
./bin/screenlink host --addr :8087 --fps 12 --quality 70 --width 0
```

Then open **http://localhost:8087** in a browser. The first run pops a
"Share your screen?" dialog — approve it once (the permission is remembered).

Flags:

| flag | meaning | default |
|---|---|---|
| `--addr` | address to serve the viewer on | `:8087` |
| `--fps` | capture frames per second | `12` |
| `--quality` | JPEG quality 1–100 | `70` |
| `--width` | downscale to this width (0 = native) | `0` |

Lower `--fps`/`--quality` or set `--width 1280` to cut bandwidth (native 1080p
JPEG is ~5 MB/s — fine on a LAN, too heavy for the open internet; Phase 4
addresses this).

## Connecting over the internet (Phase 3)

Two machines behind home routers can't reach each other directly (NAT). A
**relay** with a public IP solves it AnyDesk-style: both sides dial *out* to the
relay, which matches them by a 9-digit address and pipes bytes between them. No
port-forwarding. (The wire protocol is identical to `termlink`'s relay, so one
relay serves both apps.)

**1. Run a relay** on any box with a reachable address:

```sh
screenlink relay --addr :9000
```

**2. On the machine to control**, register with the relay:

```sh
screenlink host --relay RELAY_HOST:9000
#   Address : 707 490 730
#   Passcode: 408915
```

**3. On the viewing machine**, connect with that address + passcode:

```sh
screenlink connect --relay RELAY_HOST:9000 --pin 408915 707490730
#   Open the remote desktop at:  http://localhost:8087
```

Then open `http://localhost:8087` — a local proxy tunnels the browser (view +
control) through the relay to the host. Pass `--pin` to `host` to pin a fixed
passcode instead of a random one.

> ⚠️ Traffic over the relay is **plaintext** — the passcode keeps casual
> ID-guessers out, but a malicious relay can see (and inject) everything. Run
> your own relay, or wait for the TLS/E2E-encryption follow-up before using this
> over an untrusted network.

## How it fits together

```
 PipeWire portal ──► capture.py ──(len+JPEG frames on stdout)──► Go host ──► browser
   (Wayland)          gst encode                                 MJPEG /stream   <img>

 browser  ──(mouse/keyboard JSON over WebSocket /input)──►  Go host  ──►  /dev/uinput
   events                                                   wsock         virtual device

 over the internet (Phase 3):
   viewer browser ─► screenlink connect (local proxy) ─► relay ─► screenlink host ─► (above)
```

## Layout

```
main.go                      CLI: host / connect / relay subcommands
capture.py                   portal + PipeWire + GStreamer -> JPEG frame stream
web/index.html               the viewer (<img src="/stream"> + input capture)
internal/capture/capture.go  spawns capture.py, parses its frame stream
internal/hub/hub.go          fans the latest frame out to all viewers
internal/input/uinput.go     virtual mouse+keyboard via /dev/uinput (no daemon)
internal/input/keymap.go     browser KeyboardEvent.code -> Linux keycodes
internal/wsock/wsock.go      minimal dependency-free WebSocket server
internal/relay/relay.go      public-IP byte-pipe matched by 9-digit ID
internal/relay/host.go       host registration + http.Serve over relay conns
internal/relay/connect.go    viewer-side local proxy (browser -> relay -> host)
capture_probe.py             standalone "can we capture at all?" test
```
