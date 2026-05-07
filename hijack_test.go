package cooper

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func dialAndUpgrade(t *testing.T, addr, proto string) net.Conn {
	t.Helper()

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	req := "GET / HTTP/1.1\r\n" +
		"Host: localhost\r\n" +
		"Connection: Upgrade\r\n" +
		"Upgrade: " + proto + "\r\n" +
		"\r\n"

	if _, err := conn.Write([]byte(req)); err != nil {
		conn.Close()
		t.Fatalf("write request: %v", err)
	}

	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		conn.Close()
		t.Fatalf("read response: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusSwitchingProtocols {
		conn.Close()
		t.Fatalf("expected 101, got %d", resp.StatusCode)
	}

	// drain any buffered bytes back into the conn
	if n := br.Buffered(); n > 0 {
		peeked, _ := br.Peek(n)
		return &prefixConn{
			Reader: io.MultiReader(strings.NewReader(string(peeked)), conn),
			Conn:   conn,
		}
	}

	return conn
}

func TestHijack_BasicEcho(t *testing.T) {
	srv := httptest.NewServer(Hijack(func(conn net.Conn, proto string) {
		defer conn.Close()
		io.Copy(conn, conn)
	}))
	defer srv.Close()

	addr := strings.TrimPrefix(srv.URL, "http://")
	conn := dialAndUpgrade(t, addr, "test/1")
	defer conn.Close()

	msg := "hello cooper"
	fmt.Fprint(conn, msg)

	buf := make([]byte, len(msg))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read echo: %v", err)
	}

	if string(buf) != msg {
		t.Fatalf("expected %q, got %q", msg, string(buf))
	}
}

func TestHijack_ProtoPassedToHandler(t *testing.T) {
	var got string
	var wg sync.WaitGroup
	wg.Add(1)

	srv := httptest.NewServer(Hijack(func(conn net.Conn, proto string) {
		defer conn.Close()
		got = proto
		wg.Done()
	}))
	defer srv.Close()

	addr := strings.TrimPrefix(srv.URL, "http://")
	conn := dialAndUpgrade(t, addr, "myproto/1")
	conn.Close()

	wg.Wait()

	if got != "myproto/1" {
		t.Fatalf("expected proto %q, got %q", "myproto/1", got)
	}
}

func TestHijack_MissingUpgradeHeader(t *testing.T) {
	srv := httptest.NewServer(Hijack(func(conn net.Conn, proto string) {
		conn.Close()
	}))
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestHijack_MissingConnectionUpgradeHeader(t *testing.T) {
	srv := httptest.NewServer(Hijack(func(conn net.Conn, proto string) {
		conn.Close()
	}))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL, nil)
	req.Header.Set("Upgrade", "test/1")
	// deliberately not setting Connection: Upgrade

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestHijack_UnsupportedProto(t *testing.T) {
	srv := httptest.NewServer(Hijack(func(conn net.Conn, proto string) {
		conn.Close()
	}, Protocols("allowed/1")))
	defer srv.Close()

	conn, err := net.Dial("tcp", strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	req := "GET / HTTP/1.1\r\n" +
		"Host: localhost\r\n" +
		"Connection: Upgrade\r\n" +
		"Upgrade: denied/1\r\n" +
		"\r\n"
	conn.Write([]byte(req))

	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusUpgradeRequired {
		t.Fatalf("expected 426, got %d", resp.StatusCode)
	}

	if upgrade := resp.Header.Get("Upgrade"); upgrade != "allowed/1" {
		t.Fatalf("expected Upgrade header %q, got %q", "allowed/1", upgrade)
	}
}

func TestHijack_ProtoCaseInsensitive(t *testing.T) {
	var got string
	var wg sync.WaitGroup
	wg.Add(1)

	srv := httptest.NewServer(Hijack(func(conn net.Conn, proto string) {
		defer conn.Close()
		got = proto
		wg.Done()
	}, Protocols("MyProto/1")))
	defer srv.Close()

	addr := strings.TrimPrefix(srv.URL, "http://")
	conn := dialAndUpgrade(t, addr, "MYPROTO/1")
	conn.Close()

	wg.Wait()

	if got != "MyProto/1" {
		t.Fatalf("expected server-side proto name %q, got %q", "MyProto/1", got)
	}
}

func TestHijack_NoProtosAcceptsAnything(t *testing.T) {
	var got string
	var wg sync.WaitGroup
	wg.Add(1)

	srv := httptest.NewServer(Hijack(func(conn net.Conn, proto string) {
		defer conn.Close()
		got = proto
		wg.Done()
	}))
	defer srv.Close()

	addr := strings.TrimPrefix(srv.URL, "http://")
	conn := dialAndUpgrade(t, addr, "whatever/99")
	conn.Close()

	wg.Wait()

	if got != "whatever/99" {
		t.Fatalf("expected %q, got %q", "whatever/99", got)
	}
}

func TestHijack_HandlerPanicDoesNotCrash(t *testing.T) {
	var wg sync.WaitGroup
	wg.Add(1)

	srv := httptest.NewServer(Hijack(func(conn net.Conn, proto string) {
		defer wg.Done()
		panic("boom")
	}))
	defer srv.Close()

	addr := strings.TrimPrefix(srv.URL, "http://")
	conn := dialAndUpgrade(t, addr, "test/1")
	conn.Close()

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("handler panic did not recover within timeout")
	}
}

func TestHijack_MultipleProtos(t *testing.T) {
	var got string
	var wg sync.WaitGroup
	wg.Add(1)

	srv := httptest.NewServer(Hijack(func(conn net.Conn, proto string) {
		defer conn.Close()
		got = proto
		wg.Done()
	}, Protocols("alpha/1", "beta/2", "gamma/3")))
	defer srv.Close()

	addr := strings.TrimPrefix(srv.URL, "http://")
	conn := dialAndUpgrade(t, addr, "beta/2")
	conn.Close()

	wg.Wait()

	if got != "beta/2" {
		t.Fatalf("expected %q, got %q", "beta/2", got)
	}
}

func TestHijack_ResponseHeaders(t *testing.T) {
	srv := httptest.NewServer(Hijack(
		func(conn net.Conn, proto string) { conn.Close() },
		ResponseHeaders(func(r *http.Request, proto string) http.Header {
			h := http.Header{}
			h.Set("X-Challenge-Response", r.Header.Get("X-Challenge"))
			return h
		}),
	))
	defer srv.Close()

	conn, err := net.Dial("tcp", strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	req := "GET / HTTP/1.1\r\n" +
		"Host: localhost\r\n" +
		"Connection: Upgrade\r\n" +
		"Upgrade: test/1\r\n" +
		"X-Challenge: opensesame\r\n" +
		"\r\n"
	conn.Write([]byte(req))

	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("expected 101, got %d", resp.StatusCode)
	}

	if got := resp.Header.Get("X-Challenge-Response"); got != "opensesame" {
		t.Fatalf("expected X-Challenge-Response %q, got %q", "opensesame", got)
	}
}
