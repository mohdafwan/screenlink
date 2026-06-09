// Package capture runs the Python capture helper and reads its JPEG frame
// stream. Capturing a Wayland desktop must go through the xdg-desktop-portal +
// PipeWire stack, which is far easier from Python's gi bindings than from Go,
// so the OS-specific capture lives in capture.py and Go just consumes its
// length-prefixed frame stream.
//
// The host can also retune the encoder at runtime (Phase 4, adaptive bitrate)
// by sending one-line commands to capture.py's stdin via SetQuality.
package capture

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"sync"
)

// Options configure the capture helper.
type Options struct {
	Script  string // path to capture.py
	FPS     int
	Quality int
	Width   int    // 0 = native resolution
	Codec   string // "mjpeg" or "h264"
	Bitrate int    // h264 target bitrate in kbps (CBR)
}

// Capturer is a running capture.py process.
type Capturer struct {
	cmd   *exec.Cmd
	mu    sync.Mutex
	stdin io.WriteCloser
}

// Start launches capture.py and streams every JPEG frame to onFrame from a
// background goroutine. It returns once the process has started; when capture
// ends (EOF or error) it calls onDone with the cause. Run it for the lifetime
// of the host.
func Start(opts Options, onFrame func([]byte), onDone func(error)) (*Capturer, error) {
	cmd := exec.Command("python3", opts.Script)
	cmd.Stderr = os.Stderr // let the helper's [capture] logs through
	codec := opts.Codec
	if codec == "" {
		codec = "mjpeg"
	}
	cmd.Env = append(os.Environ(),
		"SCREENLINK_FPS="+strconv.Itoa(opts.FPS),
		"SCREENLINK_QUALITY="+strconv.Itoa(opts.Quality),
		"SCREENLINK_WIDTH="+strconv.Itoa(opts.Width),
		"SCREENLINK_CODEC="+codec,
		"SCREENLINK_BITRATE="+strconv.Itoa(opts.Bitrate),
	)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start capture.py: %w", err)
	}

	c := &Capturer{cmd: cmd, stdin: stdin}
	go func() {
		readErr := readFrames(stdout, onFrame)
		_ = cmd.Process.Kill()
		cmd.Wait()
		onDone(readErr)
	}()
	return c, nil
}

// SetQuality asks the encoder to switch to JPEG quality q (1-100) on the fly.
func (c *Capturer) SetQuality(q int) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, err := fmt.Fprintf(c.stdin, "QUALITY %d\n", q)
	return err
}

// readFrames parses the [4-byte length][jpeg] stream.
func readFrames(r io.Reader, onFrame func([]byte)) error {
	var hdr [4]byte
	for {
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				return nil
			}
			return err
		}
		n := binary.BigEndian.Uint32(hdr[:])
		if n == 0 || n > 64<<20 { // sanity cap: 64 MB
			return fmt.Errorf("capture: bad frame length %d", n)
		}
		frame := make([]byte, n)
		if _, err := io.ReadFull(r, frame); err != nil {
			return err
		}
		onFrame(frame)
	}
}
