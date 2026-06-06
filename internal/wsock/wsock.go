// Package wsock is a tiny, dependency-free WebSocket server — just enough of
// RFC 6455 to receive the browser's input events.
//
// screenlink stays a from-scratch project (no third-party Go modules), so
// rather than pull in gorilla/websocket we implement the handshake and frame
// codec here. We only need to *read* small text messages from the client and
// occasionally answer a ping, so the surface is deliberately minimal:
// single-frame messages, the three data/control opcodes we care about.
package wsock

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
)

// magic GUID from RFC 6455 §1.3 used to derive the accept key.
const magic = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// Conn is an upgraded WebSocket connection.
type Conn struct {
	conn net.Conn
	br   *bufio.Reader
}

// opcodes
const (
	opText   = 0x1
	opBinary = 0x2
	opClose  = 0x8
	opPing   = 0x9
	opPong   = 0xA
)

// Upgrade performs the server handshake and hijacks the TCP connection. After
// it returns, the caller owns the Conn and must Close it.
func Upgrade(w http.ResponseWriter, r *http.Request) (*Conn, error) {
	if !strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade") ||
		!strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		return nil, fmt.Errorf("not a websocket upgrade request")
	}
	key := r.Header.Get("Sec-WebSocket-Key")
	if key == "" {
		return nil, fmt.Errorf("missing Sec-WebSocket-Key")
	}

	hj, ok := w.(http.Hijacker)
	if !ok {
		return nil, fmt.Errorf("hijacking not supported")
	}
	conn, brw, err := hj.Hijack()
	if err != nil {
		return nil, err
	}

	sum := sha1.Sum([]byte(key + magic))
	accept := base64.StdEncoding.EncodeToString(sum[:])
	resp := "HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + accept + "\r\n\r\n"
	if _, err := io.WriteString(conn, resp); err != nil {
		conn.Close()
		return nil, err
	}
	return &Conn{conn: conn, br: brw.Reader}, nil
}

// ReadMessage returns the next text/binary message payload. Ping frames are
// answered transparently; a close frame (or EOF) yields io.EOF.
func (c *Conn) ReadMessage() ([]byte, error) {
	for {
		op, payload, err := c.readFrame()
		if err != nil {
			return nil, err
		}
		switch op {
		case opText, opBinary:
			return payload, nil
		case opPing:
			_ = c.writeFrame(opPong, payload)
		case opPong:
			// ignore
		case opClose:
			return nil, io.EOF
		}
	}
}

// readFrame reads one (assumed unfragmented) frame and returns its opcode and
// unmasked payload. Client→server frames are always masked per RFC 6455.
func (c *Conn) readFrame() (byte, []byte, error) {
	var hdr [2]byte
	if _, err := io.ReadFull(c.br, hdr[:]); err != nil {
		return 0, nil, err
	}
	op := hdr[0] & 0x0f
	masked := hdr[1]&0x80 != 0
	n := uint64(hdr[1] & 0x7f)

	switch n {
	case 126:
		var ext [2]byte
		if _, err := io.ReadFull(c.br, ext[:]); err != nil {
			return 0, nil, err
		}
		n = uint64(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err := io.ReadFull(c.br, ext[:]); err != nil {
			return 0, nil, err
		}
		n = binary.BigEndian.Uint64(ext[:])
	}
	if n > 1<<20 { // 1 MB cap — input events are tiny
		return 0, nil, fmt.Errorf("frame too large: %d", n)
	}

	var mask [4]byte
	if masked {
		if _, err := io.ReadFull(c.br, mask[:]); err != nil {
			return 0, nil, err
		}
	}
	payload := make([]byte, n)
	if _, err := io.ReadFull(c.br, payload); err != nil {
		return 0, nil, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i&3]
		}
	}
	return op, payload, nil
}

// writeFrame writes a single unmasked server frame.
func (c *Conn) writeFrame(op byte, payload []byte) error {
	var hdr []byte
	b0 := byte(0x80 | op) // FIN + opcode
	n := len(payload)
	switch {
	case n < 126:
		hdr = []byte{b0, byte(n)}
	case n < 1<<16:
		hdr = []byte{b0, 126, byte(n >> 8), byte(n)}
	default:
		hdr = make([]byte, 10)
		hdr[0], hdr[1] = b0, 127
		binary.BigEndian.PutUint64(hdr[2:], uint64(n))
	}
	if _, err := c.conn.Write(hdr); err != nil {
		return err
	}
	_, err := c.conn.Write(payload)
	return err
}

// Close tears down the connection.
func (c *Conn) Close() error { return c.conn.Close() }
