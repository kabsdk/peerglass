package rtorrent

import (
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

// token is the passkey-like path segment in the scrubbed fixtures. It must
// never appear in anything the package exposes (S5).
const token = "0123456789abcdef0123456789abcdef"

func loadRows(t *testing.T, file string) []any {
	t.Helper()
	data, err := os.ReadFile("testdata/" + file)
	if err != nil {
		t.Fatal(err)
	}
	v, err := decodeResponse(data)
	if err != nil {
		t.Fatal(err)
	}
	rows, ok := v.([]any)
	if !ok {
		t.Fatalf("fixture is %T, want array", v)
	}
	return rows
}

func TestParseTorrentFixture(t *testing.T) {
	rows := loadRows(t, "d.multicall2.xml")
	if len(rows) != 2 {
		t.Fatalf("got %d rows", len(rows))
	}
	first, err := parseTorrent(rows[0].([]any))
	if err != nil {
		t.Fatal(err)
	}
	if first.Hash != "7ACF8FB590B2060DD9C3146EF770169D593433B0" || first.Name != "debian-13.7.0-amd64-netinst.iso" {
		t.Errorf("identity: %+v", first)
	}
	if first.Size != 792723456 || first.Completed != 0 || first.Started || first.Active || first.Complete {
		t.Errorf("flags: %+v", first)
	}
	if first.Label != "tv" || first.Directory != "/downloads/incomplete" || first.Message != "" {
		t.Errorf("strings: %+v", first)
	}
	if first.Added.Unix() != 1793480812 || !first.Finished.IsZero() {
		t.Errorf("dates: added %v finished %v", first.Added, first.Finished)
	}
	if first.TrackerHost != "tracker.example.invalid" {
		t.Errorf("TrackerHost = %q", first.TrackerHost)
	}
	if !reflect.DeepEqual(first.Trackers, []string{"http://tracker.example.invalid:6969"}) {
		t.Errorf("Trackers = %q", first.Trackers)
	}
	if first.State() != StateStopped || first.Status() != "Stopped" {
		t.Errorf("state %v status %q", first.State(), first.Status())
	}
	if first.Path() != "/downloads/incomplete/debian-13.7.0-amd64-netinst.iso" {
		t.Errorf("Path = %q", first.Path())
	}

	second, err := parseTorrent(rows[1].([]any))
	if err != nil {
		t.Fatal(err)
	}
	if second.State() != StateSeeding {
		t.Errorf("state = %v", second.State())
	}
	if second.Ratio != 2.5 || second.UpRate != 204800 || second.Peers != 3 || second.Seeds != 1 || second.Leechers != 2 {
		t.Errorf("numbers: %+v", second)
	}
	if !second.MultiFile || !second.Private || second.Path() != "/downloads/movies/Example (2020)" {
		t.Errorf("multi-file path: %+v", second)
	}
	if second.Finished.Unix() != 1700003600 || second.Added.Unix() != 1700000000 {
		t.Errorf("dates: %+v", second)
	}
	// The first tracker is disabled, so the enabled one wins the column.
	if second.TrackerHost != "private.example.invalid" {
		t.Errorf("TrackerHost = %q", second.TrackerHost)
	}
	if !reflect.DeepEqual(second.Trackers, []string{"udp://disabled.example.invalid:1337", "https://private.example.invalid"}) {
		t.Errorf("Trackers = %q", second.Trackers)
	}
	if !strings.HasPrefix(second.Status(), "Seeding to 3 peers — Tracker: [Failure reason") {
		t.Errorf("Status = %q", second.Status())
	}
	if second.Progress() != 100 {
		t.Errorf("Progress = %v", second.Progress())
	}
	for _, s := range []string{second.TrackerHost, strings.Join(second.Trackers, " "), second.Status()} {
		if strings.Contains(s, token) {
			t.Errorf("passkey leaked into %q", s)
		}
	}
}

func TestParseTorrentWrongWidth(t *testing.T) {
	if _, err := parseTorrent([]any{"x"}); err == nil {
		t.Fatal("expected an error for a short row")
	}
}

func TestParseTrackerFixture(t *testing.T) {
	rows := loadRows(t, "t.multicall.xml")
	tr, err := parseTracker(rows[0].([]any))
	if err != nil {
		t.Fatal(err)
	}
	want := Tracker{
		Origin: "http://tracker.example.invalid:6969", Host: "tracker.example.invalid", Enabled: true,
		Success: 0, Failed: 0, Seeders: 1018, Leechers: 6, LastScrape: time.Unix(1793480855, 0),
	}
	if !reflect.DeepEqual(tr, want) {
		t.Fatalf("got %+v\nwant %+v", tr, want)
	}
	if strings.Contains(tr.Origin, token) {
		t.Fatal("passkey leaked")
	}
}

func TestParseFileFixture(t *testing.T) {
	rows := loadRows(t, "f.multicall.xml")
	f, err := parseFile(rows[0].([]any))
	if err != nil {
		t.Fatal(err)
	}
	want := File{Path: "debian-13.7.0-amd64-netinst.iso", Size: 792723456, CompletedChunks: 0, SizeChunks: 3024, Priority: 1}
	if f != want {
		t.Fatalf("got %+v", f)
	}
	if f.Progress() != 0 || f.PriorityLabel() != "Normal" {
		t.Fatalf("derived: %v %q", f.Progress(), f.PriorityLabel())
	}
	if (File{Priority: 0}).PriorityLabel() != "Off" || (File{Priority: 2}).PriorityLabel() != "High" {
		t.Fatal("priority labels")
	}
	if (File{CompletedChunks: 1, SizeChunks: 4}).Progress() != 25 {
		t.Fatal("progress")
	}
}

func TestTrackerHostAndOrigin(t *testing.T) {
	tests := []struct{ in, host, origin string }{
		{"http://tracker.example.invalid:6969/" + token + "/announce", "tracker.example.invalid", "http://tracker.example.invalid:6969"},
		{"https://Private.Example.invalid/announce?passkey=" + token, "private.example.invalid", "https://private.example.invalid"},
		{"udp://tracker.example.invalid:1337/announce", "tracker.example.invalid", "udp://tracker.example.invalid:1337"},
		{"  http://spaced.example.invalid/a ", "spaced.example.invalid", "http://spaced.example.invalid"},
		{"not a url", "", ""},
		{"", "", ""},
		{"/relative/path", "", ""},
		{"http://[::1", "", ""},
	}
	for _, tc := range tests {
		if got := TrackerHost(tc.in); got != tc.host {
			t.Errorf("TrackerHost(%q) = %q, want %q", tc.in, got, tc.host)
		}
		if got := TrackerOrigin(tc.in); got != tc.origin {
			t.Errorf("TrackerOrigin(%q) = %q, want %q", tc.in, got, tc.origin)
		}
	}
}

func TestStateAndStatus(t *testing.T) {
	tests := []struct {
		name   string
		t      Torrent
		state  State
		status string
	}{
		{"stopped", Torrent{}, StateStopped, "Stopped"},
		{"stopped with message", Torrent{Message: "Tracker: [x]"}, StateStopped, "Stopped — Tracker: [x]"},
		{"checking", Torrent{Started: true, Active: true, HashChecking: true, ChunksHashed: 50, ChunksTotal: 200}, StateChecking, "Checking, 25% done"},
		{"queued check", Torrent{Started: true, Hashing: 1}, StateChecking, "Waiting to be checked"},
		{"inactive", Torrent{Started: true, Active: false}, StateInactive, "Started but not active; rTorrent closed it"},
		{"seeding idle", Torrent{Started: true, Active: true, Complete: true, Peers: 2}, StateSeeding, "Seeding, 2 peers connected, nothing requested"},
		{"seeding alone", Torrent{Started: true, Active: true, Complete: true}, StateSeeding, "Seeding, no peers connected"},
		{"seeding", Torrent{Started: true, Active: true, Complete: true, Peers: 1, UpRate: 5}, StateSeeding, "Seeding to 1 peer"},
		{"downloading alone", Torrent{Started: true, Active: true}, StateDownloading, "Downloading, no peers connected"},
		{"no seeds", Torrent{Started: true, Active: true, Peers: 4}, StateDownloading, "Downloading, no seeds among 4 connected peers"},
		{"stalled", Torrent{Started: true, Active: true, Peers: 4, Seeds: 1}, StateDownloading, "Downloading, 4 peers connected, stalled"},
		{"downloading", Torrent{Started: true, Active: true, Peers: 4, Seeds: 2, DownRate: 100}, StateDownloading, "Downloading from 4 peers, 2 seeds"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.t.State(); got != tc.state {
				t.Errorf("State = %v, want %v", got, tc.state)
			}
			if got := tc.t.Status(); got != tc.status {
				t.Errorf("Status = %q, want %q", got, tc.status)
			}
		})
	}
	for _, s := range []State{StateStopped, StateChecking, StateDownloading, StateSeeding, StateInactive} {
		if s.String() == "" || s.Label() == "" {
			t.Errorf("state %d has empty names", s)
		}
	}
}

