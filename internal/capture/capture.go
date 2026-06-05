// Package capture runs the Python capture helper and reads its JPEG frame
// stream. Capturing a Wayland desktop must go through the xdg-desktop-portal +
// PipeWire stack, which is far easier from Python's gi bindings than from Go,
// so the OS-specific capture lives in capture.py and Go just consumes its
// length-prefixed frame stream.
package capture

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
)

// Options configure the capture helper.
type Options struct {
	Script  string // path to capture.py
	FPS     int
	Quality int
	Width   int // 0 = native resolution
}

// Frames spawns capture.py and calls onFrame for every JPEG frame until the
// helper exits or an error occurs. It blocks; run it in a goroutine.
func Frames(opts Options, onFrame func([]byte)) error {
	cmd := exec.Command("python3", opts.Script)
	cmd.Stderr = os.Stderr // let the helper's [capture] logs through
	cmd.Env = append(os.Environ(),
		"SCREENLINK_FPS="+strconv.Itoa(opts.FPS),
		"SCREENLINK_QUALITY="+strconv.Itoa(opts.Quality),
		"SCREENLINK_WIDTH="+strconv.Itoa(opts.Width),
	)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start capture.py: %w", err)
	}

	readErr := readFrames(stdout, onFrame)
	_ = cmd.Process.Kill()
	cmd.Wait()
	return readErr
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
