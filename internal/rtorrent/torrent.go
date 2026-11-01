package rtorrent

import (
	"fmt"
	"net/url"
	"path"
	"strings"
	"time"
)

// State is what a torrent is doing, derived from rTorrent's flags.
type State int

const (
	StateStopped State = iota
	StateChecking
	StateDownloading
	StateSeeding
	// StateInactive is started but not active: rTorrent closed it (for
	// example when disk space ran low) or could not open it.
	StateInactive
)

// String is the machine form used in filters and CSS classes.
func (s State) String() string {
	switch s {
	case StateChecking:
		return "checking"
	case StateDownloading:
		return "downloading"
	case StateSeeding:
		return "seeding"
	case StateInactive:
		return "inactive"
	}
	return "stopped"
}

// Label is the human form shown in the table.
func (s State) Label() string {
	switch s {
	case StateChecking:
		return "Checking"
	case StateDownloading:
		return "Downloading"
	case StateSeeding:
		return "Seeding"
	case StateInactive:
		return "Inactive"
	}
	return "Stopped"
}

// Torrent is one row of d.multicall2, interpreted.
type Torrent struct {
	Hash      string
	Name      string
	Size      int64
	Completed int64
	// Started is d.state: the user wants it running.
	Started bool
	// Active is d.is_active: started and open.
	Active bool
	Open   bool
	// Complete is d.complete: all wanted data is present.
	Complete     bool
	HashChecking bool
	// Hashing is d.hashing: non-zero while a check is pending or running.
	Hashing      int64
	ChunksHashed int64
	ChunksTotal  int64
	DownRate     int64
	UpRate       int64
	// Ratio is upload divided by download; rTorrent reports it times 1000.
	Ratio     float64
	Label     string
	Directory string
	// Message is d.message, rTorrent's explanation when something is wrong.
	Message string
	// Added comes from the addtime custom value rtorrent.rc sets; zero when
	// unknown.
	Added    time.Time
	Finished time.Time
	// Peers are connections right now; Seeds and Leechers split them.
	Peers     int64
	Seeds     int64
	Leechers  int64
	MultiFile bool
	Private   bool
	// TrackerHost is the host of the first enabled tracker (S5: never the
	// full URL). Empty when the torrent has no trackers.
	TrackerHost string
	// Trackers are the origins (scheme://host) of all trackers, enabled or not.
	Trackers []string
}

// State derives what the torrent is doing from rTorrent's flags.
func (t *Torrent) State() State {
	switch {
	case t.HashChecking || t.Hashing != 0:
		return StateChecking
	case !t.Started:
		return StateStopped
	case !t.Active:
		return StateInactive
	case t.Complete:
		return StateSeeding
	}
	return StateDownloading
}

// Status says in words why the torrent is in its state, followed by
// rTorrent's message when there is one (F2).
func (t *Torrent) Status() string {
	var s string
	switch t.State() {
	case StateChecking:
		if t.HashChecking && t.ChunksTotal > 0 {
			s = fmt.Sprintf("Checking, %d%% done", t.ChunksHashed*100/t.ChunksTotal)
		} else {
			s = "Waiting to be checked"
		}
	case StateStopped:
		s = "Stopped"
	case StateInactive:
		s = "Started but not active; rTorrent closed it"
	case StateSeeding:
		switch {
		case t.Peers == 0:
			s = "Seeding, no peers connected"
		case t.UpRate == 0:
			s = fmt.Sprintf("Seeding, %s connected, nothing requested", plural(t.Peers, "peer"))
		default:
			s = fmt.Sprintf("Seeding to %s", plural(t.Peers, "peer"))
		}
	case StateDownloading:
		switch {
		case t.Peers == 0:
			s = "Downloading, no peers connected"
		case t.Seeds == 0 && t.DownRate == 0:
			s = fmt.Sprintf("Downloading, no seeds among %s", plural(t.Peers, "connected peer"))
		case t.DownRate == 0:
			s = fmt.Sprintf("Downloading, %s connected, stalled", plural(t.Peers, "peer"))
		default:
			s = fmt.Sprintf("Downloading from %s, %s", plural(t.Peers, "peer"), plural(t.Seeds, "seed"))
		}
	}
	if t.Message != "" {
		s += " — " + t.Message
	}
	return s
}

// Progress is the completed share in percent.
func (t *Torrent) Progress() float64 {
	if t.Size <= 0 {
		return 0
	}
	return float64(t.Completed) * 100 / float64(t.Size)
}

// Path is where the data is: the directory itself for a multi-file torrent,
// the file inside it otherwise.
func (t *Torrent) Path() string {
	if t.MultiFile || t.Name == "" {
		return t.Directory
	}
	return path.Join(t.Directory, t.Name)
}

