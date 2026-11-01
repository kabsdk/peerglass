package rtorrent_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kabsdk/peerglass/internal/rtorrent"
	"github.com/kabsdk/peerglass/internal/rtorrent/rtorrenttest"
)

const hashA = "7ACF8FB590B2060DD9C3146EF770169D593433B0"

func fakeTorrent() rtorrenttest.Torrent {
	return rtorrenttest.Torrent{
		"d.hash=": hashA, "d.name=": "a.iso", "d.size_bytes=": int64(100), "d.completed_bytes=": int64(50),
		"d.state=": int64(1), "d.is_active=": int64(1), "d.custom1=": "tv", "d.directory=": "/downloads/incomplete",
		"d.custom=addtime": "1700000000", "d.ratio=": int64(1500),
		"trackers": []map[string]any{{"t.url=": "http://t.example.invalid/secret/announce", "t.is_enabled=": int64(1), "t.scrape_complete=": int64(9)}},
		"files":    []map[string]any{{"f.path=": "a.iso", "f.size_bytes=": int64(100), "f.size_chunks=": int64(4), "f.completed_chunks=": int64(2), "f.priority=": int64(1)}},
	}
}

func TestClientAgainstFake(t *testing.T) {
	fake := rtorrenttest.New(t)
	fake.SetTorrents(fakeTorrent())
	c := rtorrent.New(fake.Socket())
	ctx := context.Background()

	v, err := c.Version(ctx)
	if err != nil || v != "0.16.27" {
		t.Fatalf("Version = %q, %v", v, err)
	}

	ts, err := c.Torrents(ctx)
	if err != nil || len(ts) != 1 {
		t.Fatalf("Torrents = %v, %v", ts, err)
	}
	if ts[0].Name != "a.iso" || ts[0].Ratio != 1.5 || ts[0].TrackerHost != "t.example.invalid" || ts[0].Added.Unix() != 1700000000 {
		t.Errorf("parsed: %+v", ts[0])
	}
	calls := fake.CallsTo("d.multicall2")
	if len(calls) != 1 || calls[0].Params[0] != "" || calls[0].Params[1] != "main" || calls[0].Params[2] != "d.hash=" {
		t.Errorf("d.multicall2 params: %v", calls)
	}

	one, err := c.Torrent(ctx, strings.ToLower(hashA))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(one, ts[0]) {
		t.Errorf("Torrent() differs from the list row:\n%+v\n%+v", one, ts[0])
	}
	if len(fake.CallsTo("system.multicall")) != 1 {
		t.Error("Torrent() should use one system.multicall")
	}
	if _, err := c.Torrent(ctx, "0000000000000000000000000000000000000000"); err == nil {
		t.Error("unknown hash should fault")
	}

	trackers, err := c.Trackers(ctx, hashA)
	if err != nil || len(trackers) != 1 || trackers[0].Origin != "http://t.example.invalid" || trackers[0].Seeders != 9 {
		t.Errorf("Trackers = %+v, %v", trackers, err)
	}
	files, err := c.Files(ctx, hashA)
	if err != nil || len(files) != 1 || files[0].Progress() != 50 {
		t.Errorf("Files = %+v, %v", files, err)
	}

	if err := c.SetLabel(ctx, hashA, "movies"); err != nil {
		t.Fatal(err)
	}
	if got := fake.Torrents()[0]["d.custom1="]; got != "movies" {
		t.Errorf("label after SetLabel = %v", got)
	}
	for _, step := range []struct {
		name string
		fn   func(context.Context, string) error
	}{{"d.start", c.Start}, {"d.stop", c.Stop}, {"d.check_hash", c.CheckHash}, {"d.tracker_announce", c.Reannounce}} {
		if err := step.fn(ctx, hashA); err != nil {
			t.Errorf("%s: %v", step.name, err)
		}
		if got := fake.CallsTo(step.name); len(got) != 1 || got[0].Params[0] != hashA {
			t.Errorf("%s calls = %v", step.name, got)
		}
	}
	if err := c.LoadTorrent(ctx, []byte("d4:infoe"), rtorrent.LoadOptions{Directory: "/downloads/tv", Label: "tv", Start: true}); err != nil {
		t.Fatal(err)
	}
	load := fake.CallsTo("load.raw_start_verbose")
	want := []any{"", []byte("d4:infoe"), `d.directory.set="/downloads/tv"`, `d.custom1.set="tv"`}
	if len(load) != 1 || !reflect.DeepEqual(load[0].Params, want) {
		t.Errorf("load params = %#v", load)
	}
	if err := c.LoadMagnet(ctx, "magnet:?xt=urn:btih:"+hashA, rtorrent.LoadOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := fake.CallsTo("load.verbose"); len(got) != 1 || !reflect.DeepEqual(got[0].Params, []any{"", "magnet:?xt=urn:btih:" + hashA}) {
		t.Errorf("magnet params = %#v", got)
	}
	if err := c.Erase(ctx, hashA); err != nil {
		t.Fatal(err)
	}
	if len(fake.Torrents()) != 0 {
		t.Error("erase should remove the torrent")
	}
	if err := c.Start(ctx, "nothex"); err == nil || len(fake.CallsTo("d.start")) != 1 {
		t.Error("an invalid hash must be rejected before any call")
	}
}

func TestOverviewAndLimits(t *testing.T) {
	fake := rtorrenttest.New(t)
	fake.SetTorrents(fakeTorrent())
	c := rtorrent.New(fake.Socket())
	ctx := context.Background()

	o, err := c.Overview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(o.Torrents) != 1 || o.Torrents[0].Name != "a.iso" || o.Limits != (rtorrent.Limits{Known: true}) {
		t.Fatalf("overview = %+v", o)
	}
	if calls := fake.Calls(); len(calls) != 1 || calls[0].Method != "system.multicall" {
		t.Fatalf("overview should be one system.multicall, got %v", calls)
	}

	if err := c.SetLimits(ctx, 512, 0); err != nil {
		t.Fatal(err)
	}
	if fake.Global("throttle.global_down.max_rate") != 512*1024 || fake.Global("throttle.global_up.max_rate") != 0 {
		t.Fatal("limits not set in KiB")
	}
	set := fake.CallsTo("system.multicall")
	params := set[len(set)-1].Params[0].([]any)
	first := params[0].(map[string]any)
	if first["methodName"] != "throttle.global_down.max_rate.set_kb" || !reflect.DeepEqual(first["params"], []any{"", int64(512)}) {
		t.Errorf("set call = %#v", first)
	}
	if o, _ := c.Overview(ctx); o.Limits != (rtorrent.Limits{Down: 512 * 1024, Known: true}) {
		t.Errorf("limits read back = %+v", o.Limits)
	}
	before := len(fake.Calls())
	for _, bad := range [][2]int64{{-1, 0}, {0, rtorrent.MaxLimitKiB + 1}} {
		if err := c.SetLimits(ctx, bad[0], bad[1]); err == nil {
			t.Errorf("SetLimits(%d, %d) accepted", bad[0], bad[1])
		}
	}
	if len(fake.Calls()) != before {
		t.Error("out-of-range limits must not reach rTorrent")
	}
	if err := c.SetLimits(ctx, rtorrent.MaxLimitKiB, rtorrent.MaxLimitKiB); err != nil {
		t.Errorf("the maximum should be accepted: %v", err)
	}

	// An rTorrent without the throttle commands still lists torrents.
	fake.Handle("throttle.global_up.max_rate", func([]any) (any, error) {
		return nil, &rtorrent.Fault{Code: -506, Message: "Method 'throttle.global_up.max_rate' not defined"}
	})
	if o, err := c.Overview(ctx); err != nil || len(o.Torrents) != 1 || o.Limits.Known {
		t.Errorf("faulting limits: %+v, %v", o, err)
	}
	fake.Handle("d.multicall2", func([]any) (any, error) {
		return nil, &rtorrent.Fault{Code: -501, Message: "bad"}
	})
	if _, err := c.Overview(ctx); err == nil {
		t.Error("a faulting d.multicall2 must fail the overview")
	}
}

func TestClientFaultAndUnavailable(t *testing.T) {
	fake := rtorrenttest.New(t)
	c := rtorrent.New(fake.Socket())
	_, err := c.Call(context.Background(), "no.such_method")
	var f *rtorrent.Fault
	if !errors.As(err, &f) || !strings.Contains(f.Message, "no.such_method") {
		t.Fatalf("expected a fault, got %v", err)
	}
	if err := c.EraseWithData(context.Background(), hashA); !errors.As(err, &f) {
		t.Fatalf("missing torrent should fault, got %v", err)
	}

	fake.Stop()
	_, err = c.Version(context.Background())
	if err == nil || errors.As(err, &f) {
		t.Fatalf("expected a transport error, got %v", err)
	}
	fake.Start()
	if _, err := c.Version(context.Background()); err != nil {
		t.Fatalf("after restart: %v", err)
	}
}

func TestClientTimeout(t *testing.T) {
	fake := rtorrenttest.New(t)
	block := make(chan struct{})
	fake.Handle("system.client_version", func([]any) (any, error) {
		<-block
		return "late", nil
	})
	defer close(block)
	c := rtorrent.New(fake.Socket())
	c.Timeout = 50 * time.Millisecond
	start := time.Now()
	_, err := c.Version(context.Background())
	if err == nil {
		t.Fatal("expected a timeout")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("timeout did not apply")
	}
}
