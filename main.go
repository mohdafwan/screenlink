// screenlink — a from-scratch remote desktop (AnyDesk-style) for Wayland.
//
// The host captures the screen via the PipeWire portal and serves it to a
// browser as a live MJPEG stream (Phase 1), and accepts mouse/keyboard control
// from the browser over a WebSocket, replaying it through uinput (Phase 2).
//
// Usage:
//
//	screenlink host [--addr :8080] [--fps 12] [--quality 70] [--width 0] [--no-input]
//
// Then open http://localhost:8080 in a browser.
package main

import (
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mohdafwan/screenlink/internal/adaptive"
	"github.com/mohdafwan/screenlink/internal/capture"
	"github.com/mohdafwan/screenlink/internal/hub"
	"github.com/mohdafwan/screenlink/internal/input"
	"github.com/mohdafwan/screenlink/internal/relay"
	"github.com/mohdafwan/screenlink/internal/wsock"
)

//go:embed web/index.html
var webFS embed.FS

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "host":
		runHost(os.Args[2:])
	case "connect":
		runConnect(os.Args[2:])
	case "relay":
		runRelay(os.Args[2:])
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage:")
	fmt.Fprintln(os.Stderr, "  screenlink host    [--addr :8087] [--fps 40] [--quality 70] [--width 0] [--no-input] [--relay HOST:PORT] [--pin CODE]")
	fmt.Fprintln(os.Stderr, "  screenlink connect [--relay HOST:PORT] [--pin CODE] [--addr :8087] <address>")
	fmt.Fprintln(os.Stderr, "  screenlink relay   [--addr :9000]")
	os.Exit(2)
}

// runHost captures the screen and serves it (Phase 1+2). With --relay it also
// registers with a relay so viewers can connect over the internet (Phase 3).
func runHost(args []string) {
	fs := flag.NewFlagSet("host", flag.ExitOnError)
	addr := fs.String("addr", ":8087", "address to serve the viewer on (local/LAN)")
	fpsFlag := fs.Int("fps", 40, "capture frames per second")
	quality := fs.Int("quality", 70, "JPEG quality 1-100")
	width := fs.Int("width", 0, "scale to this width keeping aspect (0 = native); e.g. 1280 = 720p")
	script := fs.String("capture", "", "path to capture.py (default: next to the binary, then ./capture.py)")
	noInput := fs.Bool("no-input", false, "disable remote control (view-only); the viewer cannot move the mouse or type")
	relayAddr := fs.String("relay", "", "register with this relay (HOST:PORT) for AnyDesk-style internet access")
	pin := fs.String("pin", "", "passcode viewers must present (default: a random 6-digit code)")
	minQuality := fs.Int("min-quality", 20, "lowest JPEG quality the adaptive controller will drop to")
	noAdaptive := fs.Bool("no-adaptive", false, "disable adaptive bitrate; hold --quality/--fps fixed")
	fs.Parse(args)

	capturePath := resolveCapture(*script)
	if _, err := os.Stat(capturePath); err != nil {
		log.Fatalf("capture.py not found at %q (use --capture to point at it)", capturePath)
	}

	h := hub.New()

	// fps gate sits between capture and the hub so the adaptive controller can
	// throttle forwarded frames without touching the GStreamer pipeline.
	gate := adaptive.NewGate(*fpsFlag)

	cap, err := capture.Start(
		capture.Options{Script: capturePath, FPS: *fpsFlag, Quality: *quality, Width: *width},
		func(frame []byte) {
			if gate.Allow() {
				h.Publish(frame)
			}
		},
		func(err error) {
			if err != nil {
				log.Fatalf("capture stopped: %v", err)
			}
			log.Fatal("capture exited")
		},
	)
	if err != nil {
		log.Fatalf("start capture: %v", err)
	}

	// Adaptive bitrate (Phase 4): once per second, nudge quality/fps toward what
	// viewers can keep up with.
	if !*noAdaptive {
		ctrl := adaptive.NewController(*quality, *minQuality, *fpsFlag)
		go adaptiveLoop(ctrl, h, cap, gate)
		log.Printf("adaptive bitrate on (quality %d..%d, fps up to %d)", *minQuality, *quality, *fpsFlag)
	}

	// Set up remote control (Phase 2). If uinput is unavailable we degrade to
	// view-only rather than refusing to start.
	var injector *input.Injector
	if *noInput {
		log.Printf("remote control disabled (--no-input): view-only")
	} else if in, err := input.New(); err != nil {
		log.Printf("remote control unavailable: %v", err)
		log.Printf("  -> serving view-only. Fix the above and restart to enable control.")
	} else {
		injector = in
		defer injector.Close()
		log.Printf("remote control enabled (mouse + keyboard via uinput)")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/stream", streamHandler(h))
	mux.HandleFunc("/input", inputHandler(injector))
	mux.HandleFunc("/", indexHandler(injector != nil))

	// Always serve locally (direct/LAN access). If a relay is configured, also
	// register with it so viewers can reach us over the internet.
	log.Printf("screenlink host: local viewer at http://localhost%s  (capture=%s)", normalizeAddr(*addr), capturePath)
	if *relayAddr != "" {
		go func() { log.Fatal(http.ListenAndServe(*addr, mux)) }()
		log.Fatal(relay.ServeHTTP(*relayAddr, *pin, mux))
	}
	log.Fatal(http.ListenAndServe(*addr, mux))
}

// runConnect is the viewer side: a local proxy that tunnels the browser to a
// remote host through the relay (Phase 3).
func runConnect(args []string) {
	fs := flag.NewFlagSet("connect", flag.ExitOnError)
	relayAddr := fs.String("relay", "", "relay to reach the host through (HOST:PORT)")
	pin := fs.String("pin", "", "passcode shown by the host")
	addr := fs.String("addr", ":8087", "local address to open the viewer on")
	fs.Parse(args)

	if *relayAddr == "" || fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: screenlink connect --relay HOST:PORT [--pin CODE] [--addr :8087] <address>")
		os.Exit(2)
	}
	if err := relay.Connect(*relayAddr, fs.Arg(0), *pin, *addr); err != nil {
		log.Fatalf("connect: %v", err)
	}
}

