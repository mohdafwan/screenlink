#!/usr/bin/env python3
"""
capture.py — stream the Wayland desktop as a series of JPEG frames.

It uses the xdg-desktop-portal ScreenCast API (the only way to capture a real
Wayland desktop) to obtain a PipeWire video stream, then GStreamer encodes each
frame to JPEG. Frames are written to stdout, each prefixed with a 4-byte
big-endian length, so a parent process (our Go host) can read them as a stream.

Env knobs:
  SCREENLINK_FPS      target frames per second (default 12)
  SCREENLINK_QUALITY  JPEG quality 1-100      (default 70)
  SCREENLINK_WIDTH    scale to this width, keeping aspect (0 = native)

A restore token is cached so repeat runs don't re-prompt for permission.
"""

import os
import sys
import signal
import ctypes
import warnings
import gi
gi.require_version("Gst", "1.0")
from gi.repository import GLib, Gst
import dbus
from dbus.mainloop.glib import DBusGMainLoop

# Cosmetic: silence the unix_fd_add_full deprecation notice (the replacement
# isn't available everywhere yet).
warnings.filterwarnings("ignore", message=".*unix_fd_add_full.*")

FPS = int(os.environ.get("SCREENLINK_FPS", "12"))
QUALITY = int(os.environ.get("SCREENLINK_QUALITY", "70"))
WIDTH = int(os.environ.get("SCREENLINK_WIDTH", "0"))
CODEC = os.environ.get("SCREENLINK_CODEC", "mjpeg")   # "mjpeg" | "h264"
BITRATE = int(os.environ.get("SCREENLINK_BITRATE", "6000"))  # kbps, h264 only

TOKEN_FILE = os.path.expanduser("~/.config/screenlink/restore_token")


def log(*a):
    print("[capture]", *a, file=sys.stderr, flush=True)


def die_with_parent():
    # PR_SET_PDEATHSIG: ask the kernel to SIGKILL us if the Go host dies, so a
    # crashed/killed host never leaves an orphaned capture.py encoding forever.
    try:
        ctypes.CDLL("libc.so.6", use_errno=True).prctl(1, signal.SIGKILL)
    except Exception:
        pass  # best effort; Linux-only and non-critical


DBusGMainLoop(set_as_default=True)
Gst.init(None)

bus = dbus.SessionBus()
portal = bus.get_object("org.freedesktop.portal.Desktop",
                        "/org/freedesktop/portal/desktop")
screencast = dbus.Interface(portal, "org.freedesktop.portal.ScreenCast")
sender = bus.get_unique_name()[1:].replace(".", "_")
loop = GLib.MainLoop()
state = {"session": None, "node_id": None, "n": 0}
out = sys.stdout.buffer


def new_token(kind):
    state["n"] += 1
    return f"screenlink_{kind}_{state['n']}"


def on_request(handle, cb):
    obj = bus.get_object("org.freedesktop.portal.Desktop", handle)
    iface = dbus.Interface(obj, "org.freedesktop.portal.Request")
    sig = []

    def handler(response, results):
        if sig:
            sig[0].remove()
        if response != 0:
            fail(f"portal denied/cancelled (response={response})")
            return
        cb(results)

    sig.append(iface.connect_to_signal("Response", handler))


def fail(msg):
    log("FAIL:", msg)
    loop.quit()
    sys.exit(1)


def load_token():
    try:
        with open(TOKEN_FILE) as f:
            return f.read().strip()
    except OSError:
        return None


def save_token(tok):
    if not tok:
        return
    os.makedirs(os.path.dirname(TOKEN_FILE), exist_ok=True)
    with open(TOKEN_FILE, "w") as f:
        f.write(tok)


def create_session():
    token = new_token("create")
    handle = f"/org/freedesktop/portal/desktop/request/{sender}/{token}"
    on_request(handle, got_session)
    screencast.CreateSession({
        "handle_token": token,
        "session_handle_token": new_token("session"),
    })


def got_session(results):
    state["session"] = results["session_handle"]
    select_sources()


