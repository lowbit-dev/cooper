# cooper

A Go package for handling the HTTP/1.1 `101 Switching Protocols` handshake on both ends of a connection returning a `net.Conn`.

## Features
- **Server-side hijack**: Handle upgrade requests with `cooper.Hijack`, an `http.Handler` that negotiates the handshake and passes the raw connection to your code.
- **Client-side upgrade**: Perform the client half of the handshake over any `net.Conn` with `cooper.Upgrade`.
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

// Client
conn, _ := net.Dial("tcp", "host:8080")
req, _ := http.NewRequest("GET", "http://host:8080/raw", nil)
req.Header.Set("Upgrade", "myproto/1")

upgraded, err := cooper.Upgrade(conn, req)
if err != nil {
    // err wraps one of the Err* sentinels
}
defer upgraded.Close()
```

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

`cooper.Upgrade` works over any `net.Conn`, including TLS. Dial with `tls.Dial` before passing the connection in.

```go
tlsConn, err := tls.Dial("tcp", "host:8443", &tls.Config{
    ServerName: "host",
})
if err != nil {
    // handle
}

req, _ := http.NewRequest("GET", "https://host:8443/raw", nil)
req.Header.Set("Upgrade", "myproto/1")

upgraded, err := cooper.Upgrade(tlsConn, req)
```

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
- `ErrHijackFailed`: Connection could not be hijacked from the HTTP server.
- `ErrWriteHandshake`: Failed to write the `101` response.
- `ErrFlushHandshake`: Failed to flush the `101` response.
- `ErrHandlerPanic`: Recovered panic in the handler goroutine.
- `ErrDrainBuffer`: Could not drain the HTTP read buffer before handing off the connection.
- `ErrMissingUpgradeHeader`: Request carries no `Upgrade` header.
- `ErrSetDeadline`: Handshake deadline could not be set.
- `ErrSendRequest`: Upgrade request could not be written.
- `ErrReadResponse`: Server response could not be read or parsed.
- `ErrUnexpectedStatus`: Server responded with a status other than `101`.
- `ErrProtocolMismatch`: Server's `Upgrade` response header doesn't match what was requested.
- `ErrMissingConnectionHeader`: Server's response is missing `Connection: Upgrade`.
- `ErrResponseValidator`: A `ResponseValidator` rejected the response.
