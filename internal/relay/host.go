package relay

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
)

// ServeHTTP registers this machine with the relay, prints its AnyDesk-style
// address + passcode, and serves handler (screenlink's HTTP mux) over the relay
// — one bridged connection per browser connection. It blocks until the relay
// link or the local HTTP server fails.
//
// Each incoming connection must present "PIN <passcode>" before any HTTP is
// served; this keeps random ID-guessers out. If pin is empty a 6-digit one is
// generated. NOTE: traffic over the relay is still plaintext — the passcode
// stops casual access but a malicious relay can see everything; TLS is future.
func ServeHTTP(relayAddr, pin string, handler http.Handler) error {
	if pin == "" {
		pin = randPIN()
	}

	ctrl, err := net.Dial("tcp", relayAddr)
	if err != nil {
		return fmt.Errorf("dial relay %s: %w", relayAddr, err)
	}
	defer ctrl.Close()

	if err := WriteLine(ctrl, "HOST"); err != nil {
		return err
	}
	line, err := ReadLine(ctrl, 256)
	if err != nil {
		return fmt.Errorf("relay handshake: %w", err)
	}
	id, ok := strings.CutPrefix(line, "ID ")
	if !ok {
		return fmt.Errorf("relay: unexpected reply %q", line)
	}

	fmt.Println()
	fmt.Println("  Your screenlink Address")
	fmt.Println("  ───────────────────────")
	fmt.Printf("    Address : %s\n", formatID(id))
	fmt.Printf("    Passcode: %s\n", pin)
	fmt.Println()
	fmt.Println("  Connect from another machine with:")
	fmt.Printf("    screenlink connect --relay %s --pin %s %s\n", relayAddr, pin, id)
	fmt.Println()
	fmt.Println("  Waiting for viewers… (Ctrl-C to stop)")

	ln := newRelayListener()

	// Control loop: the relay pushes "DIAL <token>" per incoming viewer; we dial
	// a fresh data connection for each, authenticate it, and feed it to the HTTP
	// server. Runs until the control connection drops.
	go func() {
		for {
			l, err := ReadLine(ctrl, 256)
			if err != nil {
				log.Printf("relay control closed: %v", err)
				ln.Close()
				return
			}
			f := strings.Fields(l)
			if len(f) == 2 && f[0] == "DIAL" {
				go dialAndAuth(relayAddr, f[1], pin, ln)
			}
		}
	}()

	// http.Serve consumes the authenticated connections as if they were normal
	// accepted sockets — the existing handlers (/, /stream, /input) just work.
	return http.Serve(ln, handler)
}

// dialAndAuth opens one data connection for a viewer, checks its passcode, and
// (on success) hands it to the HTTP listener.
func dialAndAuth(relayAddr, token, pin string, ln *relayListener) {
	data, err := net.Dial("tcp", relayAddr)
	if err != nil {
		log.Printf("relay data dial failed: %v", err)
		return
	}
	if err := WriteLine(data, "HOSTDATA "+token); err != nil {
		data.Close()
		return
	}
	if resp, err := ReadLine(data, 256); err != nil || resp != "OK" {
		data.Close()
		return
	}
	// First line from the viewer side must be the passcode.
	l, err := ReadLine(data, 256)
	if err != nil {
		data.Close()
		return
	}
	got, ok := strings.CutPrefix(l, "PIN ")
	if !ok || got != pin {
		log.Printf("rejected viewer %s: bad passcode", data.RemoteAddr())
		WriteLine(data, "ERR bad passcode")
		data.Close()
		return
	}
	// Positive ack so the viewer can confirm the passcode before bridging the
	// browser (on success there is otherwise nothing to read until HTTP starts).
	if err := WriteLine(data, "OK"); err != nil {
		data.Close()
		return
	}
	ln.push(data)
}

func randPIN() string {
	var buf [8]byte
	rand.Read(buf[:])
	return fmt.Sprintf("%06d", binary.BigEndian.Uint64(buf[:])%1_000_000)
}

// relayListener adapts the stream of authenticated relay connections into a
// net.Listener so net/http can serve over them.
type relayListener struct {
	conns  chan net.Conn
	closed chan struct{}
	once   sync.Once
}

func newRelayListener() *relayListener {
	return &relayListener{conns: make(chan net.Conn), closed: make(chan struct{})}
}

func (l *relayListener) push(c net.Conn) {
	select {
	case l.conns <- c:
	case <-l.closed:
		c.Close()
	}
}

func (l *relayListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

func (l *relayListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}

func (l *relayListener) Addr() net.Addr { return relayNetAddr{} }

type relayNetAddr struct{}

func (relayNetAddr) Network() string { return "relay" }
func (relayNetAddr) String() string  { return "relay" }