def select_sources():
    token = new_token("select")
    handle = f"/org/freedesktop/portal/desktop/request/{sender}/{token}"
    on_request(handle, lambda r: start())
    screencast.SelectSources(state["session"], {
        "handle_token": token,
        "types": dbus.UInt32(1),        # MONITOR (whole screen)
        "multiple": False,
        "cursor_mode": dbus.UInt32(2),  # draw the cursor into the video
        "persist_mode": dbus.UInt32(2), # remember permission until revoked
    })


def start():
    token = new_token("start")
    handle = f"/org/freedesktop/portal/desktop/request/{sender}/{token}"
    on_request(handle, got_streams)
    opts = {"handle_token": token}
    tok = load_token()
    if tok:
        opts["restore_token"] = tok
    else:
        log("a 'Share your screen?' dialog should appear — approve it once.")
    screencast.Start(state["session"], "", opts)


def got_streams(results):
    if results.get("restore_token"):
        save_token(str(results["restore_token"]))
    streams = results.get("streams")
    if not streams:
        fail("no streams returned")
    state["node_id"] = int(streams[0][0])
    size = streams[0][1].get("size")
    if size is not None:
        state["src_w"], state["src_h"] = int(size[0]), int(size[1])
    open_remote()


def open_remote():
    fd = screencast.OpenPipeWireRemote(state["session"], {}).take()
    start_pipeline(fd, state["node_id"])


def start_pipeline(fd, node_id):
    scale = ""
    src_w, src_h = state.get("src_w"), state.get("src_h")
    if WIDTH > 0 and src_w and src_h and WIDTH < src_w:
        # Compute the height ourselves from the source aspect ratio and pin BOTH
        # dimensions. Fixing only width (even with pixel-aspect-ratio=1/1) lets
        # caps negotiation pick the height, and downstream accepts any height >=1
        # — so it collapses to height=1 (a 1px-tall frame). Round to an even
        # height since jpegenc/videoconvert want even dimensions.
        h = max(2, round(WIDTH * src_h / src_w / 2) * 2)
        scale = f"videoscale ! video/x-raw,width={WIDTH},height={h} ! "
    elif WIDTH > 0 and not (src_w and src_h):
        # No source size from the portal: skip scaling rather than risk the
        # height=1 collapse. Native resolution still streams fine.
        log("no source size; streaming native (ignoring SCREENLINK_WIDTH)")
    if CODEC == "h264":
        # Inter-frame compression: only the *changes* between frames are sent, so
        # a mostly-static desktop streams at a fraction of MJPEG's bytes. CBR caps
        # the bitrate regardless of resolution, which is what lets us keep native
        # res over a thin link. tune=zerolatency disables B-frames/lookahead for
        # real-time; key-int-max=FPS emits a keyframe every ~1s so a viewer can
        # (re)sync quickly. config-interval=-1 repeats SPS/PPS before each IDR so
        # every keyframe is self-decodable. byte-stream/au = Annex-B, one access
        # unit per buffer (what the browser's WebCodecs decoder wants).
        #
        # drop=false: never silently drop an *encoded* frame here — that would
        # break every later frame until the next keyframe. When the consumer is
        # slow, backpressure reaches the live pipewiresrc, which drops *raw*
        # frames instead (harmless — the encoder just sees a lower frame rate).
        enc = (
            f"x264enc tune=zerolatency speed-preset=superfast "
            f"bitrate={BITRATE} key-int-max={max(1, FPS)} name=enc ! "
            f"video/x-h264,profile=constrained-baseline ! "
            f"h264parse config-interval=-1 ! "
            f"video/x-h264,stream-format=byte-stream,alignment=au ! "
            f"appsink name=sink emit-signals=true max-buffers=4 drop=false"
        )
    else:
        enc = (
            f"jpegenc quality={QUALITY} name=enc ! "
            f"appsink name=sink emit-signals=true max-buffers=1 drop=true"
        )
    desc = (
        f"pipewiresrc fd={fd} path={node_id} ! "
        f"videorate ! video/x-raw,framerate={FPS}/1 ! "
        f"{scale}videoconvert ! {enc}"
    )
    log("pipeline:", desc)
    pipeline = Gst.parse_launch(desc)
    sink = pipeline.get_by_name("sink")
    sink.connect("new-sample", on_sample)
    state["enc"] = pipeline.get_by_name("enc")
    pipeline.set_state(Gst.State.PLAYING)
    if CODEC == "h264":
        log(f"streaming h264 {FPS}fps {BITRATE}kbps" + (f" width={WIDTH}" if WIDTH else " (native)"))
    else:
        log(f"streaming mjpeg {FPS}fps q{QUALITY}" + (f" width={WIDTH}" if WIDTH else " (native)"))
    watch_stdin()  # let the Go host retune quality at runtime (adaptive bitrate)


