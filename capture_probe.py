#!/usr/bin/env python3
"""
capture_probe.py — prove that we can capture this Wayland desktop.

Wayland blocks the old X11 screen-grab tricks, so the only way to capture the
real desktop is the xdg-desktop-portal ScreenCast API: it pops a "Share your
screen?" dialog, then hands us a PipeWire video stream. We consume that stream
with GStreamer and save ONE frame as a JPEG to prove it works end to end.

Run it, click "Share" + "Allow" on the popup, and check /tmp/screenlink_test.jpg.
"""

import sys
import signal
import gi
gi.require_version("Gst", "1.0")
from gi.repository import GLib, Gst
import dbus
from dbus.mainloop.glib import DBusGMainLoop

OUT = "/tmp/screenlink_test.jpg"

DBusGMainLoop(set_as_default=True)
Gst.init(None)

bus = dbus.SessionBus()
portal = bus.get_object("org.freedesktop.portal.Desktop",
                        "/org/freedesktop/portal/desktop")
screencast = dbus.Interface(portal, "org.freedesktop.portal.ScreenCast")

# Unique sender name (bus name with dots -> underscores) for Request handle paths.
sender = bus.get_unique_name()[1:].replace(".", "_")
loop = GLib.MainLoop()

state = {"session": None, "node_id": None, "token_n": 0}


def new_token(kind):
    state["token_n"] += 1
    return f"screenlink_{kind}_{state['token_n']}"


def on_request(handle, callback):
    """Subscribe to the Response signal of a portal Request object."""
    obj = bus.get_object("org.freedesktop.portal.Desktop", handle)
    iface = dbus.Interface(obj, "org.freedesktop.portal.Request")
    sig = None

    def handler(response, results):
        if sig:
            sig.remove()
        if response != 0:
            fail(f"portal request denied/cancelled (response={response})")
            return
        callback(results)

    sig = iface.connect_to_signal("Response", handler)


def fail(msg):
    print("FAIL:", msg, file=sys.stderr)
    loop.quit()
    sys.exit(1)


# Step 1: CreateSession
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


# Step 2: SelectSources (1 = monitor/whole screen)
def select_sources():
    token = new_token("select")
    handle = f"/org/freedesktop/portal/desktop/request/{sender}/{token}"
    on_request(handle, lambda r: start())
    screencast.SelectSources(state["session"], {
        "handle_token": token,
        "types": dbus.UInt32(1),     # MONITOR
        "multiple": False,
        "cursor_mode": dbus.UInt32(2),  # embed the cursor in the video
    })


# Step 3: Start — THIS triggers the permission popup
def start():
    token = new_token("start")
    handle = f"/org/freedesktop/portal/desktop/request/{sender}/{token}"
    on_request(handle, got_streams)
    print(">>> A 'Share your screen?' dialog should appear — click Share/Allow.")
    screencast.Start(state["session"], "", {"handle_token": token})


def got_streams(results):
    streams = results.get("streams")
    if not streams:
        fail("no streams returned")
    node_id = streams[0][0]
    state["node_id"] = int(node_id)
    open_remote()


# Step 4: OpenPipeWireRemote -> a file descriptor for the PipeWire connection
def open_remote():
    fd_obj = screencast.OpenPipeWireRemote(state["session"], {})
    fd = fd_obj.take()
    grab_frame(fd, state["node_id"])


# Step 5: GStreamer pulls one JPEG frame from the PipeWire node
def grab_frame(fd, node_id):
    desc = (
        f"pipewiresrc fd={fd} path={node_id} ! "
        f"videoconvert ! video/x-raw,format=RGB ! "
        f"jpegenc quality=80 ! appsink name=sink emit-signals=false max-buffers=1 drop=true"
    )
    print(">>> pipeline:", desc)
    pipeline = Gst.parse_launch(desc)
    sink = pipeline.get_by_name("sink")
    pipeline.set_state(Gst.State.PLAYING)

    def poll():
        # Use the appsink action signal (works without the GstApp gi typelib).
        sample = sink.emit("try-pull-sample", 100 * Gst.MSECOND)
        if sample is None:
            return True  # keep polling
        buf = sample.get_buffer()
        ok, minfo = buf.map(Gst.MapFlags.READ)
        if ok:
            with open(OUT, "wb") as f:
                f.write(minfo.data)
            buf.unmap(minfo)
            print(f"OK: wrote {OUT} ({len(minfo.data) if hasattr(minfo,'data') else '?'} bytes)")
        pipeline.set_state(Gst.State.NULL)
        loop.quit()
        return False

    GLib.timeout_add(100, poll)
    GLib.timeout_add_seconds(20, lambda: fail("timed out waiting for a frame"))


signal.signal(signal.SIGINT, lambda *_: loop.quit())
create_session()
loop.run()
