//go:build linux

// Package input injects mouse and keyboard events into the local Wayland
// session by creating a virtual device through the kernel's uinput interface.
//
// This is the host side of Phase 2 (remote control): the browser captures the
// viewer's pointer/keyboard and ships events here, and we replay them on the
// real desktop. We talk to /dev/uinput directly (no ydotool daemon) so the
// whole thing stays a single self-contained process.
//
// The virtual device is an *absolute* pointer (ABS_X/ABS_Y over a fixed 0..MAX
// range that the compositor stretches across the screen) plus mouse buttons, a
// scroll wheel, and a full keyboard. Sending absolute coordinates means the
// browser only has to report *where in the frame* the cursor is as a fraction,
// with no knowledge of the real screen resolution.
//
// Requires write access to /dev/uinput (root, or membership in the `input`
// group). New returns a clear error otherwise; the host stays view-only.
package input

import (
	"encoding/binary"
	"fmt"
	"os"
	"sync"
	"syscall"
)

// --- uinput / input-event-codes.h constants (x86-64) ---

const (
	uinputPath = "/dev/uinput"

	absMax = 65535 // ABS_X/ABS_Y logical range; compositor maps 0..absMax to the screen

	evSyn = 0x00
	evKey = 0x01
	evRel = 0x02
	evAbs = 0x03

	synReport = 0x00

	relWheel  = 0x08
	relHWheel = 0x06

	absX = 0x00
	absY = 0x01

	btnLeft   = 0x110
	btnRight  = 0x111
	btnMiddle = 0x112

	// ioctl request codes (precomputed: _IO/_IOW('U', n) for x86-64).
	uiSetEvBit  = 0x40045564 // _IOW('U', 100, int)
	uiSetKeyBit = 0x40045565 // _IOW('U', 101, int)
	uiSetRelBit = 0x40045566 // _IOW('U', 102, int)
	uiSetAbsBit = 0x40045567 // _IOW('U', 103, int)
	uiDevCreate = 0x5501     // _IO('U', 1)
	uiDevDestroy = 0x5502    // _IO('U', 2)
)

// Button identifiers as sent by the browser (0=left, 1=right, 2=middle).
const (
	ButtonLeft   = 0
	ButtonRight  = 1
	ButtonMiddle = 2
)

var browserButton = map[int]uint16{
	ButtonLeft:   btnLeft,
	ButtonRight:  btnRight,
	ButtonMiddle: btnMiddle,
}

// Injector is a live virtual input device. Methods are safe for concurrent use.
type Injector struct {
	mu sync.Mutex
	f  *os.File
}

// New opens /dev/uinput and registers a virtual absolute-pointer + keyboard
// device. The returned Injector must be Closed to remove the device.
func New() (*Injector, error) {
	f, err := os.OpenFile(uinputPath, os.O_WRONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		if os.IsPermission(err) {
			return nil, fmt.Errorf("open %s: permission denied — add yourself to the 'input' group (see README) or run as root", uinputPath)
		}
		return nil, fmt.Errorf("open %s: %w (is the uinput kernel module loaded?)", uinputPath, err)
	}

	in := &Injector{f: f}
	if err := in.register(); err != nil {
		f.Close()
		return nil, err
	}
	return in, nil
}

func (in *Injector) register() error {
	fd := in.f.Fd()

	// Declare the event classes this device emits.
	for _, ev := range []uintptr{evKey, evRel, evAbs, evSyn} {
		if err := ioctl(fd, uiSetEvBit, ev); err != nil {
			return fmt.Errorf("UI_SET_EVBIT(%d): %w", ev, err)
		}
	}
	// Absolute axes for positioning.
	for _, a := range []uintptr{absX, absY} {
		if err := ioctl(fd, uiSetAbsBit, a); err != nil {
			return fmt.Errorf("UI_SET_ABSBIT(%d): %w", a, err)
		}
	}
	// Relative axes for scrolling.
	for _, r := range []uintptr{relWheel, relHWheel} {
		if err := ioctl(fd, uiSetRelBit, r); err != nil {
			return fmt.Errorf("UI_SET_RELBIT(%d): %w", r, err)
		}
	}
	// Mouse buttons + every keyboard key we might emit.
	keys := []uintptr{btnLeft, btnRight, btnMiddle}
	for _, code := range keycodeSet() {
		keys = append(keys, uintptr(code))
	}
	for _, k := range keys {
		if err := ioctl(fd, uiSetKeyBit, k); err != nil {
			return fmt.Errorf("UI_SET_KEYBIT(%d): %w", k, err)
		}
	}

	if err := in.writeUserDev(); err != nil {
		return err
	}
	if err := ioctl(fd, uiDevCreate, 0); err != nil {
		return fmt.Errorf("UI_DEV_CREATE: %w", err)
	}
	return nil
}