# --- runtime control: the host writes one-line commands to our stdin ---

_stdin_buf = b""


def handle_cmd(line):
    # "QUALITY <1-100>" — live JPEG quality (mjpeg). "BITRATE <kbps>" — live
    # x264 bitrate (h264). Both let the host's adaptive controller retune the
    # encoder without restarting the pipeline.
    parts = line.split()
    enc = state.get("enc")
    if enc is None or len(parts) != 2:
        return
    if parts[0] == "QUALITY" and CODEC != "h264":
        try:
            enc.set_property("quality", max(1, min(100, int(parts[1]))))
        except ValueError:
            return
    elif parts[0] == "BITRATE" and CODEC == "h264":
        try:
            enc.set_property("bitrate", max(100, int(parts[1])))
        except ValueError:
            return


def on_stdin(fd, _cond):
    global _stdin_buf
    try:
        data = os.read(fd, 4096)
    except (BlockingIOError, OSError):
        return True
    if not data:
        return False  # host closed stdin; stop watching
    _stdin_buf += data
    while b"\n" in _stdin_buf:
        line, _stdin_buf = _stdin_buf.split(b"\n", 1)
        handle_cmd(line.decode(errors="ignore").strip())
    return True


def watch_stdin():
    fd = sys.stdin.fileno()
    os.set_blocking(fd, False)
    GLib.unix_fd_add_full(GLib.PRIORITY_DEFAULT, fd, GLib.IOCondition.IN, on_stdin)


def on_sample(sink):
    sample = sink.emit("pull-sample")
    if sample is None:
        return Gst.FlowReturn.OK
    if not state.get("logged_dims"):
        state["logged_dims"] = True
        caps = sample.get_caps()
        if caps is not None:
            st = caps.get_structure(0)
            ok_w, w = st.get_int("width")
            ok_h, h = st.get_int("height")
            log(f"first frame dims: {w if ok_w else '?'}x{h if ok_h else '?'}")
    buf = sample.get_buffer()
    ok, m = buf.map(Gst.MapFlags.READ)
    if ok:
        data = bytes(m.data)   # copy out before unmap
        delta = bool(buf.get_flags() & Gst.BufferFlags.DELTA_UNIT)
        buf.unmap(m)
        try:
            if CODEC == "h264":
                # Prefix each access unit with a 1-byte flag (1 = keyframe / IDR,
                # 0 = delta) so the viewer knows where it can (re)sync. The 4-byte
                # length covers the flag byte + the AU.
                out.write((len(data) + 1).to_bytes(4, "big"))
                out.write(b"\x01" if not delta else b"\x00")
                out.write(data)
            else:
                out.write(len(data).to_bytes(4, "big"))
                out.write(data)
            out.flush()
        except BrokenPipeError:
            # Parent (Go host) went away. We're on a GStreamer thread mid-stream;
            # returning to the Python finalizer while other GStreamer threads are
            # still live triggers a fatal interpreter-shutdown race, so exit hard.
            os._exit(0)
    return Gst.FlowReturn.OK


# Exit hard on these too, for the same reason: don't let the interpreter finalize
# while GStreamer streaming threads may still call back into Python.
signal.signal(signal.SIGINT, lambda *_: os._exit(0))
signal.signal(signal.SIGTERM, lambda *_: os._exit(0))
die_with_parent()
create_session()
loop.run()
