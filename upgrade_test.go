package cooper

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
)

func TestUpgrade_BasicRoundTrip(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		raw, err := ln.Accept()
		if err != nil {
			return
		}
		// act as a simple HTTP server that does the upgrade manually
		buf := make([]byte, 4096)
		n, _ := raw.Read(buf)
		_ = n // consume the request

		resp := "HTTP/1.1 101 Switching Protocols\r\n" +
			"Connection: Upgrade\r\n" +
			"Upgrade: test/1\r\n" +
			"\r\n"
		raw.Write([]byte(resp))

		// echo back whatever the client sends
		io.Copy(raw, raw)
		raw.Close()
	}()

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	req, _ := http.NewRequest("GET", "http://"+ln.Addr().String()+"/", nil)
	req.Header.Set("Upgrade", "test/1")

	upgraded, err := Upgrade(conn, req)
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	defer upgraded.Close()

	msg := "ping"
	fmt.Fprint(upgraded, msg)

	buf := make([]byte, len(msg))
	if _, err := io.ReadFull(upgraded, buf); err != nil {
		t.Fatalf("read: %v", err)
	}

	if string(buf) != msg {
		t.Fatalf("expected %q, got %q", msg, string(buf))
	}
}

func TestUpgrade_MissingUpgradeHeader(t *testing.T) {
	conn := &fakeConn{}

	req, _ := http.NewRequest("GET", "http://localhost/", nil)
	// no Upgrade header set

	_, err := Upgrade(conn, req)
	if err == nil {
		t.Fatal("expected error for missing Upgrade header")
	}

	if !errors.Is(err, ErrMissingUpgradeHeader) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestUpgrade_SetsConnectionHeaderIfMissing(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	var receivedConnection string
	done := make(chan struct{})
	go func() {
		defer close(done)
		raw, err := ln.Accept()
		if err != nil {
			return
		}
		defer raw.Close()

		// parse the incoming request to check the Connection header
		buf := make([]byte, 4096)
		n, _ := raw.Read(buf)
		for _, line := range strings.Split(string(buf[:n]), "\r\n") {
			if strings.HasPrefix(strings.ToLower(line), "connection:") {
				receivedConnection = strings.TrimSpace(line[len("connection:"):])
			}
		}

		resp := "HTTP/1.1 101 Switching Protocols\r\n" +
			"Connection: Upgrade\r\n" +
			"Upgrade: test/1\r\n" +
			"\r\n"
		raw.Write([]byte(resp))
	}()

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	req, _ := http.NewRequest("GET", "http://"+ln.Addr().String()+"/", nil)
	req.Header.Set("Upgrade", "test/1")
	// deliberately NOT setting Connection header

	upgraded, err := Upgrade(conn, req)
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	upgraded.Close()
	<-done

	if !strings.EqualFold(receivedConnection, "upgrade") {
		t.Fatalf("expected Connection: upgrade, got %q", receivedConnection)
	}
}

func TestUpgrade_Non101StatusReturnsError(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	go func() {
		raw, err := ln.Accept()
		if err != nil {
			return
		}
		defer raw.Close()

		buf := make([]byte, 4096)
		raw.Read(buf)

		resp := "HTTP/1.1 400 Bad Request\r\n" +
			"Content-Length: 0\r\n" +
			"\r\n"
		raw.Write([]byte(resp))
	}()

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	req, _ := http.NewRequest("GET", "http://"+ln.Addr().String()+"/", nil)
	req.Header.Set("Upgrade", "test/1")

	_, err = Upgrade(conn, req)
	if err == nil {
		t.Fatal("expected error for non-101 response")
	}

	if !errors.Is(err, ErrUnexpectedStatus) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestUpgrade_MismatchedUpgradeHeaderReturnsError(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	go func() {
		raw, err := ln.Accept()
		if err != nil {
			return
		}
		defer raw.Close()

		buf := make([]byte, 4096)
		raw.Read(buf)

		resp := "HTTP/1.1 101 Switching Protocols\r\n" +
			"Connection: Upgrade\r\n" +
			"Upgrade: wrong/1\r\n" +
			"\r\n"
		raw.Write([]byte(resp))
	}()

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	req, _ := http.NewRequest("GET", "http://"+ln.Addr().String()+"/", nil)
	req.Header.Set("Upgrade", "test/1")

	_, err = Upgrade(conn, req)
	if err == nil {
		t.Fatal("expected error for mismatched Upgrade header")
	}

	if !errors.Is(err, ErrProtocolMismatch) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestUpgrade_MissingConnectionUpgradeInResponse(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	go func() {
		raw, err := ln.Accept()
		if err != nil {
			return
		}
		defer raw.Close()

		buf := make([]byte, 4096)
		raw.Read(buf)

		resp := "HTTP/1.1 101 Switching Protocols\r\n" +
			"Upgrade: test/1\r\n" +
			"\r\n"
		raw.Write([]byte(resp))
	}()

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	req, _ := http.NewRequest("GET", "http://"+ln.Addr().String()+"/", nil)
	req.Header.Set("Upgrade", "test/1")

	_, err = Upgrade(conn, req)
	if err == nil {
		t.Fatal("expected error for missing Connection: Upgrade in response")
	}

	if !errors.Is(err, ErrMissingConnectionHeader) {
		t.Fatalf("unexpected error: %v", err)
	}
}

// fakeConn is a minimal net.Conn used where we only need to test pre-send
// validation and never actually write to a network.
type fakeConn struct{ net.Conn }

func TestUpgrade_ResponseValidatorCalledWithResponse(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	go func() {
		raw, err := ln.Accept()
		if err != nil {
			return
		}
		defer raw.Close()
		buf := make([]byte, 4096)
		raw.Read(buf)
		raw.Write([]byte("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: test/1\r\nX-Token: secret\r\n\r\n"))
	}()

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	var gotToken string
	req, _ := http.NewRequest("GET", "http://"+ln.Addr().String()+"/", nil)
	req.Header.Set("Upgrade", "test/1")

	upgraded, err := Upgrade(conn, req, ResponseValidator(func(req *http.Request, resp *http.Response) error {
		gotToken = resp.Header.Get("X-Token")
		return nil
	}))
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	upgraded.Close()

	if gotToken != "secret" {
		t.Fatalf("expected X-Token %q, got %q", "secret", gotToken)
	}
}

func TestUpgrade_ResponseValidatorRejection(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	go func() {
		raw, err := ln.Accept()
		if err != nil {
			return
		}
		defer raw.Close()
		buf := make([]byte, 4096)
		raw.Read(buf)
		raw.Write([]byte("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: test/1\r\n\r\n"))
	}()

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	req, _ := http.NewRequest("GET", "http://"+ln.Addr().String()+"/", nil)
	req.Header.Set("Upgrade", "test/1")

	_, err = Upgrade(conn, req, ResponseValidator(func(req *http.Request, resp *http.Response) error {
		return fmt.Errorf("bad accept header")
	}))
	if err == nil {
		t.Fatal("expected error from validator")
	}
	if !errors.Is(err, ErrResponseValidator) {
		t.Fatalf("unexpected error: %v", err)
	}
}
