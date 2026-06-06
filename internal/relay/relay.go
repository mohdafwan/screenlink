package relay

import (
	"crypto/rand"
	"encoding/binary"
	"io"
	"log"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	maxLine    = 256
	dialWait   = 20 * time.Second // how long a client waits for the host to dial back
	idLow      = 100_000_000      // 9-digit IDs, AnyDesk-style
	idHighSpan = 900_000_000
)

// hostConn is a registered host's control connection.
type hostConn struct {
	id   string
	conn net.Conn
	mu   sync.Mutex // serializes DIAL writes from concurrent client goroutines
}

func (h *hostConn) sendDial(token string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return WriteLine(h.conn, "DIAL "+token)
}

// pending is a client waiting for its host to dial back with the matching token.
type pending struct {
	client net.Conn
	ready  chan net.Conn // receives the host's data connection
}

// Server is the relay. Safe for concurrent use.
type Server struct {
	mu       sync.Mutex
	hosts    map[string]*hostConn // id -> host control conn
	pendings map[string]*pending  // token -> waiting client
	tokenSeq atomic.Int64
}

// NewServer creates an empty relay.
func NewServer() *Server {
	return &Server{
		hosts:    make(map[string]*hostConn),
		pendings: make(map[string]*pending),
	}
}

// RunRelay listens on addr and serves until the listener errors.
func RunRelay(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	log.Printf("screenlink relay listening on %s", addr)
	return NewServer().Serve(ln)
}

// Serve accepts connections on ln. Exposed so tests can supply their own.
func (s *Server) Serve(ln net.Listener) error {
	defer ln.Close()
	for {
		conn, err := ln.Accept()
		if err != nil {
			return err
		}
		go s.handle(conn)
	}
}

func (s *Server) handle(conn net.Conn) {
	line, err := ReadLine(conn, maxLine)
	if err != nil {
		conn.Close()
		return
	}
	fields := strings.Fields(line)
	if len(fields) == 0 {
		conn.Close()
		return
	}
	switch fields[0] {
	case "HOST":
		s.handleHost(conn)
	case "HOSTDATA":
		if len(fields) < 2 {
			conn.Close()
			return
		}
		s.handleHostData(conn, fields[1])
	case "JOIN":
		if len(fields) < 2 {
			conn.Close()
			return
		}
		s.handleJoin(conn, fields[1])
	default:
		conn.Close()
	}
}

// handleHost registers a host, sends it an ID, then keeps the control
// connection open until the host disconnects.
func (s *Server) handleHost(conn net.Conn) {
	h := s.register(conn)
	if err := WriteLine(conn, "ID "+h.id); err != nil {
		s.unregister(h.id)
		conn.Close()
		return
	}
	log.Printf("host registered: id=%s from %s", h.id, conn.RemoteAddr())

	// The host sends nothing more on the control channel; reading blocks until
	// it disconnects, which is our cue to unregister.
	buf := make([]byte, 1)
	for {
		if _, err := conn.Read(buf); err != nil {
			break
		}
	}
	s.unregister(h.id)
	conn.Close()
	log.Printf("host gone: id=%s", h.id)
}

// handleHostData pairs a host's freshly-dialed data connection with the client
// that triggered it (by token), and hands it off to that client's goroutine.
func (s *Server) handleHostData(conn net.Conn, token string) {
	p := s.takePending(token)
	if p == nil {
		conn.Close() // client gave up or token unknown
		return
	}
	p.ready <- conn // handleJoin owns the conn from here (it bridges & closes)
}

// handleJoin looks up the requested ID, asks that host to dial back, waits for
// the host's data connection, then bridges the two.
func (s *Server) handleJoin(conn net.Conn, id string) {
	h := s.lookup(id)
	if h == nil {
		WriteLine(conn, "ERR no host with that address online")
		conn.Close()
		return
	}

	token := strconv.FormatInt(s.tokenSeq.Add(1), 10)
	p := &pending{client: conn, ready: make(chan net.Conn, 1)}
	s.addPending(token, p)

	if err := h.sendDial(token); err != nil {
		s.takePending(token)
		WriteLine(conn, "ERR host unreachable")
		conn.Close()
		return
	}

	select {
	case hostData := <-p.ready:
		WriteLine(conn, "OK")
		WriteLine(hostData, "OK")
		log.Printf("bridging client %s <-> host id=%s", conn.RemoteAddr(), id)
		bridge(conn, hostData)
		log.Printf("session ended: id=%s", id)
	case <-time.After(dialWait):
		s.takePending(token)
		WriteLine(conn, "ERR timed out waiting for host")
		conn.Close()
	}
}

// bridge copies bytes in both directions until either side closes.
func bridge(a, b net.Conn) {
	done := make(chan struct{}, 2)
	go func() { io.Copy(a, b); done <- struct{}{} }()
	go func() { io.Copy(b, a); done <- struct{}{} }()
	<-done
	a.Close()
	b.Close()
	<-done
}

// --- map helpers (all guarded by s.mu) ---

func (s *Server) register(conn net.Conn) *hostConn {
	s.mu.Lock()
	defer s.mu.Unlock()
	var id string
	for {
		id = randID()
		if _, exists := s.hosts[id]; !exists {
			break
		}
	}
	h := &hostConn{id: id, conn: conn}
	s.hosts[id] = h
	return h
}

func (s *Server) unregister(id string) {
	s.mu.Lock()
	delete(s.hosts, id)
	s.mu.Unlock()
}

func (s *Server) lookup(id string) *hostConn {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hosts[id]
}

func (s *Server) addPending(token string, p *pending) {
	s.mu.Lock()
	s.pendings[token] = p
	s.mu.Unlock()
}

func (s *Server) takePending(token string) *pending {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.pendings[token]
	delete(s.pendings, token)
	return p
}

// randID returns a random 9-digit numeric ID.
func randID() string {
	var buf [8]byte
	rand.Read(buf[:])
	n := binary.BigEndian.Uint64(buf[:])%idHighSpan + idLow
	return strconv.FormatUint(n, 10)
}
