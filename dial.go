package cooper

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

var (
	// ErrNilRequest is returned when the provided *http.Request is nil.
	ErrNilRequest = errors.New("request cannot be nil")

	// ErrInvalidHost is returned when the request URL does not contain a valid hostname.
	ErrInvalidHost = errors.New("request is missing a valid host")

	// ErrProtocolConflict is returned when a protocol is supplied via WithProtocol
	// and the request already carries a different Upgrade header value.
	ErrProtocolConflict = errors.New("protocol conflict: WithProtocol and Upgrade header disagree")

	// ErrDialFailed is returned when the initial network connection (TCP)
	// to the remote host cannot be established. This usually indicates
	// a timeout, an unreachable host, or a refused connection.
	ErrDialFailed = errors.New("dial failed")

	// ErrTLSHandshakeFailed is returned when the TCP connection is successful
	// but the subsequent TLS/SSL cryptographic handshake fails. This can
	// happen due to expired certificates, hostname mismatches (SNI),
	// or unsupported protocol versions.
	ErrTLSHandshakeFailed = errors.New("tls handshake failed")
)

// DialOption configures the behaviour of Dial.
type DialOption func(*dialConfig)

type dialConfig struct {
	dialer         *net.Dialer
	tlsConfig      *tls.Config
	upgradeOptions []UpgradeOption
	protocol       string
}

// WithDialer sets a custom net.Dialer for establishing the TCP connection.
func WithDialer(d *net.Dialer) DialOption {
	return func(c *dialConfig) {
		c.dialer = d
	}
}

// WithTLSConfig enables TLS for the connection using the provided configuration.
// Pass &tls.Config{} to use default TLS settings; the server name is derived
// from the request URL when not explicitly set.
func WithTLSConfig(tlsConfig *tls.Config) DialOption {
	return func(c *dialConfig) {
		c.tlsConfig = tlsConfig
	}
}

// WithUpgradeOptions passes additional options to the underlying Upgrade call.
func WithUpgradeOptions(opts ...UpgradeOption) DialOption {
	return func(c *dialConfig) {
		c.upgradeOptions = append(c.upgradeOptions, opts...)
	}
}

// WithProtocol sets the Upgrade protocol when the request does not already
// carry an Upgrade header. If the request header is already set to a different
// value, Dial returns ErrProtocolConflict.
func WithProtocol(proto string) DialOption {
	return func(c *dialConfig) {
		c.protocol = proto
	}
}

// DialContext establishes a connection to the host specified in r, performs
// an HTTP/1.1 protocol upgrade, and returns the established connection using the provided context.
func DialContext(ctx context.Context, r *http.Request, opts ...DialOption) (net.Conn, error) {
	if r == nil {
		return nil, ErrNilRequest
	}

	cfg := &dialConfig{}
	for _, o := range opts {
		o(cfg)
	}

	headerProto := r.Header.Get("Upgrade")
	if cfg.protocol != "" && headerProto != "" && !strings.EqualFold(cfg.protocol, headerProto) {
		return nil, fmt.Errorf("%w: option %q, header %q", ErrProtocolConflict, cfg.protocol, headerProto)
	}

	if cfg.protocol != "" && headerProto == "" {
		r.Header.Set("Upgrade", cfg.protocol)
	}

	host := r.URL.Hostname()
	if host == "" {
		return nil, ErrInvalidHost
	}

	port := r.URL.Port()
	if port == "" {
		if cfg.tlsConfig != nil {
			port = "443"
		} else {
			port = "80"
		}
	}

	addr := net.JoinHostPort(host, port)

	dialer := cfg.dialer
	if dialer == nil {
		dialer = &net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}
	}

	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrDialFailed, err)
	}

	if cfg.tlsConfig != nil {
		tlsConfig := cfg.tlsConfig
		if tlsConfig.ServerName == "" {
			tlsConfig = tlsConfig.Clone()
			tlsConfig.ServerName = host
		}

		tlsConn := tls.Client(conn, tlsConfig)

		if err := tlsConn.HandshakeContext(ctx); err != nil {
			conn.Close()
			return nil, fmt.Errorf("%w: %w", ErrTLSHandshakeFailed, err)
		}
		conn = tlsConn
	}

	upgradedConn, err := Upgrade(conn, r, cfg.upgradeOptions...)
	if err != nil {
		conn.Close()
		return nil, err
	}

	return upgradedConn, nil
}

// Dial establishes a connection to the host specified in r, performs
// an HTTP/1.1 protocol upgrade, and returns the established connection using the request context.
//
// In case you want to use an alternate context for dialing use DialContext instead.
func Dial(r *http.Request, opts ...DialOption) (net.Conn, error) {
	return DialContext(r.Context(), r, opts...)
}
