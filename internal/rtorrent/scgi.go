package rtorrent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"time"
)

// maxResponse bounds what one reply may occupy in memory. A d.multicall2 over
// 1,000 torrents is a few megabytes.
const maxResponse = 64 << 20

// scgiRequest frames body as an SCGI request: a netstring of headers, then
// the body (docs/rtorrent.md, Transport).
func scgiRequest(body []byte) []byte {
	hdr := fmt.Sprintf("CONTENT_LENGTH\x00%d\x00SCGI\x001\x00", len(body))
	var b bytes.Buffer
	b.Grow(len(hdr) + len(body) + 16)
	fmt.Fprintf(&b, "%d:%s,", len(hdr), hdr)
	b.Write(body)
	return b.Bytes()
}

// splitSCGIResponse returns the body of an SCGI reply: everything after the
// HTTP-style headers and the blank line that ends them.
func splitSCGIResponse(resp []byte) ([]byte, error) {
	i := bytes.Index(resp, []byte("\r\n\r\n"))
	if i < 0 {
		return nil, errors.New("malformed SCGI response: no header terminator")
	}
	return resp[i+4:], nil
}

// roundTrip sends one request on a fresh connection and reads the reply until
// rTorrent closes the connection. One connection per call, as rTorrent
// serves them one at a time anyway.
func (c *Client) roundTrip(ctx context.Context, body []byte) ([]byte, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", c.Socket)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(dl); err != nil {
			return nil, err
		}
	}
	// Unblock a pending read or write if the context is cancelled early.
	stop := context.AfterFunc(ctx, func() { conn.SetDeadline(time.Now()) })
	defer stop()

	if _, err := conn.Write(scgiRequest(body)); err != nil {
		return nil, err
	}
	resp, err := io.ReadAll(io.LimitReader(conn, maxResponse+1))
	if err != nil {
		return nil, err
	}
	if len(resp) > maxResponse {
		return nil, fmt.Errorf("response larger than %d bytes", maxResponse)
	}
	return splitSCGIResponse(resp)
}
