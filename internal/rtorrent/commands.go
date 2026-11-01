package rtorrent

import (
	"context"
	"fmt"
	"strings"
)

// Version returns rTorrent's version string (system.client_version).
func (c *Client) Version(ctx context.Context) (string, error) {
	v, err := c.Call(ctx, "system.client_version")
	if err != nil {
		return "", err
	}
	return asString(v), nil
}

// Torrents lists every torrent in one d.multicall2 (N1).
func (c *Client) Torrents(ctx context.Context) ([]Torrent, error) {
	v, err := c.Call(ctx, "d.multicall2", listArgs()...)
	if err != nil {
		return nil, err
	}
	return parseTorrents(v)
}

func listArgs() []any {
	args := []any{"", "main"}
	for _, f := range torrentFields {
		args = append(args, f)
	}
	return args
}

func parseTorrents(v any) ([]Torrent, error) {
	rows, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("rtorrent: d.multicall2 returned %T, want array", v)
	}
	out := make([]Torrent, 0, len(rows))
	for i, r := range rows {
		row, ok := r.([]any)
		if !ok {
			return nil, fmt.Errorf("rtorrent: d.multicall2 row %d is %T, want array", i, r)
		}
		t, err := parseTorrent(row)
		if err != nil {
			return nil, fmt.Errorf("rtorrent: d.multicall2 row %d: %w", i, err)
		}
		out = append(out, t)
	}
	return out, nil
}

// Limits are rTorrent's global transfer limits in bytes per second; 0 means
// unlimited.
type Limits struct {
	Down, Up int64
	// Known is false when rTorrent would not say, which leaves the limits
	// out of the page rather than claiming "unlimited".
	Known bool
}

// MaxLimitKiB is the highest limit rTorrent accepts, in KiB/s: it refuses
// rates above 4,294,967,294 bytes per second.
const MaxLimitKiB = 4194303

// Overview is what the torrent list needs: every torrent and the global
// limits.
type Overview struct {
	Torrents []Torrent
	Limits   Limits
}

// Overview fetches the torrents (one d.multicall2, N1) and the global
// limits in a single system.multicall, so a poll costs rTorrent one request.
func (c *Client) Overview(ctx context.Context) (Overview, error) {
	results, faults, err := c.multicall(ctx, []call{
		{method: "d.multicall2", args: listArgs()},
		{method: "throttle.global_down.max_rate", args: []any{""}},
		{method: "throttle.global_up.max_rate", args: []any{""}},
	})
	if err != nil {
		return Overview{}, err
	}
	if faults[0] != nil {
		return Overview{}, faults[0]
	}
	torrents, err := parseTorrents(results[0])
	if err != nil {
		return Overview{}, err
	}
	o := Overview{Torrents: torrents}
	if faults[1] == nil && faults[2] == nil {
		o.Limits = Limits{Down: asInt(results[1]), Up: asInt(results[2]), Known: true}
	}
	return o, nil
}

// SetLimits sets the global download and upload limits in KiB/s; 0 means
// unlimited. rTorrent keeps them until it restarts: rtorrent.rc sets the
// limits it starts with (F13).
func (c *Client) SetLimits(ctx context.Context, downKiB, upKiB int64) error {
	for _, v := range []int64{downKiB, upKiB} {
		if v < 0 || v > MaxLimitKiB {
			return fmt.Errorf("rtorrent: limit %d KiB/s outside 0–%d", v, MaxLimitKiB)
		}
	}
	_, faults, err := c.multicall(ctx, []call{
		{method: "throttle.global_down.max_rate.set_kb", args: []any{"", downKiB}},
		{method: "throttle.global_up.max_rate.set_kb", args: []any{"", upKiB}},
	})
	if err != nil {
		return err
	}
	for _, f := range faults {
		if f != nil {
			return f
		}
	}
	return nil
}

// Torrent fetches one torrent with the same fields as Torrents, through one
// system.multicall.
func (c *Client) Torrent(ctx context.Context, hash string) (Torrent, error) {
	hash, err := checkHash(hash)
	if err != nil {
		return Torrent{}, err
	}
	calls := make([]call, len(torrentFields))
	for i, f := range torrentFields {
		calls[i] = fieldCall(f, hash)
	}
	results, faults, err := c.multicall(ctx, calls)
	if err != nil {
		return Torrent{}, err
	}
	for _, f := range faults {
		if f != nil {
			return Torrent{}, f
		}
	}
	return parseTorrent(results)
}

// fieldCall turns a multicall field such as "d.custom=addtime" or
// "t.multicall=,t.url=" into a call against one hash.
func fieldCall(field, hash string) call {
	method, rest, _ := strings.Cut(field, "=")
	args := []any{hash}
	if rest != "" {
		for _, a := range strings.Split(rest, ",") {
			args = append(args, a)
		}
	}
	return call{method: method, args: args}
}

// Trackers lists a torrent's trackers (t.multicall).
func (c *Client) Trackers(ctx context.Context, hash string) ([]Tracker, error) {
	hash, err := checkHash(hash)
	if err != nil {
		return nil, err
	}
	args := []any{hash, ""}
	for _, f := range trackerFields {
		args = append(args, f)
	}
	v, err := c.Call(ctx, "t.multicall", args...)
	if err != nil {
		return nil, err
	}
	rows, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("rtorrent: t.multicall returned %T, want array", v)
	}
	out := make([]Tracker, 0, len(rows))
	for i, r := range rows {
		row, ok := r.([]any)
		if !ok {
			return nil, fmt.Errorf("rtorrent: t.multicall row %d is %T, want array", i, r)
		}
		tr, err := parseTracker(row)
		if err != nil {
			return nil, fmt.Errorf("rtorrent: t.multicall row %d: %w", i, err)
		}
		out = append(out, tr)
	}
	return out, nil
}