func TestProgress(t *testing.T) {
	if (&Torrent{Size: 200, Completed: 50}).Progress() != 25 {
		t.Fatal("25%")
	}
	if (&Torrent{}).Progress() != 0 {
		t.Fatal("zero size")
	}
}

func TestQuote(t *testing.T) {
	tests := map[string]string{
		`plain`:            `"plain"`,
		`/downloads/a, b`:  `"/downloads/a, b"`,
		`say "hi"`:         `"say \"hi\""`,
		`back\slash`:       `"back\\slash"`,
		`"; d.erase=; x="`: `"\"; d.erase=; x=\""`,
		``:                 `""`,
	}
	for in, want := range tests {
		if got := quote(in); got != want {
			t.Errorf("quote(%q) = %s, want %s", in, got, want)
		}
	}
	if got := setCommand("d.directory.set", `/downloads/tv/It's "fine", really`); got != `d.directory.set="/downloads/tv/It's \"fine\", really"` {
		t.Errorf("setCommand = %s", got)
	}
}

func TestLoadArgs(t *testing.T) {
	got := loadArgs([]byte("x"), LoadOptions{Directory: "/downloads/tv", Label: "tv", Start: true})
	want := []any{"", []byte("x"), `d.directory.set="/downloads/tv"`, `d.custom1.set="tv"`}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v", got)
	}
	got = loadArgs("magnet:?xt=urn:btih:x", LoadOptions{})
	if !reflect.DeepEqual(got, []any{"", "magnet:?xt=urn:btih:x"}) {
		t.Errorf("defaults: got %#v", got)
	}
}

