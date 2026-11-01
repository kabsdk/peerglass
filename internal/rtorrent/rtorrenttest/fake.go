// Package rtorrenttest is a fake rTorrent for tests: a small SCGI server on
// a Unix socket that answers XML-RPC from canned data and records every
// call it receives (docs/testing.md).
package rtorrenttest

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/kabsdk/peerglass/internal/rtorrent"
)

// Call is one request the fake received.
type Call struct {
	Method string
	Params []any
}

// Torrent is a fake torrent: values keyed by the multicall field that
// yields them ("d.name=", "d.custom=addtime"). "trackers" and "files" hold
// []map[string]any keyed the same way ("t.url=", "f.path="). Fields that are
// not set read as 0.
type Torrent map[string]any

// Handler answers one method. Return a *rtorrent.Fault to fault.
type Handler func(params []any) (any, error)

// Server is the fake. Start it with New and configure it through its
// methods; all of them are safe to call while it serves.
type Server struct {
	t      testing.TB
	socket string

	mu       sync.Mutex
	ln       net.Listener
	calls    []Call
	torrents []Torrent
	handlers map[string]Handler
	// globals holds global values by command, such as
	// "throttle.global_down.max_rate" in bytes per second.
	globals map[string]int64
	wg      sync.WaitGroup
}

// New starts a fake listening on a socket in a temporary directory. It stops
// when the test ends.
func New(t testing.TB) *Server {
	t.Helper()
	s := &Server{t: t, socket: filepath.Join(t.TempDir(), "rtorrent.sock"), handlers: map[string]Handler{}, globals: map[string]int64{}}
	s.Start()
	t.Cleanup(s.Stop)
	return s
}

// Socket is the path of the fake's Unix socket.
func (s *Server) Socket() string { return s.socket }

// Start listens again after Stop, on the same path.
func (s *Server) Start() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ln != nil {
		return
	}
	_ = os.Remove(s.socket)
	ln, err := net.Listen("unix", s.socket)
	if err != nil {
		s.t.Fatalf("rtorrenttest: listen: %v", err)
	}
	s.ln = ln
	s.wg.Add(1)
	go s.serve(ln)
}

// Stop closes the socket, which looks to a client like rTorrent being down.
func (s *Server) Stop() {
	s.mu.Lock()
	ln := s.ln
	s.ln = nil
	s.mu.Unlock()
	if ln != nil {
		ln.Close()
		s.wg.Wait()
		_ = os.Remove(s.socket)
	}
}

// SetTorrents replaces the fake's torrents.
func (s *Server) SetTorrents(ts ...Torrent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.torrents = ts
}

// Torrents returns a copy of the fake's torrents, as actions may have
// changed them.
func (s *Server) Torrents() []Torrent {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Torrent, len(s.torrents))
	copy(out, s.torrents)
	return out
}

// Global returns a global value, such as "throttle.global_down.max_rate".
func (s *Server) Global(name string) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.globals[name]
}

// Handle overrides the answer for one method.
func (s *Server) Handle(method string, h Handler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handlers[method] = h
}

// Calls returns every call received so far, in order.
func (s *Server) Calls() []Call {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Call, len(s.calls))
	copy(out, s.calls)
	return out
}

// CallsTo returns the calls to one method.
func (s *Server) CallsTo(method string) []Call {
	var out []Call
	for _, c := range s.Calls() {
		if c.Method == method {
			out = append(out, c)
		}
	}
	return out
}

// Reset forgets the recorded calls.
func (s *Server) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = nil
}

func (s *Server) serve(ln net.Listener) {
	defer s.wg.Done()
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer conn.Close()
			s.handleConn(conn)
		}()
	}
}

