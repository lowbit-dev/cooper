package cooper

import (
	"errors"
	"io"
	"net"
)

// ErrDrainBuffer is returned or delivered when leftover buffered bytes from
// the HTTP layer cannot be read before the connection is handed to the caller.
var ErrDrainBuffer = errors.New("drain read buffer failed")

// prefixConn wraps a net.Conn and replaces its Read with a reader that drains
// a byte prefix (leftover HTTP buffer data) before falling through to the
// underlying connection. All other net.Conn methods delegate directly.
type prefixConn struct {
	io.Reader
	net.Conn
}

func (c *prefixConn) Read(b []byte) (int, error) {
	return c.Reader.Read(b)
}
