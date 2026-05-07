package cooper

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

var (
	// ErrMissingUpgradeHeader is returned by Upgrade when the request has no
	// Upgrade header set.
	ErrMissingUpgradeHeader = errors.New("missing Upgrade header")

	// ErrSetDeadline is returned by Upgrade when the handshake deadline cannot
	// be set on the connection.
	ErrSetDeadline = errors.New("set handshake deadline failed")

	// ErrSendRequest is returned by Upgrade when the HTTP request cannot be
	// written to the connection.
	ErrSendRequest = errors.New("send upgrade request failed")

	// ErrReadResponse is returned by Upgrade when the server response cannot
	// be read or parsed.
	ErrReadResponse = errors.New("read upgrade response failed")

	// ErrUnexpectedStatus is returned by Upgrade when the server responds with
	// a status other than 101 Switching Protocols.
	ErrUnexpectedStatus = errors.New("unexpected response status")

	// ErrProtocolMismatch is returned by Upgrade when the Upgrade header in
	// the server's response does not match the requested protocol.
	ErrProtocolMismatch = errors.New("protocol mismatch in response")

	// ErrMissingConnectionHeader is returned by Upgrade when the server's
	// response is missing the required Connection: Upgrade header.
	ErrMissingConnectionHeader = errors.New("response missing Connection: Upgrade header")

	// ErrResponseValidator is returned by Upgrade when a ResponseValidator
	// option rejects the server's response.
	ErrResponseValidator = errors.New("response validation failed")
)

// handshakeTimeout caps how long the HTTP upgrade round-trip may take.
const handshakeTimeout = 10 * time.Second

// UpgradeOption configures the behaviour of Upgrade.
type UpgradeOption func(*upgradeConfig)

type upgradeConfig struct {
	responseValidator func(req *http.Request, resp *http.Response) error
}

// ResponseValidator sets a function that is called after Cooper's own header
// checks pass (101 status, Upgrade match, Connection: Upgrade). The validator
// receives the original request and the server response, and may inspect any
// additional headers — for example to verify Sec-WebSocket-Accept. If it
// returns an error, Upgrade returns that error wrapped with ErrResponseValidator.
func ResponseValidator(fn func(req *http.Request, resp *http.Response) error) UpgradeOption {
	return func(c *upgradeConfig) {
		c.responseValidator = fn
	}
}

// Upgrade performs an HTTP/1.1 protocol upgrade on conn using req. On success
// it returns a net.Conn.
//
// The caller is fully responsible for constructing req: method, URL, headers,
// and an optional body may all be set. The only hard requirement is that
// req.Header must contain "Upgrade: <proto>" (used for response validation).
func Upgrade(conn net.Conn, req *http.Request, opts ...UpgradeOption) (net.Conn, error) {
	cfg := &upgradeConfig{}
	for _, o := range opts {
		o(cfg)
	}

	proto := req.Header.Get("Upgrade")
	if proto == "" {
		return nil, ErrMissingUpgradeHeader
	}

	if !strings.Contains(strings.ToLower(req.Header.Get("Connection")), "upgrade") {
		req.Header.Set("Connection", "upgrade")
	}

	if err := conn.SetDeadline(time.Now().Add(handshakeTimeout)); err != nil {
		return nil, fmt.Errorf("%w (%s): %w", ErrSetDeadline, proto, err)
	}
	defer conn.SetDeadline(time.Time{})

	if err := req.Write(conn); err != nil {
		return nil, fmt.Errorf("%w (%s): %w", ErrSendRequest, proto, err)
	}

	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		return nil, fmt.Errorf("%w (%s): %w", ErrReadResponse, proto, err)
	}
	resp.Body.Close()

	// Require 101 Switching Protocols.
	if resp.StatusCode != http.StatusSwitchingProtocols {
		return nil, fmt.Errorf("%w (%s): %s", ErrUnexpectedStatus, proto, resp.Status)
	}

	if !strings.EqualFold(resp.Header.Get("Upgrade"), proto) {
		return nil, fmt.Errorf("%w (%s): server returned %q", ErrProtocolMismatch, proto, resp.Header.Get("Upgrade"))
	}

	if !strings.Contains(strings.ToLower(resp.Header.Get("Connection")), "upgrade") {
		return nil, fmt.Errorf("%w (%s)", ErrMissingConnectionHeader, proto)
	}

	if cfg.responseValidator != nil {
		if err := cfg.responseValidator(req, resp); err != nil {
			return nil, fmt.Errorf("%w (%s): %w", ErrResponseValidator, proto, err)
		}
	}

	if n := br.Buffered(); n > 0 {
		peeked := make([]byte, n)
		if _, err := io.ReadFull(br, peeked); err != nil {
			return nil, fmt.Errorf("%w (%s): %w", ErrDrainBuffer, proto, err)
		}

		return &prefixConn{
			Reader: io.MultiReader(bytes.NewReader(peeked), conn),
			Conn:   conn,
		}, nil
	}

	return conn, nil
}
