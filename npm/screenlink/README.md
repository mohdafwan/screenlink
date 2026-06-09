# screenlink

AnyDesk-style **remote desktop** (live screen view + mouse/keyboard control)
for Linux/Wayland. One package, one command — it bundles a prebuilt native binary
for every platform and runs the right one (no Go toolchain needed).

```sh
npm install -g screenlink
```

## What runs where

- **Host (share a screen):** Linux/Wayland only — needs `xdg-desktop-portal`
  + PipeWire + GStreamer for capture and `/dev/uinput` for control.
- **View & control a remote host:** any OS (the viewer is a browser; `connect`
  and `relay` are pure Go and run on Linux, macOS, and Windows).

## Use it

Share this machine's screen over the internet in one command (Linux host):

```sh
screenlink host --tunnel
```

It prints a public URL + password — open that in any browser, on any device, to
view and control. (Needs [`cloudflared`](https://github.com/cloudflare/cloudflared)
on the host. H.264 is the default and needs a Chrome/Edge viewer; pass
`--codec mjpeg` for any browser.)

Or go through your own relay (AnyDesk-style ID), which works from any OS:

```sh
screenlink relay  --addr :9000                                   # on a public box
screenlink host   --relay RELAY_HOST:9000                        # on the Linux host
screenlink connect --relay RELAY_HOST:9000 --pin CODE <address>  # on any viewer
```

## Host system requirements (Linux)

The package ships the binary and `capture.py`, but **hosting** relies on system
packages npm does not install:

- a Wayland session with `xdg-desktop-portal` + `pipewire`
- GStreamer with `pipewiresrc`, `x264enc` + `h264parse` (H.264) and `jpegenc` (MJPEG)
- `python3` with `gi` (PyGObject) and `dbus`
- write access to `/dev/uinput` for control (`sudo usermod -aG input "$USER"`)

Viewing/relaying from macOS or Windows needs none of these.

## Full docs

<https://github.com/mohdafwan/screenlink>