// Files lists a torrent's files (f.multicall).
func (c *Client) Files(ctx context.Context, hash string) ([]File, error) {
	hash, err := checkHash(hash)
	if err != nil {
		return nil, err
	}
	args := []any{hash, ""}
	for _, f := range fileFields {
		args = append(args, f)
	}
	v, err := c.Call(ctx, "f.multicall", args...)
	if err != nil {
		return nil, err
	}
	rows, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("rtorrent: f.multicall returned %T, want array", v)
	}
	out := make([]File, 0, len(rows))
	for i, r := range rows {
		row, ok := r.([]any)
		if !ok {
			return nil, fmt.Errorf("rtorrent: f.multicall row %d is %T, want array", i, r)
		}
		f, err := parseFile(row)
		if err != nil {
			return nil, fmt.Errorf("rtorrent: f.multicall row %d: %w", i, err)
		}
		out = append(out, f)
	}
	return out, nil
}

// FinishedDir asks where a torrent will be moved when it finishes. The
// method is defined in rtorrent.rc, so a *Fault means this rTorrent lacks it.
func (c *Client) FinishedDir(ctx context.Context, hash string) (string, error) {
	hash, err := checkHash(hash)
	if err != nil {
		return "", err
	}
	v, err := c.Call(ctx, "d.finished_dir", hash)
	if err != nil {
		return "", err
	}
	return asString(v), nil
}

// Start starts a torrent (d.start).
func (c *Client) Start(ctx context.Context, hash string) error {
	return c.simple(ctx, "d.start", hash)
}

// Stop stops a torrent (d.stop).
func (c *Client) Stop(ctx context.Context, hash string) error {
	return c.simple(ctx, "d.stop", hash)
}

// CheckHash rechecks a torrent's data (d.check_hash).
func (c *Client) CheckHash(ctx context.Context, hash string) error {
	return c.simple(ctx, "d.check_hash", hash)
}

// Reannounce asks rTorrent to announce a torrent to its trackers now
// (d.tracker_announce). It is rTorrent's ordinary manual announce, which
// still honours a tracker's minimum interval; d.tracker_announce.force, which
// does not, is deliberately not used, to stay polite to private trackers.
func (c *Client) Reannounce(ctx context.Context, hash string) error {
	return c.simple(ctx, "d.tracker_announce", hash)
}

// Erase removes a torrent and keeps its data (d.erase).
func (c *Client) Erase(ctx context.Context, hash string) error {
	return c.simple(ctx, "d.erase", hash)
}

// EraseWithData removes a torrent and its data. rTorrent does the deleting
// through d.erase_with_data, a method rtorrent.rc defines (F10, S6); a
// *Fault means this rTorrent lacks it.
func (c *Client) EraseWithData(ctx context.Context, hash string) error {
	return c.simple(ctx, "d.erase_with_data", hash)
}

// SetLabel sets a torrent's label (d.custom1.set). An empty label clears it.
func (c *Client) SetLabel(ctx context.Context, hash, label string) error {
	hash, err := checkHash(hash)
	if err != nil {
		return err
	}
	_, err = c.Call(ctx, "d.custom1.set", hash, label)
	return err
}

func (c *Client) simple(ctx context.Context, method, hash string) error {
	hash, err := checkHash(hash)
	if err != nil {
		return err
	}
	_, err = c.Call(ctx, method, hash)
	return err
}

// LoadOptions are the choices offered when adding a torrent (F6).
type LoadOptions struct {
	// Directory is the destination, or "" for rTorrent's default.
	Directory string
	// Label is stored in d.custom1, or "" for none.
	Label string
	// Start starts the torrent right away.
	Start bool
}

// loadArgs builds the arguments after the target for a load.* call: the
// payload, then one quoted command per option (docs/rtorrent.md, Actions).
func loadArgs(payload any, opts LoadOptions) []any {
	args := []any{"", payload}
	if opts.Directory != "" {
		args = append(args, setCommand("d.directory.set", opts.Directory))
	}
	if opts.Label != "" {
		args = append(args, setCommand("d.custom1.set", opts.Label))
	}
	return args
}

// LoadTorrent adds a .torrent file's contents (load.raw_start_verbose or
// load.raw_verbose). The verbose variants report a broken file as a *Fault.
func (c *Client) LoadTorrent(ctx context.Context, data []byte, opts LoadOptions) error {
	method := "load.raw_verbose"
	if opts.Start {
		method = "load.raw_start_verbose"
	}
	_, err := c.Call(ctx, method, loadArgs(data, opts)...)
	return err
}

// LoadMagnet adds a magnet link (load.start_verbose or load.verbose). The
// caller validates the scheme (S8); this only passes the link on.
func (c *Client) LoadMagnet(ctx context.Context, uri string, opts LoadOptions) error {
	method := "load.verbose"
	if opts.Start {
		method = "load.start_verbose"
	}
	_, err := c.Call(ctx, method, loadArgs(uri, opts)...)
	return err
}
