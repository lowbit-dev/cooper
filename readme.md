# cooper

`cooper` handles the HTTP/1.1 `101 Switching Protocols` handshake — on both ends of a connection — and returns a `net.Conn`. It does nothing else.

The handshake is a narrow, well-specified operation: exchange headers, agree on a protocol, and transfer connection ownership. Everything that follows — framing, message parsing, keep-alives, codec concerns — belongs to the application or to a protocol-specific package built on top. Cooper does not model protocols. It clears the path for them.

Zero external dependencies. All types in the public API are from the standard library.

```
go get lowbit.dev/cooper
```

---

## Server side

`cooper.Hijack` returns an `http.Handler`. On receipt of a valid upgrade request it performs the handshake, then transfers connection ownership to the provided handler. The handler runs in its own goroutine and is responsible for closing the connection.

```go
http.Handle("/raw", cooper.Hijack(func(conn net.Conn, proto string) {
    defer conn.Close()
    // conn is a raw net.Conn. proto is the negotiated protocol name.
    io.Copy(conn, conn)
}))
```

### Protocol restriction

Pass `Protocols` to restrict which upgrade values the server accepts. Matching is case-insensitive. A client requesting an unlisted protocol receives `426 Upgrade Required` with the accepted list in the `Upgrade` response header. Without `Protocols`, any non-empty `Upgrade` value is accepted.

```go
cooper.Hijack(handler, cooper.Protocols("myproto/1", "myproto/2"))
```

### Additional response headers

Some protocols require headers in the `101` response beyond the mandatory `Connection` and `Upgrade` fields. `ResponseHeaders` provides a hook for this. The function receives the original request and the negotiated protocol name; whatever it returns is merged into the response.

```go
cooper.Hijack(handler, cooper.ResponseHeaders(func(r *http.Request, proto string) http.Header {
    h := http.Header{}
    h.Set("Sec-WebSocket-Accept", deriveAccept(r.Header.Get("Sec-WebSocket-Key")))
    return h
}))
```

### Error handling

Errors that occur after the response writer is no longer available — write and flush failures on the `101` response, and recovered panics from the handler goroutine — cannot be returned. `OnError` sets a callback to receive them. If not set, they are silently discarded.

```go
cooper.Hijack(handler, cooper.OnError(func(err error) {
    slog.Error("upgrade error", "err", err)
}))
```

The sentinel values `ErrHijackFailed`, `ErrWriteHandshake`, `ErrFlushHandshake`, `ErrHandlerPanic`, and `ErrDrainBuffer` can be used with `errors.Is` to distinguish failure modes.

---

## Client side

`cooper.Upgrade` performs the client half of the handshake over an existing `net.Conn`. The caller constructs the request in full; the only requirement is that it carries an `Upgrade` header. `Connection: Upgrade` is added automatically if absent. A 10-second deadline is applied to the handshake and cleared on return.

```go
conn, _ := net.Dial("tcp", "host:8080")

req, _ := http.NewRequest("GET", "http://host:8080/raw", nil)
req.Header.Set("Upgrade", "myproto/1")

upgraded, err := cooper.Upgrade(conn, req)
if err != nil {
    // err wraps one of the Err* sentinels from upgrade.go
}
defer upgraded.Close()
```

`Upgrade` returns an error if the server responds with any status other than `101`, if the response `Upgrade` header does not echo the requested protocol, or if `Connection: Upgrade` is absent from the response.

### Response validation

Protocols that exchange additional headers during the handshake — WebSocket's `Sec-WebSocket-Accept`, for example — can be verified through `ResponseValidator`. The validator is called after Cooper's own checks pass. If it returns an error, `Upgrade` returns that error wrapped with `ErrResponseValidator`.

```go
cooper.Upgrade(conn, req, cooper.ResponseValidator(func(req *http.Request, resp *http.Response) error {
    expected := deriveAccept(req.Header.Get("Sec-WebSocket-Key"))
    if resp.Header.Get("Sec-WebSocket-Accept") != expected {
        return errors.New("accept header mismatch")
    }
    return nil
}))
```

---

## Buffered read data

Go's HTTP server buffers incoming data through a `bufio.Reader`. After the handshake request is parsed, that buffer may already contain bytes that belong to the raw protocol stream. Cooper drains those bytes and prepends them transparently to the returned connection via an internal `prefixConn` wrapper. The same applies on the client side. The returned `net.Conn` is always safe to read from directly.