func (s *Server) handleConn(conn net.Conn) {
	body, err := readSCGI(bufio.NewReader(conn))
	if err != nil {
		return
	}
	method, params, err := rtorrent.DecodeCall(body)
	if err != nil {
		writeResponse(conn, rtorrent.EncodeFault(-503, "fake: cannot decode call: "+err.Error()))
		return
	}
	s.mu.Lock()
	s.calls = append(s.calls, Call{Method: method, Params: params})
	s.mu.Unlock()

	v, err := s.dispatch(method, params)
	var resp []byte
	if err != nil {
		var f *rtorrent.Fault
		if !errors.As(err, &f) {
			f = &rtorrent.Fault{Code: -1, Message: err.Error()}
		}
		resp = rtorrent.EncodeFault(f.Code, f.Message)
	} else if resp, err = rtorrent.EncodeResponse(v); err != nil {
		resp = rtorrent.EncodeFault(-1, "fake: cannot encode: "+err.Error())
	}
	writeResponse(conn, resp)
}

// readSCGI reads one SCGI request: "<len>:<headers>,<body>".
func readSCGI(r *bufio.Reader) ([]byte, error) {
	lenStr, err := r.ReadString(':')
	if err != nil {
		return nil, err
	}
	n, err := strconv.Atoi(strings.TrimSuffix(lenStr, ":"))
	if err != nil || n < 0 || n > 1<<16 {
		return nil, errors.New("bad netstring length")
	}
	hdr := make([]byte, n+1)
	if _, err := io.ReadFull(r, hdr); err != nil {
		return nil, err
	}
	if hdr[n] != ',' {
		return nil, errors.New("netstring not terminated")
	}
	fields := bytes.Split(hdr[:n], []byte{0})
	var contentLength int
	for i := 0; i+1 < len(fields); i += 2 {
		if string(fields[i]) == "CONTENT_LENGTH" {
			contentLength, err = strconv.Atoi(string(fields[i+1]))
			if err != nil {
				return nil, err
			}
		}
	}
	body := make([]byte, contentLength)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, err
	}
	return body, nil
}

func writeResponse(w io.Writer, body []byte) {
	fmt.Fprintf(w, "Status: 200 OK\r\nContent-Type: text/xml\r\nContent-Length: %d\r\n\r\n", len(body))
	w.Write(body)
}

