// Package rtorrent talks to rTorrent over its SCGI Unix socket with XML-RPC.
// It exposes typed calls for exactly the commands peerglass needs
// (docs/rtorrent.md) and nothing that passes arbitrary commands on (S1).
package rtorrent

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Client calls rTorrent. One connection is opened per call.
type Client struct {
	// Socket is the path of rTorrent's SCGI Unix socket.
	Socket string
	// Timeout bounds a call whose context has no deadline of its own.
	Timeout time.Duration
}

// New returns a client for the socket with a 30 second timeout.
func New(socket string) *Client {
	return &Client{Socket: socket, Timeout: 30 * time.Second}
}

// Call performs one XML-RPC call. Arguments may be string, int, int64,
// []byte (sent as base64), []any or map[string]any. Faults come back as
// *Fault; any other error means rTorrent could not be reached or answered
// unintelligibly.
func (c *Client) Call(ctx context.Context, method string, args ...any) (any, error) {
	body, err := encodeCall(method, args)
	if err != nil {
		return nil, err
	}
	if _, ok := ctx.Deadline(); !ok && c.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.Timeout)
		defer cancel()
	}
	resp, err := c.roundTrip(ctx, body)
	if err != nil {
		return nil, fmt.Errorf("rtorrent: %s: %w", method, err)
	}
	v, err := decodeResponse(resp)
	if err != nil {
		if _, isFault := err.(*Fault); isFault {
			return nil, err
		}
		return nil, fmt.Errorf("rtorrent: %s: %w", method, err)
	}
	return v, nil
}

// call is one entry of a system.multicall.
type call struct {
	method string
	args   []any
}

// multicall runs several calls in one request. results[i] holds the value of
// calls[i], or nil with faults[i] set when that one call failed.
func (c *Client) multicall(ctx context.Context, calls []call) (results []any, faults []error, err error) {
	list := make([]any, len(calls))
	for i, cl := range calls {
		params := cl.args
		if params == nil {
			params = []any{}
		}
		// Order matters: rTorrent rejects a params-first struct.
		list[i] = orderedStruct{{"methodName", cl.method}, {"params", params}}
	}
	v, err := c.Call(ctx, "system.multicall", list)
	if err != nil {
		return nil, nil, err
	}
	rows, ok := v.([]any)
	if !ok || len(rows) != len(calls) {
		return nil, nil, fmt.Errorf("rtorrent: system.multicall: expected %d results, got %T", len(calls), v)
	}
	results = make([]any, len(calls))
	faults = make([]error, len(calls))
	for i, row := range rows {
		switch r := row.(type) {
		case []any:
			if len(r) != 1 {
				return nil, nil, fmt.Errorf("rtorrent: system.multicall: result %d has %d values", i, len(r))
			}
			results[i] = r[0]
		case map[string]any:
			code, _ := r["faultCode"].(int64)
			msg, _ := r["faultString"].(string)
			faults[i] = &Fault{Code: code, Message: msg}
		default:
			return nil, nil, fmt.Errorf("rtorrent: system.multicall: result %d is %T", i, row)
		}
	}
	return results, faults, nil
}

// ValidHash reports whether h is a 40 character hexadecimal info-hash.
func ValidHash(h string) bool {
	if len(h) != 40 {
		return false
	}
	for i := 0; i < len(h); i++ {
		c := h[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

// checkHash validates and upper-cases an info-hash before it becomes a
// command target.
func checkHash(h string) (string, error) {
	if !ValidHash(h) {
		return "", fmt.Errorf("rtorrent: invalid info-hash %q", h)
	}
	return strings.ToUpper(h), nil
}

// asString converts a decoded XML-RPC value to a string, leniently.
func asString(v any) string {
	switch v := v.(type) {
	case string:
		return v
	case []byte:
		return string(v)
	case int64:
		return fmt.Sprint(v)
	case nil:
		return ""
	}
	return fmt.Sprint(v)
}

// asInt converts a decoded XML-RPC value to an integer, leniently. rTorrent
// returns most numbers as <i8> but custom values as strings.
func asInt(v any) int64 {
	switch v := v.(type) {
	case int64:
		return v
	case bool:
		if v {
			return 1
		}
		return 0
	case float64:
		return int64(v)
	case string:
		var n int64
		fmt.Sscan(strings.TrimSpace(v), &n)
		return n
	}
	return 0
}
