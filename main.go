// screenlink — a from-scratch remote desktop (AnyDesk-style) for Wayland.
//
// Phase 1 (this build): the host captures the screen via the PipeWire portal and
// serves it to a browser as a live MJPEG stream. View-only. Mouse/keyboard
// control and relay-over-internet come next.
//
// Usage:
//
//	screenlink host [--addr :8080] [--fps 12] [--quality 70] [--width 0]
//
// Then open http://localhost:8080 in a browser.
package main

import (
	"embed"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"screenlink/internal/capture"
	"screenlink/internal/hub"
)

//go:embed web/index.html
var webFS embed.FS

func main() {
	if len(os.Args) < 2 || os.Args[1] != "host" {
		fmt.Fprintln(os.Stderr, "usage: screenlink host [--addr :8080] [--fps 12] [--quality 70] [--width 0]")
		os.Exit(2)
	}

	fs := flag.NewFlagSet("host", flag.ExitOnError)
	addr := fs.String("addr", ":8080", "address to serve the viewer on")
	fpsFlag := fs.Int("fps", 40, "capture frames per second")
	quality := fs.Int("quality", 70, "JPEG quality 1-100")
	width := fs.Int("width", 0, "scale to this width keeping aspect (0 = native); e.g. 1280 = 720p")
	script := fs.String("capture", "", "path to capture.py (default: next to the binary, then ./capture.py)")
	fs.Parse(os.Args[2:])

	capturePath := resolveCapture(*script)
	if _, err := os.Stat(capturePath); err != nil {
		log.Fatalf("capture.py not found at %q (use --capture to point at it)", capturePath)
	}

	h := hub.New()

	// Start capturing in the background; frames flow into the hub.
	go func() {
		err := capture.Frames(capture.Options{
			Script:  capturePath,
			FPS:     *fpsFlag,
			Quality: *quality,
			Width:   *width,
		}, h.Publish)
		if err != nil {
			log.Fatalf("capture stopped: %v", err)
		}
		log.Fatal("capture exited")
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("/stream", streamHandler(h))
	mux.HandleFunc("/", indexHandler)

	log.Printf("screenlink host: open http://localhost%s  (capture=%s)", normalizeAddr(*addr), capturePath)
	log.Fatal(http.ListenAndServe(*addr, mux))
}

func indexHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	data, err := webFS.ReadFile("web/index.html")
	if err != nil {
		http.Error(w, "viewer missing", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(data)
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

		ch := h.Subscribe()
		defer h.Unsubscribe(ch)

		for {
			select {
			case <-r.Context().Done():
				return
			case frame := <-ch:
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