func (s *Server) dispatch(method string, params []any) (any, error) {
	s.mu.Lock()
	h := s.handlers[method]
	s.mu.Unlock()
	if h != nil {
		return h(params)
	}
	switch method {
	case "system.client_version":
		return "0.16.27", nil
	case "system.multicall":
		return s.multicall(params)
	case "d.multicall2":
		if len(params) < 2 {
			return nil, &rtorrent.Fault{Code: -501, Message: "Wrong number of parameters"}
		}
		var rows []any
		for _, tr := range s.Torrents() {
			rows = append(rows, rowFor(tr, params[2:]))
		}
		if rows == nil {
			rows = []any{}
		}
		return rows, nil
	case "t.multicall", "f.multicall":
		if len(params) < 2 {
			return nil, &rtorrent.Fault{Code: -501, Message: "Wrong number of parameters"}
		}
		tr, ok := s.find(params[0])
		if !ok {
			return nil, &rtorrent.Fault{Code: -500, Message: "invalid parameters: invalid target"}
		}
		key := "trackers"
		if method == "f.multicall" {
			key = "files"
		}
		rows := []any{}
		items, _ := tr[key].([]map[string]any)
		for _, it := range items {
			rows = append(rows, rowFor(Torrent(it), params[2:]))
		}
		return rows, nil
	case "load.raw_start_verbose", "load.raw_verbose", "load.start_verbose", "load.verbose":
		return int64(0), nil
	case "throttle.global_down.max_rate", "throttle.global_up.max_rate":
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.globals[method], nil
	case "throttle.global_down.max_rate.set_kb", "throttle.global_up.max_rate.set_kb":
		if len(params) != 2 {
			return nil, &rtorrent.Fault{Code: -501, Message: "Wrong number of parameters"}
		}
		kib, ok := params[1].(int64)
		if !ok || kib < 0 || kib*1024 > 4294967294 {
			return nil, &rtorrent.Fault{Code: -500, Message: "Throttle rate must be between 0 and 4294967294."}
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		s.globals[strings.TrimSuffix(method, ".set_kb")] = kib * 1024
		return int64(0), nil
	case "d.start", "d.stop", "d.check_hash", "d.tracker_announce", "d.erase", "d.erase_with_data", "d.custom1.set":
		if len(params) < 1 {
			return nil, &rtorrent.Fault{Code: -501, Message: "Wrong number of parameters"}
		}
		tr, ok := s.find(params[0])
		if !ok {
			return nil, &rtorrent.Fault{Code: -500, Message: "invalid parameters: invalid target"}
		}
		switch method {
		case "d.erase", "d.erase_with_data":
			s.remove(tr)
		case "d.custom1.set":
			if len(params) > 1 {
				s.mu.Lock()
				tr["d.custom1="] = params[1]
				s.mu.Unlock()
			}
		}
		return int64(0), nil
	}
	if strings.HasPrefix(method, "d.") && len(params) >= 1 {
		tr, ok := s.find(params[0])
		if !ok {
			return nil, &rtorrent.Fault{Code: -500, Message: "invalid parameters: invalid target"}
		}
		key := method + "="
		for _, p := range params[1:] {
			key += fmt.Sprint(p) + ","
		}
		key = strings.TrimSuffix(key, ",")
		s.mu.Lock()
		v, ok := tr[key]
		s.mu.Unlock()
		if !ok {
			return defaultFor(key), nil
		}
		return v, nil
	}
	return nil, &rtorrent.Fault{Code: -506, Message: fmt.Sprintf("Method '%s' not defined", method)}
}

func (s *Server) multicall(params []any) (any, error) {
	if len(params) != 1 {
		return nil, &rtorrent.Fault{Code: -501, Message: "Wrong number of parameters"}
	}
	list, ok := params[0].([]any)
	if !ok {
		return nil, &rtorrent.Fault{Code: -501, Message: "system.multicall wants an array"}
	}
	out := make([]any, 0, len(list))
	for _, item := range list {
		m, _ := item.(map[string]any)
		name, _ := m["methodName"].(string)
		sub, _ := m["params"].([]any)
		v, err := s.dispatch(name, sub)
		if err != nil {
			var f *rtorrent.Fault
			if !errors.As(err, &f) {
				f = &rtorrent.Fault{Code: -1, Message: err.Error()}
			}
			out = append(out, map[string]any{"faultCode": f.Code, "faultString": f.Message})
			continue
		}
		out = append(out, []any{v})
	}
	return out, nil
}

// stringFields are the commands rTorrent answers with a string; unset ones
// read as "" rather than 0.
var stringFields = map[string]bool{
	"d.hash=": true, "d.name=": true, "d.custom1=": true, "d.directory=": true, "d.message=": true,
	"t.url=": true, "f.path=": true,
}

// defaultFor is the value of a field the test did not set.
func defaultFor(field string) any {
	if stringFields[field] || strings.HasPrefix(field, "d.custom=") {
		return ""
	}
	return int64(0)
}

// rowFor answers the requested fields for one torrent (or tracker, or
// file), including a nested t.multicall.
func rowFor(tr Torrent, fields []any) []any {
	row := make([]any, 0, len(fields))
	for _, f := range fields {
		field, _ := f.(string)
		if strings.HasPrefix(field, "t.multicall=") {
			sub := strings.Split(strings.TrimPrefix(field, "t.multicall="), ",")
			var subFields []any
			for _, sf := range sub[1:] {
				subFields = append(subFields, sf)
			}
			trackers, _ := tr["trackers"].([]map[string]any)
			nested := []any{}
			for _, t := range trackers {
				nested = append(nested, rowFor(Torrent(t), subFields))
			}
			row = append(row, nested)
			continue
		}
		if v, ok := tr[field]; ok {
			row = append(row, v)
		} else {
			row = append(row, defaultFor(field))
		}
	}
	return row
}

func (s *Server) find(hash any) (Torrent, bool) {
	h, _ := hash.(string)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, tr := range s.torrents {
		if th, _ := tr["d.hash="].(string); strings.EqualFold(th, h) {
			return tr, true
		}
	}
	return nil, false
}

func (s *Server) remove(tr Torrent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.torrents {
		if s.torrents[i]["d.hash="] == tr["d.hash="] {
			s.torrents = append(s.torrents[:i], s.torrents[i+1:]...)
			return
		}
	}
}
