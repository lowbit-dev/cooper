package cooper

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
)

var (
	// ErrHijackFailed is delivered to the OnError callback when the server
	// cannot take ownership of the connection from the HTTP server.
	ErrHijackFailed = errors.New("hijack failed")

	// ErrWriteHandshake is delivered when the 101 response cannot be written.
	ErrWriteHandshake = errors.New("write handshake response failed")

	// ErrFlushHandshake is delivered when the 101 response cannot be flushed.
	ErrFlushHandshake = errors.New("flush handshake response failed")

	// ErrHandlerPanic is delivered when a HijackHandler panics.
	// The recovered value is appended to the error message.
	ErrHandlerPanic = errors.New("handler panic")
)

// HijackHandler is called after a successful HTTP/1.1 upgrade handshake.
// conn is the raw connection ready for protocol use; proto is the negotiated
// protocol value from the Upgrade header.
//
// Ownership of conn transfers to the handler — it is responsible for closing it.
type HijackHandler func(conn net.Conn, proto string)

// Option configures the behaviour of Hijack.
type Option func(*config)

type config struct {
	protos          []string
	onError         func(error)
	responseHeaders func(*http.Request, string) http.Header
}

// Protocols restricts the server to the given protocol names.
// Matching is case-insensitive. Clients requesting a protocol not in the list
// receive a 426 Upgrade Required response listing the accepted protocols.
// When Protocols is not set, any non-empty Upgrade value is accepted.
func Protocols(protos ...string) Option {
	return func(c *config) {
		c.protos = protos
	}
}

// OnError sets a callback that receives errors occurring during or after the
// handshake. This includes write failures and recovered handler panics.
// If not set, these errors are silently discarded.
func OnError(fn func(error)) Option {
	return func(c *config) {
		c.onError = fn
	}
}

// ResponseHeaders sets a function that produces additional headers to include
// in the 101 Switching Protocols response. It receives the incoming request and
// the negotiated protocol name so challenge headers (e.g. Sec-WebSocket-Accept)
// can be derived from them. Headers returned here are merged into the response
// after the mandatory Connection and Upgrade headers.
func ResponseHeaders(fn func(*http.Request, string) http.Header) Option {
	return func(c *config) {
		c.responseHeaders = fn
	}
}

// Hijack returns an http.Handler that performs a full HTTP/1.1 protocol upgrade
// handshake and hands the raw connection to handler.
//
// Use Protocols to restrict accepted upgrade values. Use OnError to receive
// errors that occur after the response writer is no longer available.
//
// Ownership of the connection transfers to handler on success;
// Hijack closes conn only when the handshake itself fails.
func Hijack(handler HijackHandler, opts ...Option) http.Handler {
	cfg := &config{}
	for _, o := range opts {
		o(cfg)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested := r.Header.Get("Upgrade")
		if requested == "" {
			http.Error(w, "missing Upgrade header", http.StatusBadRequest)
			return
		}

		if !strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade") {
			http.Error(w, "missing Connection: Upgrade header", http.StatusBadRequest)
			return
		}

		proto := requested
		if len(cfg.protos) > 0 {
			proto = ""
			for _, p := range cfg.protos {
				if strings.EqualFold(p, requested) {
					proto = p
					break
				}
			}
		}

		if proto == "" {
			w.Header().Set("Upgrade", strings.Join(cfg.protos, ", "))
			http.Error(w, "unsupported upgrade protocol", http.StatusUpgradeRequired)
			return
		}

		hj, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "protocol upgrade not supported", http.StatusInternalServerError)
			return
		}

		conn, buf, err := hj.Hijack()
		if err != nil {
			if cfg.onError != nil {
				cfg.onError(fmt.Errorf("%w: %w", ErrHijackFailed, err))
			}
			return
		}

		resp := "HTTP/1.1 101 Switching Protocols\r\n" +
			"Connection: Upgrade\r\n" +
			"Upgrade: " + proto + "\r\n"

		if cfg.responseHeaders != nil {
			for k, vs := range cfg.responseHeaders(r, proto) {
				for _, v := range vs {
					resp += k + ": " + v + "\r\n"
				}
			}
		}
		resp += "\r\n"

		if _, err := buf.WriteString(resp); err != nil {
			if cfg.onError != nil {
				cfg.onError(fmt.Errorf("%w: %w", ErrWriteHandshake, err))
			}
			conn.Close()
			return
		}

		if err := buf.Flush(); err != nil {
			if cfg.onError != nil {
				cfg.onError(fmt.Errorf("%w: %w", ErrFlushHandshake, err))
			}
			conn.Close()
			return
		}

		// The HTTP server may have read bytes past the end of the request
		// into buf.Reader's internal buffer. Those bytes are the start of
		// the raw protocol stream and must not be lost.
		var raw net.Conn = conn
		if n := buf.Reader.Buffered(); n > 0 {
			peeked := make([]byte, n)
			if _, err := io.ReadFull(buf.Reader, peeked); err != nil {
				if cfg.onError != nil {
					cfg.onError(fmt.Errorf("%w: %w", ErrDrainBuffer, err))
				}
				conn.Close()
				return
			}
			raw = &prefixConn{
				Reader: io.MultiReader(bytes.NewReader(peeked), conn),
				Conn:   conn,
			}
		}

		go func() {
			defer func() {
				if p := recover(); p != nil {
					if cfg.onError != nil {
						cfg.onError(fmt.Errorf("%w: %v", ErrHandlerPanic, p))
					}
				}
			}()

			handler(raw, proto)
		}()
	})
}
