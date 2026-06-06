// Package relay is the public-IP middleman that makes the AnyDesk-style "type
// an ID, connect from anywhere" flow possible for screenlink — and it speaks
// the exact same tiny wire protocol as termlink's relay, so either relay binary
// works for either app.
//
// Why it exists: two devices behind home routers can't reach each other
// directly (NAT blocks incoming connections). So BOTH dial OUT to this relay,
// which has a public IP. The relay matches them by ID and pipes bytes between
// the two outbound connections. No port-forwarding needed.
//
// Wire handshake (plain-text lines, before the bridged traffic takes over):
//
//	host control conn :  C->S "HOST"             S->C "ID <number>"
//	                     (later, per client)     S->C "DIAL <token>"
//	host data conn    :  C->S "HOSTDATA <token>" S->C "OK"   then bridged
//	client conn       :  C->S "JOIN <number>"    S->C "OK"   then bridged
//	                                             S->C "ERR <reason>" on failure
package relay

import (
	"io"
	"strings"
)

// WriteLine writes s followed by '\n' — used for the relay's text handshake
// before raw byte-bridging takes over.
func WriteLine(w io.Writer, s string) error {
	_, err := w.Write([]byte(s + "\n"))
	return err
}

// ReadLine reads one '\n'-terminated line a byte at a time so it never consumes
// past the newline — critical because the same connection switches to bridged
// traffic immediately after the handshake line. max caps the length.
func ReadLine(r io.Reader, max int) (string, error) {
	var b []byte
	var tmp [1]byte
	for len(b) < max {
		n, err := r.Read(tmp[:])
		if n > 0 {
			if tmp[0] == '\n' {
				return string(b), nil
			}
			if tmp[0] != '\r' {
				b = append(b, tmp[0])
			}
		}
		if err != nil {
			return string(b), err
		}
	}
	return string(b), nil
}

// formatID groups digits in threes for readability: "524147985" -> "524 147 985".
func formatID(id string) string {
	n := len(id)
	if n <= 3 {
		return id
	}
	var parts []string
	head := n % 3
	if head > 0 {
		parts = append(parts, id[:head])
	}
	for i := head; i < n; i += 3 {
		parts = append(parts, id[i:i+3])
	}
	return strings.Join(parts, " ")
}