// writeUserDev sends the legacy struct uinput_user_dev describing the device,
// including the absolute-axis range. (1116 bytes on x86-64.)
func (in *Injector) writeUserDev() error {
	const (
		nameSize = 80
		absCnt   = 64
	)
	buf := make([]byte, nameSize+8+4+absCnt*4*4)
	copy(buf, "screenlink-virtual-input")

	// struct input_id { u16 bustype, vendor, product, version } at offset 80.
	off := nameSize
	binary.LittleEndian.PutUint16(buf[off:], 0x03) // BUS_USB
	binary.LittleEndian.PutUint16(buf[off+2:], 0x1234)
	binary.LittleEndian.PutUint16(buf[off+4:], 0x5678)
	binary.LittleEndian.PutUint16(buf[off+6:], 1)

	// ff_effects_max (u32) at 88, then absmax[64], absmin[64], absfuzz, absflat.
	absmaxOff := nameSize + 8 + 4
	for _, axis := range []int{absX, absY} {
		binary.LittleEndian.PutUint32(buf[absmaxOff+axis*4:], absMax)
		// absmin stays 0 (next 256-byte block); fuzz/flat stay 0.
	}

	if _, err := in.f.Write(buf); err != nil {
		return fmt.Errorf("write uinput_user_dev: %w", err)
	}
	return nil
}

// MoveTo warps the pointer to (fx, fy), each a fraction in [0,1] of the screen.
func (in *Injector) MoveTo(fx, fy float64) error {
	clamp := func(v float64) int32 {
		if v < 0 {
			v = 0
		} else if v > 1 {
			v = 1
		}
		return int32(v * absMax)
	}
	in.mu.Lock()
	defer in.mu.Unlock()
	in.emit(evAbs, absX, clamp(fx))
	in.emit(evAbs, absY, clamp(fy))
	return in.sync()
}

// Button presses (down=true) or releases a mouse button (0=left,1=right,2=mid).
func (in *Injector) Button(btn int, down bool) error {
	code, ok := browserButton[btn]
	if !ok {
		return fmt.Errorf("unknown button %d", btn)
	}
	in.mu.Lock()
	defer in.mu.Unlock()
	in.emit(evKey, code, boolToVal(down))
	return in.sync()
}

// Scroll turns wheel notches (dy: +up/-down, dx: +right/-left).
func (in *Injector) Scroll(dx, dy int) error {
	in.mu.Lock()
	defer in.mu.Unlock()
	if dy != 0 {
		in.emit(evRel, relWheel, int32(dy))
	}
	if dx != 0 {
		in.emit(evRel, relHWheel, int32(dx))
	}
	return in.sync()
}

// Key presses (down=true) or releases the key named by a browser KeyboardEvent
// .code string (e.g. "KeyA", "Enter", "ShiftLeft"). Unknown codes are ignored.
func (in *Injector) Key(code string, down bool) error {
	kc, ok := keycodeFor(code)
	if !ok {
		return nil // silently ignore keys we don't map
	}
	in.mu.Lock()
	defer in.mu.Unlock()
	in.emit(evKey, uint16(kc), boolToVal(down))
	return in.sync()
}

// emit queues one input_event (24 bytes on x86-64). Caller holds in.mu.
func (in *Injector) emit(typ, code uint16, value int32) {
	var ev [24]byte
	// struct timeval (16 bytes) left zero — the kernel timestamps for us.
	binary.LittleEndian.PutUint16(ev[16:], typ)
	binary.LittleEndian.PutUint16(ev[18:], code)
	binary.LittleEndian.PutUint32(ev[20:], uint32(value))
	in.f.Write(ev[:])
}

// sync flushes the queued events as one atomic report. Caller holds in.mu.
func (in *Injector) sync() error {
	in.emit(evSyn, synReport, 0)
	return nil
}

// Close removes the virtual device.
func (in *Injector) Close() error {
	in.mu.Lock()
	defer in.mu.Unlock()
	ioctl(in.f.Fd(), uiDevDestroy, 0)
	return in.f.Close()
}

func ioctl(fd, request, arg uintptr) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, request, arg)
	if errno != 0 {
		return errno
	}
	return nil
}

func boolToVal(b bool) int32 {
	if b {
		return 1
	}
	return 0
}
