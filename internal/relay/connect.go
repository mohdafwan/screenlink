package relay

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"strings"
)

// Connect runs the viewer side: a local TCP proxy on localAddr that tunnels the
// browser to a remote screenlink host through the relay. The browser can't
// speak the relay handshake itself, so for every connection it makes to
// localAddr we open a fresh relay link (JOIN <id>), present the passcode, and
// bridge the two. The user just opens http://localhost<localAddr>.
//
// It blocks until the local listener fails.
func Connect(relayAddr, id, pin, localAddr string) error {
	id = strings.ReplaceAll(id, " ", "") // accept "524 147 985" as typed

	ln, err := net.Listen("tcp", localAddr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", localAddr, err)
	}
	defer ln.Close()

	// Verify the host/passcode up front so the user gets a clear error instead
	// of a broken-looking browser tab.
	if err := probe(relayAddr, id, pin); err != nil {
		return err
	}

	fmt.Println()
	fmt.Printf("  Connected to %s via relay %s\n", formatID(id), relayAddr)
	fmt.Printf("  Open the remote desktop at:  http://localhost%s\n", localAddr)
	fmt.Println("  (Ctrl-C to disconnect)")
	fmt.Println()

	for {
		c, err := ln.Accept()
		if err != nil {
			return err
		}
		go proxyOne(c, relayAddr, id, pin)
	}
}

// probe opens one relay link, authenticates, and closes it — confirming the
// address is online and the passcode is right before we start proxying.
func probe(relayAddr, id, pin string) error {
	conn, err := joinAndAuth(relayAddr, id, pin)
	if err != nil {
		return err
	}
	conn.Close()
	return nil
}

// proxyOne bridges one browser connection to the host over a fresh relay link.
func proxyOne(browser net.Conn, relayAddr, id, pin string) {
	defer browser.Close()
	conn, err := joinAndAuth(relayAddr, id, pin)
	if err != nil {
		log.Printf("relay link failed: %v", err)
		return
	}
	defer conn.Close()
	// Shuttle bytes both ways until either side closes.
	done := make(chan struct{}, 2)
	go func() { io.Copy(conn, browser); done <- struct{}{} }()
	go func() { io.Copy(browser, conn); done <- struct{}{} }()
	<-done
}

// joinAndAuth dials the relay, joins the host by ID, and sends the passcode.
// The returned connection is ready to carry HTTP to/from the host.
func joinAndAuth(relayAddr, id, pin string) (net.Conn, error) {
	conn, err := net.Dial("tcp", relayAddr)
	if err != nil {
		return nil, fmt.Errorf("dial relay %s: %w", relayAddr, err)
	}
	if err := WriteLine(conn, "JOIN "+id); err != nil {
		conn.Close()
		return nil, err
	}
	resp, err := ReadLine(conn, 256)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("relay handshake: %w", err)
	}
	if resp != "OK" {
		conn.Close()
		return nil, errors.New("relay: " + resp)
	}
	// The host validates this before serving any HTTP, then acks "OK".
	if err := WriteLine(conn, "PIN "+pin); err != nil {
		conn.Close()
		return nil, err
	}
	ack, err := ReadLine(conn, 256)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("relay handshake: %w", err)
	}
	if ack != "OK" {
		conn.Close()
		return nil, errors.New("relay: " + ack) // e.g. "ERR bad passcode"
	}
	return conn, nil
}
