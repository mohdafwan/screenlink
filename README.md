# screenlink

[![npm](https://img.shields.io/npm/v/screenlink.svg)](https://www.npmjs.com/package/screenlink)

A from-scratch **remote desktop** (AnyDesk-style screen view + control) for
Linux/Wayland. Companion to `termlink` (the terminal-only remote shell).

Install in one command on any OS — npm ships a prebuilt binary:

```sh
npm install -g screenlink
```

> 📦 npm: <https://www.npmjs.com/package/screenlink>

On Wayland the only way to capture the real desktop is the
`xdg-desktop-portal` ScreenCast API, which yields a **PipeWire** video stream.
`capture.py` drives that (and the one-time "Share your screen?" permission),
GStreamer encodes it (**H.264** by default, MJPEG optional), and the Go host
streams the frames to a browser over a WebSocket. The browser also captures the
viewer's mouse/keyboard and ships them back over the same socket, where the host
replays them through the kernel's **uinput** device.

## Requirements

- A Wayland session with `xdg-desktop-portal-gnome` + `pipewire`
- `gstreamer` with `pipewiresrc`, `x264enc` + `h264parse` (H.264) and `jpegenc` (MJPEG)
- `python3` with `gi` (PyGObject) and `dbus`
- Go (local install at `~/.go-sdk`)
- A viewer browser: any browser for MJPEG; **Chrome/Edge** for H.264 (needs WebCodecs)
- For **control**: write access to `/dev/uinput` — see below.

## Enabling remote control

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

## Install & run

The easiest way — installs the `screenlink` CLI onto your `PATH` with a prebuilt
binary, no Go toolchain needed (works on Linux, macOS, Windows):

```sh
npm install -g screenlink         # https://www.npmjs.com/package/screenlink
```

Or build from source with Go ≥ 1.22:

```sh
go install github.com/mohdafwan/screenlink@latest
```

Either way, the three subcommands work the same everywhere:

```sh
screenlink host                       # share THIS screen (Linux/Wayland only)
screenlink connect --relay … --pin … <address>   # view/control a remote host
screenlink relay                      # run a relay
```

Hosting needs `capture.py` next to the binary; the simplest way to host is from
a clone of the repo:

```sh
git clone https://github.com/mohdafwan/screenlink && cd screenlink
make run                   # build + serve on :8087  (or: go run . host)
```

Then open **http://localhost:8087** from **another device** (viewing on the host
machine itself is blocked — see below). The first run pops a "Share your screen?"
dialog — approve it once (the permission is remembered). The `connect` and
`relay` subcommands don't need `capture.py`, so `go install` alone is enough for
those.

Flags:

| flag | meaning | default |
|---|---|---|
| `--addr` | address to serve the viewer on | `:8087` |
| `--codec` | `h264` (sharp, low-bitrate, Chrome/Edge) or `mjpeg` (any browser) | `h264` |
| `--bitrate` | H.264 target bitrate in kbps (CBR); higher = sharper | `6000` |
| `--fps` | max capture frames per second | `40` |
| `--width` | downscale to this width (0 = native) | `0` |
| `--quality` | max JPEG quality 1–100 (MJPEG only, adaptive ceiling) | `70` |
| `--min-quality` | lowest quality adaptive will drop to (MJPEG only) | `20` |
| `--no-adaptive` | hold quality/fps fixed (MJPEG only) | off |
| `--password` | require this password (HTTP Basic Auth) — use when tunneling | — |
| `--allow-local` | allow viewing on the host machine itself | off |
| `--tunnel` | expose a public https URL via `cloudflared` | off |

## Video codecs

**H.264 (default).** Inter-frame compression: only what *changed* between frames
is sent, so a mostly-static desktop streams at a fraction of MJPEG's bytes. That
lets the host keep **native resolution** even on a thin link and cap the load
with a fixed CBR `--bitrate`. The host emits a keyframe ~once a second; the
browser decodes with **WebCodecs** into a `<canvas>` and re-syncs to the next
keyframe after any dropped frame, so glitches self-heal in ≈1s. Chrome/Edge only.

**MJPEG (`--codec mjpeg`).** Every frame is a standalone JPEG painted into an
`<img>` — works in any browser, but uses far more bandwidth at the same quality.
Use it for non-Chromium viewers, or when WebCodecs isn't available.

For MJPEG the host **auto-tunes** to what the viewer can receive: once a second
it checks the viewers' drop rate and steps a 5-rung quality+fps ladder — backing
off fast on congestion, climbing back toward `--quality`/`--fps` with headroom
(`--min-quality` is the floor). It's all low-latency: quality is changed live on
the encoder and fps is capped host-side; resolution stays at `--width`. H.264
holds its fixed CBR cap instead and leans on keyframe re-sync.

Over the internet (`--tunnel`/`--relay`) the defaults drop automatically: H.264
to 3000 kbps @ 25 fps (native res), MJPEG to 960-wide @ 20 fps. Override any knob.

## Viewing on the host is blocked

By default the host refuses connections from **its own browser** — viewing the
captured desktop on the very machine being captured just feeds the stream back
into itself (a hall-of-mirrors). Such requests get a short "open this on another
device" page. Tunnel and relay viewers are unaffected. Pass `--allow-local` to
opt back in (e.g. when developing screenlink itself).

## Quickest internet access — one command

```sh
screenlink host --tunnel
```

That's it. `--tunnel` starts the host, generates a password, launches a
[`cloudflared`](https://github.com/cloudflare/cloudflared) tunnel, and prints a
public URL + password:

```
  ┌─────────────────────────────────────────────────────────┐
  │  Share these with whoever should view/control this screen │
  └─────────────────────────────────────────────────────────┘
    URL     : https://random-words.trycloudflare.com
    Password: 73f9902d4f
```

The other person opens that URL in **any browser on any network**, enters the
password, and gets live view + control. No relay, no public server, nothing to
install on their side. (`cloudflared` must be installed on the host —
<https://github.com/cloudflare/cloudflared/releases>. `--password PW` pins your
own password instead of a generated one.)

> Some ISPs block DNS for `*.trycloudflare.com`. If the URL shows
> "DNS address could not be found" but `1.1.1.1`/`8.8.8.8` resolve it, point your
> machine at one of those resolvers — or use the relay below.

The built-in relay below is the alternative when you want your *own* fixed
address/passcode infrastructure (AnyDesk-style) instead of a cloudflare URL.

## Connecting over the internet

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

## Running on Windows / macOS

screenlink can only **host** (share a screen) on **Linux/Wayland** — capture
(xdg-desktop-portal + PipeWire + GStreamer) and control (`/dev/uinput`) are
Linux-only. Windows/macOS can't be the host. But the **viewer** and **relay**
are pure Go and run anywhere, so you can control a Linux box *from* Windows/macOS.

**A. View on the same LAN — no install needed.** On the Linux host run
`screenlink host`, then on the Windows/Mac machine just open the host's URL in a
browser: `http://HOST-IP:8087`. View and control both work in the browser; no
screenlink download required.

**B. View over the internet — needs the `screenlink connect` proxy.** Install the
CLI with the *same one command* as on Linux (no `.exe` to build or copy — just
[Go](https://go.dev/dl) on the machine):

```sh
go install github.com/mohdafwan/screenlink@latest
```

Then connect using the address + passcode the host printed — identical command on
Windows, macOS, and Linux:

```sh
screenlink connect --relay RELAY_HOST:9000 --pin 408915 707490730
# Open the remote desktop at:  http://localhost:8087
```

You can also run the relay anywhere the same way: `screenlink relay --addr :9000`.

## How it fits together

```
 PipeWire portal ──► capture.py ──(length-prefixed H.264/JPEG frames)──► Go host ──► browser
   (Wayland)          gst encode                                  WebSocket /ws   <canvas>/<img>

 browser ──(mouse/keyboard JSON over the same WebSocket)──► Go host ──► /dev/uinput
   events                                                    wsock        virtual device

 over the internet:
   viewer browser ─► cloudflared tunnel  ──────────────────┐
   viewer browser ─► screenlink connect ─► relay ─► screenlink host ─► (above)
```

## Layout

```
main.go                      CLI: host / connect / relay subcommands; codec + access gating
capture.py                   portal + PipeWire + GStreamer -> H.264/JPEG frame stream
web/index.html               the viewer: WebCodecs/<canvas> (h264) or <img> (mjpeg) + input capture
internal/capture/capture.go  spawns capture.py, reads its frame stream, retunes the encoder live
internal/hub/hub.go          fans the latest frame out to all viewers + drop stats
internal/adaptive/adaptive.go bandwidth ladder + fps gate (MJPEG auto quality/fps)
internal/input/uinput.go     virtual mouse+keyboard via /dev/uinput (no daemon)
internal/input/keymap.go     browser KeyboardEvent.code -> Linux keycodes
internal/wsock/wsock.go      minimal dependency-free WebSocket server
internal/relay/relay.go      public-IP byte-pipe matched by 9-digit ID
internal/relay/host.go       host registration + http.Serve over relay conns
internal/relay/connect.go    viewer-side local proxy (browser -> relay -> host)
capture_probe.py             standalone "can we capture at all?" test
```
