package cooper

import (
	"bytes"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
)

// HijackHandler is called after a successful HTTP/1.1 upgrade handshake.
// conn is the raw connection ready for protocol use; proto is the negotiated
// protocol value from the Upgrade header.
//
// Ownership of conn transfers to the handler — it is responsible for closing it.
type HijackHandler func(conn net.Conn, proto string)

// Hijack returns an http.Handler that performs a full HTTP/1.1 protocol upgrade
// handshake and hands the raw connection to handler.
//
// protos is the optional set of protocol names the server accepts.
// When no protos are provided, any non-empty Upgrade value is accepted.
// When protos are provided, the client's Upgrade header is matched
// case-insensitively; if it requests a protocol not in the list, a
// 426 Upgrade Required response is sent listing the accepted protocols.
//
// Ownership of the connection transfers to handler on success;
// Hijack closes conn only when the handshake itself fails.
func Hijack(handler HijackHandler, protos ...string) http.Handler {
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
		if len(protos) > 0 {
			proto = ""
			for _, p := range protos {
				if strings.EqualFold(p, requested) {
					proto = p
					break
				}
			}
		}

		if proto == "" {
			w.Header().Set("Upgrade", strings.Join(protos, ", "))
			http.Error(w, "unsupported upgrade protocol", http.StatusUpgradeRequired)
			return
		}

		hj, ok := w.(http.Hijacker)
		if !ok {
			slog.Error("writer is not hijackable")
			http.Error(w, "protocol upgrade not supported", http.StatusInternalServerError)
			return
		}

		conn, buf, err := hj.Hijack()
		if err != nil {
			slog.Error("failed to hijack connection", "error", err)
			return
		}

		resp := "HTTP/1.1 101 Switching Protocols\r\n" +
			"Connection: Upgrade\r\n" +
			"Upgrade: " + proto + "\r\n" +
			"\r\n"

		if _, err := buf.WriteString(resp); err != nil {
			slog.Error("failed to write 101 response", "error", err)
			conn.Close()
			return
		}

		if err := buf.Flush(); err != nil {
			slog.Error("failed to flush 101 response", "error", err)
			conn.Close()
			return
		}

		// The HTTP server may have read bytes past the end of the request
		// into buf.Reader's internal buffer. Those bytes are the start of
		// the raw protocol stream and must not be lost
		var raw net.Conn = conn
		if n := buf.Reader.Buffered(); n > 0 {
			peeked := make([]byte, n)
			if _, err := io.ReadFull(buf.Reader, peeked); err != nil {
				slog.Error("failed to drain read buffer", "error", err)
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
				if r := recover(); r != nil {
					slog.Error("[cooper.Hijack] recovered panic in hijack handler goroutine", "panic", r)
				}
			}()

			handler(raw, proto)
		}()
	})
}

// HTTPHijacker is a convenience http.Handler that bridges a HijackHandler
// to the cooper.Hijack upgrade flow. It performs the HTTP/1.1 101 handshake
// and delegates the resulting raw connection to the underlying handler.
type HTTPHijacker struct {
	handler HijackHandler
	protos  []string
}

// NewHTTPHijacker returns an HTTPHijacker that will delegate upgraded
// connections to handler. It panics if handler is nil.
func NewHTTPHijacker(handler HijackHandler, protos ...string) *HTTPHijacker {
	if handler == nil {
		panic("cooper: nil HijackHandler passed to NewHTTPHijacker")
	}

	return &HTTPHijacker{handler: handler, protos: protos}
}

// ServeHTTP implements http.Handler by initiating a protocol upgrade for the
// specified protocols and handing the hijacked connection to the underlying
// HijackHandler.
func (h *HTTPHijacker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	Hijack(h.handler, h.protos...).ServeHTTP(w, r)
}
