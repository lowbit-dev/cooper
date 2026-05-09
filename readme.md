# cooper

A Go package for handling the HTTP/1.1 `101 Switching Protocols` handshake on both ends of a connection returning a `net.Conn`.

## Features
- **Server-side hijack**: Handle upgrade requests with `cooper.Hijack`, an `http.Handler` that negotiates the handshake and passes the raw connection to your code.
- **Client-side dial**: Establish a TCP (or TLS) connection, perform the upgrade handshake, and receive a `net.Conn` in one call with `cooper.Dial`.
- **Client-side upgrade**: Perform the upgrade handshake over any existing `net.Conn` with `cooper.Upgrade`.
- **Protocol negotiation**: Restrict which protocols the server accepts; unrecognised clients receive `426 Upgrade Required`.
- **Response validation**: Verify custom handshake headers (e.g. `Sec-WebSocket-Accept`) before the connection is handed over.
- **Buffered data safety**: Leftover HTTP buffer bytes are transparently prepended to the returned connection — always safe to read from directly.
- **Zero dependencies**: No external imports. Every type in the public API is from the standard library.

## Installation

```
go get lowbit.dev/cooper
```

## Usage

```go
// Server
http.Handle("/raw", cooper.Hijack(func(conn net.Conn, proto string) {
    defer conn.Close()
    io.Copy(conn, conn)
}))

// Client — using Dial (TCP + upgrade in one call)
req, _ := http.NewRequest("GET", "http://host:8080/raw", nil)
req.Header.Set("Upgrade", "myproto/1")

conn, err := cooper.Dial(req)
if err != nil {
    // err wraps one of the Err* sentinels
}
defer conn.Close()

// Client — using Upgrade over an existing connection
raw, _ := net.Dial("tcp", "host:8080")
req, _ := http.NewRequest("GET", "http://host:8080/raw", nil)
req.Header.Set("Upgrade", "myproto/1")

conn, err := cooper.Upgrade(raw, req)
if err != nil {
    // err wraps one of the Err* sentinels
}
defer conn.Close()
```

## Dial

`cooper.Dial` establishes the TCP connection, performs the TLS handshake if configured, and runs the HTTP/1.1 upgrade — returning a `net.Conn` ready for raw protocol use.

The upgrade protocol is read from the request's `Upgrade` header. It can also be provided via `WithProtocol`, which sets the header when the request doesn't already carry one. Supplying both with different values returns `ErrProtocolConflict`.

```go
req, _ := http.NewRequest("GET", "https://host:8443/raw", nil)

conn, err := cooper.Dial(req,
    cooper.WithProtocol("myproto/1"),
    cooper.WithTLSConfig(&tls.Config{}),
)
```

### Dial options
- `WithProtocol(proto string)`: Set the `Upgrade` header when the request does not already carry one. Conflicts with a pre-set header return `ErrProtocolConflict`.
- `WithTLSConfig(cfg *tls.Config)`: Enable TLS using the given config. Pass `&tls.Config{}` for defaults; the server name is derived from the request URL when not explicitly set.
- `WithDialer(d *net.Dialer)`: Use a custom dialer for the TCP connection.
- `WithUpgradeOptions(opts ...UpgradeOption)`: Pass additional options to the underlying `Upgrade` call (e.g. `ResponseValidator`).

## Server options
- `Protocols(names ...string)`: Restrict accepted upgrade values. Case-insensitive. Unrecognised protocols receive `426`.
- `ResponseHeaders(fn)`: Inject additional headers into the `101` response (e.g. `Sec-WebSocket-Accept`).
- `OnError(fn)`: Receive errors that occur after the response writer is gone — write/flush failures and recovered handler panics.

```go
cooper.Hijack(handler,
    cooper.Protocols("myproto/1", "myproto/2"),
    cooper.ResponseHeaders(func(r *http.Request, proto string) http.Header {
        h := http.Header{}
        h.Set("Sec-WebSocket-Accept", deriveAccept(r.Header.Get("Sec-WebSocket-Key")))
        return h
    }),
    cooper.OnError(func(err error) {
        slog.Error("upgrade error", "err", err)
    }),
)
```

## TLS

Pass `WithTLSConfig` to `Dial` to enable TLS. The server name is set automatically from the request URL when not specified in the config.

```go
req, _ := http.NewRequest("GET", "https://host:8443/raw", nil)
req.Header.Set("Upgrade", "myproto/1")

conn, err := cooper.Dial(req, cooper.WithTLSConfig(&tls.Config{}))
```

When using `Upgrade` directly, `cooper.Upgrade` works over any `net.Conn`, including one you dialled with `tls.Dial` yourself.

## Client options
- `ResponseValidator(fn)`: Called after Cooper's own checks pass. Return an error to reject the response; it is wrapped with `ErrResponseValidator`.

```go
cooper.Upgrade(conn, req,
    cooper.ResponseValidator(func(req *http.Request, resp *http.Response) error {
        if resp.Header.Get("Sec-WebSocket-Accept") != deriveAccept(req.Header.Get("Sec-WebSocket-Key")) {
            return errors.New("accept header mismatch")
        }
        return nil
    }),
)
```

## Error sentinels
All errors wrap a sentinel value detectable with `errors.Is`.

**Dial**
- `ErrNilRequest`: A nil `*http.Request` was passed to `Dial`.
- `ErrInvalidHost`: The request URL contains no hostname.
- `ErrProtocolConflict`: `WithProtocol` and the request's `Upgrade` header both set a protocol but disagree on the value.
- `ErrDialFailed`: The TCP connection could not be established.
- `ErrTLSHandshakeFailed`: TCP succeeded but the TLS handshake failed.

**Upgrade**
- `ErrMissingUpgradeHeader`: Request carries no `Upgrade` header.
- `ErrSetDeadline`: Handshake deadline could not be set.
- `ErrSendRequest`: Upgrade request could not be written.
- `ErrReadResponse`: Server response could not be read or parsed.
- `ErrUnexpectedStatus`: Server responded with a status other than `101`.
- `ErrProtocolMismatch`: Server's `Upgrade` response header doesn't match what was requested.
- `ErrMissingConnectionHeader`: Server's response is missing `Connection: Upgrade`.
- `ErrResponseValidator`: A `ResponseValidator` rejected the response.
- `ErrDrainBuffer`: Could not drain the HTTP read buffer before handing off the connection.

**Hijack**
- `ErrHijackFailed`: Connection could not be hijacked from the HTTP server.
- `ErrWriteHandshake`: Failed to write the `101` response.
- `ErrFlushHandshake`: Failed to flush the `101` response.
- `ErrHandlerPanic`: Recovered panic in the handler goroutine.