func plural(n int64, noun string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// torrentFields are the d.multicall2 commands behind Torrent. parseTorrent
// reads the row in this order, and Client.Torrent issues the same commands
// one by one, so the three must change together.
var torrentFields = []string{
	"d.hash=",
	"d.name=",
	"d.size_bytes=",
	"d.completed_bytes=",
	"d.state=",
	"d.is_active=",
	"d.is_open=",
	"d.complete=",
	"d.is_hash_checking=",
	"d.hashing=",
	"d.chunks_hashed=",
	"d.size_chunks=",
	"d.down.rate=",
	"d.up.rate=",
	"d.ratio=",
	"d.custom1=",
	"d.directory=",
	"d.message=",
	"d.custom=addtime",
	"d.timestamp.finished=",
	"d.peers_connected=",
	"d.peers_complete=",
	"d.peers_accounted=",
	"d.is_multi_file=",
	"d.is_private=",
	// Nested call: one [url, enabled] pair per tracker, so the list needs
	// no call per torrent for the tracker column.
	"t.multicall=,t.url=,t.is_enabled=",
}

// parseTorrent interprets one d.multicall2 row.
func parseTorrent(row []any) (Torrent, error) {
	if len(row) != len(torrentFields) {
		return Torrent{}, fmt.Errorf("torrent row has %d fields, want %d", len(row), len(torrentFields))
	}
	t := Torrent{
		Hash:         asString(row[0]),
		Name:         asString(row[1]),
		Size:         asInt(row[2]),
		Completed:    asInt(row[3]),
		Started:      asInt(row[4]) == 1,
		Active:       asInt(row[5]) == 1,
		Open:         asInt(row[6]) == 1,
		Complete:     asInt(row[7]) == 1,
		HashChecking: asInt(row[8]) == 1,
		Hashing:      asInt(row[9]),
		ChunksHashed: asInt(row[10]),
		ChunksTotal:  asInt(row[11]),
		DownRate:     asInt(row[12]),
		UpRate:       asInt(row[13]),
		Ratio:        float64(asInt(row[14])) / 1000,
		Label:        asString(row[15]),
		Directory:    asString(row[16]),
		Message:      asString(row[17]),
		Peers:        asInt(row[20]),
		Seeds:        asInt(row[21]),
		Leechers:     asInt(row[22]),
		MultiFile:    asInt(row[23]) == 1,
		Private:      asInt(row[24]) == 1,
	}
	if ts := asInt(row[18]); ts > 0 {
		t.Added = time.Unix(ts, 0)
	}
	if ts := asInt(row[19]); ts > 0 {
		t.Finished = time.Unix(ts, 0)
	}
	if trackers, ok := row[25].([]any); ok {
		for _, tr := range trackers {
			pair, ok := tr.([]any)
			if !ok || len(pair) < 2 {
				continue
			}
			rawURL := asString(pair[0])
			if origin := TrackerOrigin(rawURL); origin != "" {
				t.Trackers = append(t.Trackers, origin)
			}
			if t.TrackerHost == "" && asInt(pair[1]) == 1 {
				t.TrackerHost = TrackerHost(rawURL)
			}
		}
		if t.TrackerHost == "" && len(trackers) > 0 {
			if pair, ok := trackers[0].([]any); ok && len(pair) > 0 {
				t.TrackerHost = TrackerHost(asString(pair[0]))
			}
		}
	}
	return t, nil
}

// TrackerHost reduces an announce URL to its host name, without port, path
// or query, so a private tracker's passkey never leaves this package (S5).
// It returns "" for anything that does not parse as a URL with a host.
func TrackerHost(rawURL string) string {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Host == "" {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// TrackerOrigin reduces an announce URL to scheme://host[:port] (S5). It
// returns "" for anything that does not parse as a URL with a host.
func TrackerOrigin(rawURL string) string {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Host == "" || u.Scheme == "" {
		return ""
	}
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host)
}

// Tracker is one row of t.multicall, with its URL reduced to an origin.
type Tracker struct {
	// Origin is scheme://host[:port]; the path with the passkey is dropped (S5).
	Origin  string
	Host    string
	Enabled bool
	Success int64
	Failed  int64
	// Seeders and Leechers come from the last scrape; -1 when never scraped.
	Seeders      int64
	Leechers     int64
	LastActivity time.Time
	LastScrape   time.Time
}

var trackerFields = []string{
	"t.url=",
	"t.is_enabled=",
	"t.success_counter=",
	"t.failed_counter=",
	"t.scrape_complete=",
	"t.scrape_incomplete=",
	"t.activity_time_last=",
	"t.scrape_time_last=",
}

func parseTracker(row []any) (Tracker, error) {
	if len(row) != len(trackerFields) {
		return Tracker{}, fmt.Errorf("tracker row has %d fields, want %d", len(row), len(trackerFields))
	}
	rawURL := asString(row[0])
	tr := Tracker{
		Origin:   TrackerOrigin(rawURL),
		Host:     TrackerHost(rawURL),
		Enabled:  asInt(row[1]) == 1,
		Success:  asInt(row[2]),
		Failed:   asInt(row[3]),
		Seeders:  asInt(row[4]),
		Leechers: asInt(row[5]),
	}
	if ts := asInt(row[6]); ts > 0 {
		tr.LastActivity = time.Unix(ts, 0)
	}
	if ts := asInt(row[7]); ts > 0 {
		tr.LastScrape = time.Unix(ts, 0)
	}
	return tr, nil
}

// File is one row of f.multicall.
type File struct {
	Path            string
	Size            int64
	CompletedChunks int64
	SizeChunks      int64
	// Priority is rTorrent's: 0 off, 1 normal, 2 high.
	Priority int64
}

// Progress is the completed share in percent.
func (f File) Progress() float64 {
	if f.SizeChunks <= 0 {
		return 0
	}
	return float64(f.CompletedChunks) * 100 / float64(f.SizeChunks)
}

// PriorityLabel is the priority in words.
func (f File) PriorityLabel() string {
	switch f.Priority {
	case 0:
		return "Off"
	case 2:
		return "High"
	}
	return "Normal"
}

var fileFields = []string{
	"f.path=",
	"f.size_bytes=",
	"f.completed_chunks=",
	"f.size_chunks=",
	"f.priority=",
}

func parseFile(row []any) (File, error) {
	if len(row) != len(fileFields) {
		return File{}, fmt.Errorf("file row has %d fields, want %d", len(row), len(fileFields))
	}
	return File{
		Path:            asString(row[0]),
		Size:            asInt(row[1]),
		CompletedChunks: asInt(row[2]),
		SizeChunks:      asInt(row[3]),
		Priority:        asInt(row[4]),
	}, nil
}
