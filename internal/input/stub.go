//go:build !linux

// On non-Linux platforms there is no uinput, so remote control is unavailable.
// This stub lets the rest of screenlink (the viewer-side `connect` proxy and the
// `relay`) build and run on Windows/macOS — only the Linux-only `host` capture +
// control path is absent. New() returns a clear error; the host then serves
// view-only (and on these platforms screen capture itself is unsupported too).
package input

import "errors"

// Injector is a no-op on platforms without uinput.
type Injector struct{}

// New always fails off Linux: there is no uinput device to inject through.
func New() (*Injector, error) {
	return nil, errors.New("remote control (uinput) is only available on Linux")
}

func (*Injector) MoveTo(fx, fy float64) error { return nil }
func (*Injector) Button(btn int, down bool) error { return nil }
func (*Injector) Scroll(dx, dy int) error { return nil }
func (*Injector) Key(code string, down bool) error { return nil }
func (*Injector) Close() error { return nil }
