# screenlink

A from-scratch **remote desktop** (AnyDesk-style screen view + control) for
Linux/Wayland. Companion to `termlink` (the terminal-only remote shell).

On Wayland the only way to capture the real desktop is the
`xdg-desktop-portal` ScreenCast API, which yields a **PipeWire** video stream.
`capture.py` drives that (and the one-time "Share your screen?" permission),
GStreamer encodes each frame to JPEG, and the Go host streams them to a browser.

## Status

- [x] **Phase 1 — live screen in the browser (view only)** ✅
- [ ] Phase 2 — mouse/keyboard control (browser → host via ydotool/uinput)
- [ ] Phase 3 — connect over the internet via the termlink relay (AnyDesk-style ID)
- [ ] Phase 4 — bandwidth: downscale / delta / H.264 instead of full JPEG frames

## Requirements (already present on this machine)

- A Wayland session with `xdg-desktop-portal-gnome` + `pipewire`
- `gstreamer` with `pipewiresrc` + `jpegenc`
- `python3` with `gi` (PyGObject) and `dbus`
- Go (local install at `~/.go-sdk`)

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

## How it fits together

```
 PipeWire portal ──► capture.py ──(len+JPEG frames on stdout)──► Go host ──► browser
   (Wayland)          gst encode                                 MJPEG /stream   <img>
```

## Layout

```
main.go                      CLI + MJPEG HTTP server
capture.py                   portal + PipeWire + GStreamer -> JPEG frame stream
web/index.html               the viewer (just an <img src="/stream">)
internal/capture/capture.go  spawns capture.py, parses its frame stream
internal/hub/hub.go          fans the latest frame out to all viewers
capture_probe.py             standalone "can we capture at all?" test
```