// runRelay runs the public-IP middleman (Phase 3). Run this on a server with a
// reachable address; hosts and viewers both dial out to it.
func runRelay(args []string) {
	fs := flag.NewFlagSet("relay", flag.ExitOnError)
	addr := fs.String("addr", ":9000", "address to listen on")
	fs.Parse(args)
	log.Fatal(relay.RunRelay(*addr))
}

func indexHandler(controlEnabled bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		data, err := webFS.ReadFile("web/index.html")
		if err != nil {
			http.Error(w, "viewer missing", http.StatusInternalServerError)
			return
		}
		// Tell the viewer whether control is live (replaces a placeholder).
		page := strings.Replace(string(data), "__CONTROL__", boolJS(controlEnabled), 1)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, page)
	}
}

func boolJS(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// inputEvent is one control event from the browser. Compact field names keep
// the per-frame mouse-move messages small.
//
//	{"t":"m","x":0.5,"y":0.3}   absolute move (fractions of the screen)
//	{"t":"b","b":0,"d":true}    button 0=left/1=right/2=middle, d=down
//	{"t":"s","x":0,"y":-1}      scroll wheel notches (x=horiz, y=vert)
//	{"t":"k","c":"KeyA","d":true} key by browser KeyboardEvent.code
type inputEvent struct {
	T string  `json:"t"`
	X float64 `json:"x"`
	Y float64 `json:"y"`
	B int     `json:"b"`
	C string  `json:"c"`
	D bool    `json:"d"`
}

// inputHandler upgrades to WebSocket and replays each event via the injector.
// With control disabled (injector nil) it accepts and drains the socket so the
// viewer's UI behaves consistently, but performs no injection.
func inputHandler(in *input.Injector) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := wsock.Upgrade(w, r)
		if err != nil {
			http.Error(w, "websocket upgrade failed", http.StatusBadRequest)
			return
		}
		defer conn.Close()

		for {
			msg, err := conn.ReadMessage()
			if err != nil {
				return // EOF / closed / read error
			}
			if in == nil {
				continue
			}
			var ev inputEvent
			if json.Unmarshal(msg, &ev) != nil {
				continue
			}
			dispatch(in, ev)
		}
	}
}

func dispatch(in *input.Injector, ev inputEvent) {
	switch ev.T {
	case "m":
		in.MoveTo(ev.X, ev.Y)
	case "b":
		in.Button(ev.B, ev.D)
	case "s":
		in.Scroll(int(ev.X), int(ev.Y))
	case "k":
		in.Key(ev.C, ev.D)
	}
}

// adaptiveLoop tunes encoder quality + forwarded fps once a second based on how
// well viewers are keeping up (Phase 4).
func adaptiveLoop(ctrl *adaptive.Controller, h *hub.Hub, cap *capture.Capturer, gate *adaptive.Gate) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	var prev hub.Stats
	for range t.C {
		s := h.Stats()
		dd, dr := diff(s.Delivered, prev.Delivered), diff(s.Dropped, prev.Dropped)
		prev = s
		if s.Subscribers == 0 {
			continue // nobody watching; leave settings as-is
		}
		if lv, changed := ctrl.Tune(dd, dr); changed {
			cap.SetQuality(lv.Quality)
			gate.SetTarget(lv.FPS)
			log.Printf("adaptive: quality=%d fps=%d (delivered=%d dropped=%d)", lv.Quality, lv.FPS, dd, dr)
		}
	}
}

// diff returns now-prev, or 0 if the counter fell (a viewer left mid-interval).
func diff(now, prev uint64) uint64 {
	if now >= prev {
		return now - prev
	}
	return 0
}

// streamHandler serves an endless multipart/x-mixed-replace JPEG stream that an
// <img> tag renders as live video.
func streamHandler(h *hub.Hub) http.HandlerFunc {
	const boundary = "screenlinkframe"
	return func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary="+boundary)
		w.Header().Set("Cache-Control", "no-store")

		sub := h.Subscribe()
		defer h.Unsubscribe(sub)

		for {
			select {
			case <-r.Context().Done():
				return
			case frame := <-sub.C:
				_, err := fmt.Fprintf(w, "--%s\r\nContent-Type: image/jpeg\r\nContent-Length: %d\r\n\r\n", boundary, len(frame))
				if err != nil {
					return
				}
				if _, err := w.Write(frame); err != nil {
					return
				}
				if _, err := w.Write([]byte("\r\n")); err != nil {
					return
				}
				flusher.Flush()
			}
		}
	}
}

// resolveCapture finds capture.py: explicit flag, then next to the binary, then cwd.
func resolveCapture(flagVal string) string {
	if flagVal != "" {
		return flagVal
	}
	if exe, err := os.Executable(); err == nil {
		cand := filepath.Join(filepath.Dir(exe), "capture.py")
		if _, err := os.Stat(cand); err == nil {
			return cand
		}
	}
	return "capture.py"
}

func normalizeAddr(addr string) string {
	if addr != "" && addr[0] == ':' {
		return addr
	}
	return ":" + addr
}