func TestFieldCall(t *testing.T) {
	tests := []struct {
		field string
		want  call
	}{
		{"d.name=", call{method: "d.name", args: []any{"H"}}},
		{"d.custom=addtime", call{method: "d.custom", args: []any{"H", "addtime"}}},
		{"t.multicall=,t.url=,t.is_enabled=", call{method: "t.multicall", args: []any{"H", "", "t.url=", "t.is_enabled="}}},
	}
	for _, tc := range tests {
		if got := fieldCall(tc.field, "H"); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("fieldCall(%q) = %#v, want %#v", tc.field, got, tc.want)
		}
	}
}

func TestValidHash(t *testing.T) {
	if !ValidHash("7ACF8FB590B2060DD9C3146EF770169D593433B0") || !ValidHash("7acf8fb590b2060dd9c3146ef770169d593433b0") {
		t.Fatal("valid hashes rejected")
	}
	for _, h := range []string{"", "7ACF", "7ACF8FB590B2060DD9C3146EF770169D593433BG", "7ACF8FB590B2060DD9C3146EF770169D593433B0 "} {
		if ValidHash(h) {
			t.Errorf("ValidHash(%q) accepted", h)
		}
	}
	if h, err := checkHash("7acf8fb590b2060dd9c3146ef770169d593433b0"); err != nil || h != "7ACF8FB590B2060DD9C3146EF770169D593433B0" {
		t.Fatal("checkHash should upper-case")
	}
}

func TestAsHelpers(t *testing.T) {
	if asInt("1793480812") != 1793480812 || asInt(int64(3)) != 3 || asInt(true) != 1 || asInt(nil) != 0 || asInt("junk") != 0 {
		t.Fatal("asInt")
	}
	if asString(int64(5)) != "5" || asString([]byte("b")) != "b" || asString(nil) != "" {
		t.Fatal("asString")
	}
}
