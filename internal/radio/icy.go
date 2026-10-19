package radio

import (
	"bytes"
	"context"
	"io"
	"net"
)

// DialICY wraps a DialContext function so that responses from SHOUTcast
// servers, which answer "ICY 200 OK" instead of "HTTP/1.x 200 OK", can be read
// by net/http. The status line is rewritten to HTTP/1.0; everything else
// passes through untouched.
func DialICY(dial func(ctx context.Context, network, addr string) (net.Conn, error)) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		c, err := dial(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		return &icyConn{Conn: c}, nil
	}
}

type icyConn struct {
	net.Conn
	checked bool
	pending []byte
}

func (c *icyConn) Read(p []byte) (int, error) {
	if !c.checked {
		c.checked = true
		// Collect enough bytes to recognise the "ICY " prefix.
		buf := make([]byte, 4)
		n, err := io.ReadAtLeast(c.Conn, buf, len(buf))
		buf = buf[:n]
		if bytes.Equal(buf, []byte("ICY ")) {
			buf = []byte("HTTP/1.0 ")
		}
		c.pending = buf
		if len(c.pending) == 0 && err != nil {
			return 0, err
		}
	}
	if len(c.pending) > 0 {
		n := copy(p, c.pending)
		c.pending = c.pending[n:]
		return n, nil
	}
	return c.Conn.Read(p)
}
