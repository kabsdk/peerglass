package web

import (
	"bytes"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kabsdk/peerglass/internal/rtorrent"
	"github.com/kabsdk/peerglass/internal/rtorrent/rtorrenttest"
)

const (
	hashA = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA1"
	hashB = "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB2"
	// token is a passkey-like secret in tracker URLs; it must never reach a
	// page or a log line (S5).
	token = "0123456789abcdef0123456789abcdef"
	// hostile is a torrent name chosen to break escaping (docs/testing.md).
	hostile = `<script>alert("x")</script> {{.Evil}}`
)

func torrentA() rtorrenttest.Torrent {
	return rtorrenttest.Torrent{
		"d.hash=": hashA, "d.name=": "alpha.iso", "d.size_bytes=": int64(1000), "d.completed_bytes=": int64(250),
		"d.state=": int64(1), "d.is_active=": int64(1), "d.is_open=": int64(1), "d.down.rate=": int64(100),
		"d.custom1=": "tv", "d.directory=": "/downloads/incomplete", "d.custom=addtime": "1700000000",
		"d.peers_connected=": int64(3), "d.peers_complete=": int64(1), "d.peers_accounted=": int64(2), "d.ratio=": int64(500),
		"trackers": []map[string]any{{"t.url=": "http://tracker-a.example.invalid/" + token + "/announce", "t.is_enabled=": int64(1), "t.scrape_complete=": int64(7)}},
		"files":    []map[string]any{{"f.path=": "alpha.iso", "f.size_bytes=": int64(1000), "f.size_chunks=": int64(4), "f.completed_chunks=": int64(1), "f.priority=": int64(1)}},
	}
}

func torrentB() rtorrenttest.Torrent {
	return rtorrenttest.Torrent{
		"d.hash=": hashB, "d.name=": hostile, "d.size_bytes=": int64(2000), "d.completed_bytes=": int64(2000),
		"d.state=": int64(1), "d.is_active=": int64(1), "d.is_open=": int64(1), "d.complete=": int64(1), "d.up.rate=": int64(50),
		"d.custom1=": "movies", "d.directory=": "/downloads/movies/B", "d.custom=addtime": "1600000000", "d.is_multi_file=": int64(1),
		"d.message=": `Tracker: [Failure reason "<b>nope</b>"]`, "d.peers_connected=": int64(1), "d.ratio=": int64(3000),
		"trackers": []map[string]any{{"t.url=": "https://tracker-b.example.invalid/announce?passkey=" + token, "t.is_enabled=": int64(1)}},
	}
}

type testEnv struct {
	srv  *Server
	fake *rtorrenttest.Server
	logs *bytes.Buffer
}

func newEnv(t *testing.T) *testEnv {
	t.Helper()
	fake := rtorrenttest.New(t)
	fake.SetTorrents(torrentA(), torrentB())
	logs := &bytes.Buffer{}
	srv, err := New(rtorrent.New(fake.Socket()), Config{Labels: []string{"tv", "movies"}, Root: "/downloads/"}, slog.New(slog.NewTextHandler(logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return &testEnv{srv: srv, fake: fake, logs: logs}
}

// get performs a GET and returns the response and body.
func (e *testEnv) get(t *testing.T, target string) (*http.Response, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.Host = "ui.example"
	rec := httptest.NewRecorder()
	e.srv.ServeHTTP(rec, req)
	body, _ := io.ReadAll(rec.Result().Body)
	return rec.Result(), string(body)
}

// getH performs a GET with extra request headers.
func (e *testEnv) getH(t *testing.T, target string, headers map[string]string) (*http.Response, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.Host = "ui.example"
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	e.srv.ServeHTTP(rec, req)
	body, _ := io.ReadAll(rec.Result().Body)
	return rec.Result(), string(body)
}

// post performs a same-origin form POST unless origin is overridden.
func (e *testEnv) post(t *testing.T, target string, form url.Values, headers map[string]string) (*http.Response, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(form.Encode()))
	req.Host = "ui.example"
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "https://ui.example")
	for k, v := range headers {
		if v == "" {
			req.Header.Del(k)
		} else {
			req.Header.Set(k, v)
		}
	}
	rec := httptest.NewRecorder()
	e.srv.ServeHTTP(rec, req)
	body, _ := io.ReadAll(rec.Result().Body)
	return rec.Result(), string(body)
}

func (e *testEnv) checkNoLeak(t *testing.T, body string) {
	t.Helper()
	if strings.Contains(body, token) {
		t.Error("a tracker passkey reached the page")
	}
	if strings.Contains(e.logs.String(), token) {
		t.Error("a tracker passkey reached the log")
	}
}

func TestPageRendersAndEscapes(t *testing.T) {
	e := newEnv(t)
	resp, body := e.get(t, "/")
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Security-Policy"); !strings.Contains(got, "default-src 'self'") || strings.Contains(got, "unsafe") {
		t.Errorf("CSP = %q", got)
	}
	for _, want := range []string{
		`<script src="/static/htmx.min.js" defer></script>`,
		"alpha.iso", "tracker-a.example.invalid", "tracker-b.example.invalid",
		`&lt;script&gt;alert(&#34;x&#34;)&lt;/script&gt; {{.Evil}}`,
		`Failure reason &#34;&lt;b&gt;nope&lt;/b&gt;&#34;`,
		"2 torrents", `<span class="t">Downloading</span><span class="n">1</span>`, `<span class="t">Seeding</span><span class="n">1</span>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	for _, bad := range []string{"<script>alert", "<b>nope</b>", ` onclick=`, `style="`} {
		if strings.Contains(body, bad) {
			t.Errorf("page contains %q", bad)
		}
	}
	e.checkNoLeak(t, body)
	if !strings.Contains(body, "Downloading from 3 peers, 1 seed") {
		t.Error("status text (F2) missing")
	}
}

func TestStaticAssets(t *testing.T) {
	e := newEnv(t)
	for _, p := range []string{"/static/htmx.min.js", "/static/rows.js", "/static/theme.js", "/static/style.css", "/static/favicon.svg"} {
		resp, body := e.get(t, p)
		if resp.StatusCode != 200 || len(body) == 0 {
			t.Errorf("%s: status %d, %d bytes", p, resp.StatusCode, len(body))
		}
		if resp.Header.Get("Content-Security-Policy") == "" {
			t.Errorf("%s: no CSP header", p)
		}
	}
	if resp, _ := e.get(t, "/static/htmx.min.js.sha256"); resp.StatusCode != 200 {
		t.Error("the pinned hash file should be served too")
	}
}

func rowOrder(body string) []string {
	var out []string
	for _, h := range []string{hashA, hashB} {
		if i := strings.Index(body, `value="`+h+`"`); i >= 0 {
			out = append(out, h)
		}
	}
	// Sort by position in the body.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && strings.Index(body, out[j]) < strings.Index(body, out[j-1]); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func TestListFilterSortSearch(t *testing.T) {
	e := newEnv(t)
	cases := []struct {
		query string
		want  []string
	}{
		{"", []string{hashA, hashB}},                                             // default: added, newest first
		{"sort=name&order=asc", []string{hashB, hashA}},                          // "<script…" sorts before "alpha"
		{"sort=name&order=desc", []string{hashA, hashB}},                         // flipped
		{"sortby=size&orderby=desc&sort=name&order=asc", []string{hashB, hashA}}, // a header click wins over the form
		{"state=seeding", []string{hashB}},
		{"state=downloading", []string{hashA}},
		{"state=error", []string{hashB}},
		{"state=stopped", nil},
		{"state=completed", []string{hashB}},
		{"fstate=seeding&state=downloading", []string{hashB}}, // a sidebar click wins over the form
		{"fstate=&state=downloading", []string{hashA, hashB}}, // "All" clears the filter even though empty
		{"flabel=" + url.QueryEscape(noneOption) + "&label=tv", nil},
		{"ftracker=tracker-a.example.invalid", []string{hashA}},
		{"label=tv", []string{hashA}},
		{"label=" + url.QueryEscape(noneOption), nil},
		{"tracker=tracker-b.example.invalid", []string{hashB}},
		{"q=ALPHA", []string{hashA}},
		{"q=" + hashB[:6], []string{hashB}},
		{"q=nothing-matches", nil},
	}
	for _, tc := range cases {
		resp, body := e.get(t, "/torrents?"+tc.query)
		if resp.StatusCode != 200 {
			t.Errorf("%q: status %d", tc.query, resp.StatusCode)
		}
		if got := rowOrder(body); strings.Join(got, ",") != strings.Join(tc.want, ",") {
			t.Errorf("%q: rows %v, want %v", tc.query, got, tc.want)
		}
		if tc.want == nil && !strings.Contains(body, "No torrents match.") {
			t.Errorf("%q: empty table should say so", tc.query)
		}
		e.checkNoLeak(t, body)
	}
}

func TestListStateAndOOB(t *testing.T) {
	e := newEnv(t)
	// A selection sent along is ignored: the browser keeps it (rows.js).
	_, body := e.get(t, "/torrents?sort=size&order=desc&h="+hashA+"&open="+hashB)
	if strings.Contains(body, " checked") || strings.Contains(body, " selected") {
		t.Error("the server never renders the selection")
	}
	if !strings.Contains(body, `<tbody hx-target="#detail" hx-swap="outerHTML">`) || !strings.Contains(body, `<tr id="t-`+hashB+`" class="st-seeding open haserror" tabindex="0" hx-get="/torrents/`+hashB+`" hx-trigger="click target:{td, td *}">`) {
		t.Error("the whole row should open the detail panel, and the open one be marked")
	}
	if !strings.Contains(body, `<th class="sel"><input type="checkbox" name="h" value="`+hashB+`" form="selection"></th>`) {
		t.Error("the checkbox cell is a th, so the row trigger's td filter skips it, and belongs to #selection")
	}
	if strings.Contains(body, `<a href="/?open=`) {
		t.Error("the name must not be a link of its own any more; two triggers would fire")
	}
	if !strings.Contains(body, `class="col-size sorted desc"`) {
		t.Error("sorted column should be marked")
	}
	if !strings.Contains(body, `<span id="liststate" hx-swap-oob="true"><input type="hidden" name="sort" value="size"><input type="hidden" name="order" value="desc"><input type="hidden" name="state" value=""><input type="hidden" name="label" value=""><input type="hidden" name="tracker" value=""><input type="hidden" name="v" value="`) {
		t.Error("sort and filter state must be sent back for the form")
	}
	if !strings.Contains(body, `hx-vals="{&#34;orderby&#34;:&#34;asc&#34;,&#34;sortby&#34;:&#34;size&#34;}"`) {
		t.Error("clicking the sorted column again should flip the order")
	}
	if !strings.Contains(body, `<nav id="sidebar" class="sidebar" aria-label="Filters" hx-sync="#list:replace" hx-swap-oob="true">`) {
		t.Error("the sidebar should be re-sent with the list")
	}
	for _, want := range []string{
		`class="active" hx-get="/torrents" hx-vals="{&#34;fstate&#34;:&#34;&#34;}"`, // All states active
		`class="" hx-get="/torrents" hx-vals="{&#34;fstate&#34;:&#34;seeding&#34;}" hx-include="#filters" hx-target="#torrents" hx-swap="outerHTML" title="Seeding"><span class="t">Seeding</span><span class="n">1</span>`,
		`<span class="t">Completed</span><span class="n">1</span>`,
		`<span class="t">With errors</span><span class="n">1</span>`,
		`class="" hx-get="/torrents" hx-vals="{&#34;flabel&#34;:&#34;tv&#34;}" hx-include="#filters" hx-target="#torrents" hx-swap="outerHTML" title="tv"><span class="t">tv</span><span class="n">1</span>`,
		`<span class="t">No label</span><span class="n">0</span>`,
		`<span class="t">movies</span><span class="n">1</span>`,
		`hx-vals="{&#34;ftracker&#34;:&#34;tracker-b.example.invalid&#34;}" hx-include="#filters" hx-target="#torrents" hx-swap="outerHTML" title="tracker-b.example.invalid"><span class="t">tracker-b.example.invalid</span><span class="n">1</span>`,
		`<span class="t">No tracker</span><span class="n">0</span>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("sidebar lacks %s", want)
		}
	}
	if strings.Contains(body, "<select") {
		t.Error("no filter dropdowns any more")
	}
	e.checkNoLeak(t, body)

	// A filter in force is sent back as hidden state and marked in the sidebar.
	_, body = e.get(t, "/torrents?label=tv")
	if !strings.Contains(body, `<input type="hidden" name="label" value="tv">`) {
		t.Error("label filter must be sent back for the form")
	}
	if !strings.Contains(body, `class="active" hx-get="/torrents" hx-vals="{&#34;flabel&#34;:&#34;tv&#34;}"`) {
		t.Error("active label should be marked in the sidebar")
	}
}

func versionOf(t *testing.T, body string) string {
	t.Helper()
	i := strings.Index(body, `name="v" value="`)
	if i < 0 {
		t.Fatal("no version in the hidden state")
	}
	v := body[i+len(`name="v" value="`):]
	return v[:strings.Index(v, `"`)]
}

// Polls are answered against the version the browser shows: nothing when
// unchanged, only the changed rows when the rows are the same in the same
// order, the whole table otherwise.
func TestListIncrementalUpdates(t *testing.T) {
	e := newEnv(t)
	resp, body := e.get(t, "/torrents")
	if resp.Header.Get("HX-Reswap") != "" || !strings.Contains(body, "<table") {
		t.Fatal("first request should carry the table")
	}
	v1 := versionOf(t, body)

	resp, body = e.get(t, "/torrents?v="+v1)
	if resp.StatusCode != 200 || resp.Header.Get("HX-Reswap") != "none" || strings.Contains(body, "<tr") || !strings.Contains(body, `id="sidebar"`) {
		t.Errorf("unchanged: status %d, reswap %q, body %q", resp.StatusCode, resp.Header.Get("HX-Reswap"), body)
	}
	if versionOf(t, body) != v1 {
		t.Error("unchanged list keeps its version")
	}

	// A rate change in one torrent: only that row is sent.
	ts := e.fake.Torrents()
	ts[1]["d.up.rate="] = int64(99999)
	e.fake.SetTorrents(ts...)
	e.srv.mu.Lock()
	e.srv.cachedAt = time.Time{} // expire the one-second list cache
	e.srv.mu.Unlock()
	resp, body = e.get(t, "/torrents?v="+v1)
	if resp.Header.Get("HX-Reswap") != "none" || strings.Count(body, `<tr hx-swap-oob="true" id="t-`) != 1 || !strings.Contains(body, `id="t-`+hashB+`"`) || !strings.Contains(body, "97.7 KiB/s") {
		t.Errorf("rate change: want only B patched, got reswap %q body %q", resp.Header.Get("HX-Reswap"), body)
	}
	if versionOf(t, body) == v1 {
		t.Error("a patch carries a new version")
	}

	// Sorting differently reorders: the whole table again.
	resp, body = e.get(t, "/torrents?v="+versionOf(t, body)+"&sort=name&order=asc")
	if resp.Header.Get("HX-Reswap") != "" || !strings.Contains(body, "<table") {
		t.Error("a new order must swap the table")
	}
	// A torrent disappearing: the whole table.
	e.fake.SetTorrents(torrentA())
	e.srv.mu.Lock()
	e.srv.cachedAt = time.Time{}
	e.srv.mu.Unlock()
	resp, body = e.get(t, "/torrents?v="+versionOf(t, body)+"&sort=name&order=asc")
	if resp.Header.Get("HX-Reswap") != "" || !strings.Contains(body, "<table") || strings.Contains(body, hashB) {
		t.Error("a removed torrent must swap the table")
	}
	// An unknown version (another tab, or expired): the whole table.
	if resp, body := e.get(t, "/torrents?v=nonsense"); resp.Header.Get("HX-Reswap") != "" || !strings.Contains(body, "<table") {
		t.Error("an unknown version must swap the table")
	}
}

func TestUnavailableAndRecovery(t *testing.T) {
	e := newEnv(t)
	e.fake.Stop()
	resp, body := e.get(t, "/torrents")
	if resp.StatusCode != http.StatusBadGateway || resp.Header.Get("HX-Reswap") != "none" {
		t.Errorf("fragment: status %d, HX-Reswap %q", resp.StatusCode, resp.Header.Get("HX-Reswap"))
	}
	if !strings.Contains(body, "rTorrent unavailable") {
		t.Error("fragment should carry the banner (F11)")
	}
	resp, body = e.get(t, "/")
	if resp.StatusCode != 200 || !strings.Contains(body, "rTorrent unavailable") {
		t.Errorf("page: status %d, banner missing", resp.StatusCode)
	}
	resp, body = e.get(t, "/torrents/"+hashA)
	if resp.StatusCode != http.StatusBadGateway || resp.Header.Get("HX-Reswap") != "none" || !strings.Contains(body, "rTorrent unavailable") {
		t.Errorf("detail: status %d", resp.StatusCode)
	}
	if !strings.Contains(e.logs.String(), "rtorrent unavailable") {
		t.Error("the rTorrent error should be logged (N3)")
	}
	if !strings.Contains(body, "rTorrent unavailable") {
		t.Error("the page should keep its banner")
	}
	e.fake.Start()
	resp, body = e.get(t, "/torrents")
	if resp.StatusCode != 200 || resp.Header.Get("HX-Reswap") != "" || !strings.Contains(body, "alpha.iso") {
		t.Errorf("after recovery: status %d", resp.StatusCode)
	}
	if !strings.Contains(body, `<div id="banner" hx-swap-oob="true"></div>`) {
		t.Error("recovery should clear the banner")
	}
}

func TestDetail(t *testing.T) {
	e := newEnv(t)
	resp, body := e.get(t, "/torrents/"+strings.ToLower(hashB))
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	for _, want := range []string{
		`id="detail"`, hashB, "https://tracker-b.example.invalid", "/downloads/movies/B",
		`&lt;script&gt;alert(&#34;x&#34;)&lt;/script&gt;`, `Failure reason &#34;&lt;b&gt;nope&lt;/b&gt;&#34;`,
		`<input type="hidden" id="openstate" name="open" value="` + hashB + `" hx-swap-oob="true">`,
		`class="tab active"`, "3.00",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("general tab lacks %q", want)
		}
	}
	e.checkNoLeak(t, body)
	if calls := e.fake.CallsTo("system.multicall"); len(calls) != 1 {
		t.Errorf("detail should fetch the torrent with one system.multicall, got %d", len(calls))
	}
	if resp.Header.Get("HX-Trigger-After-Settle") != "refresh" {
		t.Error("a click should refresh the list so the row is highlighted at once")
	}
	if strings.Contains(body, "detail-actions") || strings.Contains(body, `hx-post="/actions/`) {
		t.Error("the detail panel has no actions of its own; the selection has them")
	}
	if resp, _ := e.getH(t, "/torrents/"+hashB, map[string]string{"HX-Trigger": "detail"}); resp.Header.Get("HX-Trigger-After-Settle") != "" {
		t.Error("the panel's own poll must not trigger list refreshes")
	}

	_, body = e.get(t, "/torrents/"+hashA+"?tab=trackers")
	if !strings.Contains(body, "http://tracker-a.example.invalid") || !strings.Contains(body, "<td class=\"num\">7</td>") {
		t.Error("trackers tab lacks the tracker row")
	}
	if calls := e.fake.CallsTo("t.multicall"); len(calls) == 0 || calls[len(calls)-1].Params[0] != hashA {
		t.Error("trackers tab should call t.multicall for the hash")
	}
	e.checkNoLeak(t, body)

	_, body = e.get(t, "/torrents/"+hashA+"?tab=files")
	if !strings.Contains(body, "alpha.iso") || !strings.Contains(body, "25.0%") || !strings.Contains(body, "Normal") {
		t.Error("files tab lacks the file row")
	}

	resp, body = e.get(t, "/torrents/"+strings.Repeat("C", 39)+"3")
	if resp.StatusCode != http.StatusNotFound || !strings.Contains(body, "no longer in rTorrent") {
		t.Errorf("unknown hash: status %d", resp.StatusCode)
	}
	if !strings.Contains(body, `name="open" value="" hx-swap-oob="true"`) {
		t.Error("a vanished torrent should clear the open state")
	}
	if resp, _ := e.get(t, "/torrents/not-a-hash"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("invalid hash: status %d", resp.StatusCode)
	}
	if resp, body := e.get(t, "/detail"); resp.StatusCode != 200 || !strings.Contains(body, "Select a torrent") {
		t.Error("empty detail")
	}
}

func TestContextMenu(t *testing.T) {
	e := newEnv(t)
	resp, body := e.get(t, "/menu/"+strings.ToLower(hashB))
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	for _, want := range []string{
		`<div class="menu-title">&lt;script&gt;alert(&#34;x&#34;)&lt;/script&gt; {{.Evil}}</div>`,
		`<div class="menu-items" hx-target="#flash" hx-include="#selection">`,
		`hx-post="/actions/stop" popovertarget="menu" popovertargetaction="hide"`,
		`hx-post="/actions/reannounce" popovertarget="menu" popovertargetaction="hide">Reannounce</button>`,
		`<option value="movies" selected>movies</option>`,
		`hx-confirm="Remove this torrent AND DELETE its data from disk? This cannot be undone."`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("menu lacks %s", want)
		}
	}
	if strings.Contains(body, "<script>alert") {
		t.Error("menu title is not escaped")
	}
	if strings.Contains(body, "hx-vals") {
		t.Error("the menu acts on the posted selection, never on a hash of its own")
	}
	_, body = e.get(t, "/menu/"+hashA+"?n=3")
	if !strings.Contains(body, `<div class="menu-title">3 selected torrents</div>`) || !strings.Contains(body, "Remove the 3 selected torrents") {
		t.Errorf("selection menu: %q", body)
	}
	if _, body := e.get(t, "/menu/"+hashA+"?n=1"); !strings.Contains(body, `<div class="menu-title">alpha.iso</div>`) {
		t.Error("a selection of one is titled with the name")
	}
	if resp, body := e.get(t, "/menu/"+strings.Repeat("C", 39)+"3"); resp.StatusCode != http.StatusNotFound || !strings.Contains(body, "no longer in rTorrent") {
		t.Errorf("unknown hash: %d %q", resp.StatusCode, body)
	}
	if resp, _ := e.get(t, "/menu/nothex"); resp.StatusCode != http.StatusNotFound {
		t.Error("invalid hash should be a 404")
	}
	_, page := e.get(t, "/")
	if !strings.Contains(page, `<script src="/static/rows.js" defer></script>`) || !strings.Contains(page, `<div id="menu" class="menu" popover></div>`) || !strings.Contains(page, `<button type="button" data-select="all" title="Ctrl+A">Select all</button>`) {
		t.Error("page should load the menu script and carry the popover")
	}
}

// The selection travels only in POST bodies. A GET (every poll, sort, filter
// and search) must never include it: through the auth proxy a request line
// is limited, and 825 hashes in a query string were refused with 431.
func TestSelectionNeverInGetRequests(t *testing.T) {
	e := newEnv(t)
	_, page := e.get(t, "/")
	_, list := e.get(t, "/torrents")
	_, menu := e.get(t, "/menu/"+hashA)
	tag := regexp.MustCompile(`<[a-z]+ [^>]*>`)
	for _, doc := range []string{page, list, menu} {
		for _, el := range tag.FindAllString(doc, -1) {
			if strings.Contains(el, "hx-get=") && strings.Contains(el, `hx-include="#selection"`) {
				t.Errorf("a GET includes the selection: %s", el)
			}
		}
	}
	// Toolbar actions say so themselves; the menu's buttons inherit it from
	// their container. Add and limits post forms of their own.
	for _, el := range tag.FindAllString(page, -1) {
		if strings.Contains(el, `hx-post="/actions/`) && !strings.Contains(el, "/actions/add") && !strings.Contains(el, "/actions/limits") &&
			!strings.Contains(el, `hx-include="#selection"`) {
			t.Errorf("a toolbar action does not post the selection: %s", el)
		}
	}
	if !strings.Contains(menu, `<div class="menu-items" hx-target="#flash" hx-include="#selection">`) {
		t.Error("the menu's actions must post the selection")
	}
	if !strings.Contains(page, `<form id="selection" hidden></form>`) || !strings.Contains(list, `form="selection"`) || strings.Contains(list, `form="filters"`) {
		t.Error("the checkboxes belong to the #selection form, not to #filters")
	}
	if !strings.Contains(page, `<span id="selcount" class="selcount" aria-live="polite"></span>`) {
		t.Error("the page needs the selection counter that rows.js fills")
	}
}

func TestSpeedLimits(t *testing.T) {
	e := newEnv(t)
	_, page := e.get(t, "/")
	if !strings.Contains(page, `<details id="limits" class="pop"><summary>Speed limits</summary>`) || strings.Contains(page, `class="limit"`) {
		t.Fatal("page should offer the limits and show none in force")
	}
	if !strings.Contains(page, `name="down" min="0" max="4194303" step="1" value="" placeholder="unlimited"`) {
		t.Error("an unlimited rate is an empty field")
	}
	e.get(t, "/torrents") // fill the one-second cache

	resp, body := e.post(t, "/actions/limits", url.Values{"down": {"512"}, "up": {""}}, nil)
	if resp.StatusCode != 200 || !strings.Contains(body, "Limits: download 512 KiB/s, upload unlimited, until rTorrent restarts.") {
		t.Fatalf("set: %d %q", resp.StatusCode, body)
	}
	if e.fake.Global("throttle.global_down.max_rate") != 512*1024 || e.fake.Global("throttle.global_up.max_rate") != 0 {
		t.Error("rTorrent did not get the limits in KiB/s")
	}
	if resp.Header.Get("HX-Trigger-After-Settle") != "refresh" {
		t.Error("setting limits should refresh the list")
	}
	if !strings.Contains(body, `<details id="limits" class="pop" hx-swap-oob="true"><summary>`) || !strings.Contains(body, `name="down" min="0" max="4194303" step="1" value="512"`) {
		t.Error("the popover should come back closed, showing the new limits")
	}
	// The action dropped the cache: the header shows the limit at once.
	_, body = e.get(t, "/torrents")
	if !strings.Contains(body, `↓ 100 B/s <span class="limit">(max 512 KiB/s)</span> ↑`) {
		t.Errorf("header should show the limit, got %q", body[strings.Index(body, `id="totals"`):][:200])
	}
	if _, form := e.get(t, "/limits"); !strings.Contains(form, `<form id="limitsform"`) || !strings.Contains(form, `value="512"`) {
		t.Error("the form fetched on open shows the limits in force")
	}

	resp, body = e.post(t, "/actions/limits", url.Values{"down": {"512"}, "up": {"64"}, "clear": {"1"}}, nil)
	if resp.StatusCode != 200 || !strings.Contains(body, "download unlimited, upload unlimited") || e.fake.Global("throttle.global_down.max_rate") != 0 {
		t.Errorf("clear: %d %q", resp.StatusCode, body)
	}

	for _, bad := range []url.Values{
		{"down": {"-1"}}, {"down": {"abc"}}, {"up": {"1.5"}}, {"down": {"4194304"}}, {"up": {"1e3"}},
	} {
		e.fake.Reset()
		resp, body := e.post(t, "/actions/limits", bad, nil)
		if resp.StatusCode != 400 || !strings.Contains(body, "whole number of KiB/s from 0 to 4194303") {
			t.Errorf("%v: %d %q", bad, resp.StatusCode, body)
		}
		if len(e.fake.Calls()) != 0 {
			t.Errorf("%v reached rTorrent", bad)
		}
	}
	e.fake.Reset()
	if resp, _ := e.post(t, "/actions/limits", url.Values{"down": {"1"}}, map[string]string{"Origin": "https://evil.example"}); resp.StatusCode != 403 || len(e.fake.Calls()) != 0 {
		t.Error("limits from a foreign origin must be refused (S4)")
	}
	if resp, _ := e.get(t, "/actions/limits"); resp.StatusCode != http.StatusMethodNotAllowed {
		t.Error("GET must not change limits")
	}
}

func TestTheme(t *testing.T) {
	e := newEnv(t)
	_, body := e.get(t, "/")
	if !strings.Contains(body, "<html lang=\"en\">") || !strings.Contains(body, ">Theme: System</button>") {
		t.Error("no cookie: follow the system")
	}
	for value, want := range map[string]string{
		"dark":                 `<html lang="en" data-theme="dark">`,
		"light":                `<html lang="en" data-theme="light">`,
		`"><script>x</script>`: "<html lang=\"en\">",
		"blue":                 "<html lang=\"en\">",
	} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Host = "ui.example"
		req.AddCookie(&http.Cookie{Name: "theme", Value: value})
		rec := httptest.NewRecorder()
		e.srv.ServeHTTP(rec, req)
		if got := rec.Body.String(); !strings.Contains(got, want) {
			t.Errorf("cookie %q: page lacks %s", value, want)
		}
	}
	if !strings.Contains(body, `<script src="/static/theme.js" defer></script>`) {
		t.Error("the page should load theme.js")
	}
}

// Actions drop the one-second list cache, so the refresh they trigger shows
// their effect instead of the state from before.
func TestActionsInvalidateCache(t *testing.T) {
	e := newEnv(t)
	e.get(t, "/torrents")
	if resp, _ := e.post(t, "/actions/label", url.Values{"h": {hashA}, "setlabel": {"movies"}}, nil); resp.StatusCode != 200 {
		t.Fatal("label failed")
	}
	_, body := e.get(t, "/torrents?label=movies")
	if !strings.Contains(body, `id="t-`+hashA+`"`) {
		t.Error("the list right after an action must show its effect")
	}
}

func TestPageWithOpenTorrent(t *testing.T) {
	e := newEnv(t)
	_, body := e.get(t, "/?open="+hashA+"&state=downloading")
	if !strings.Contains(body, `<div id="detail" class="detail" hx-get="/torrents/`+hashA+`?tab=general"`) {
		t.Error("page should render the open torrent's panel")
	}
	if strings.Count(body, `id="openstate"`) != 1 {
		t.Error("exactly one openstate input on the page")
	}
	if !strings.Contains(body, `class="active" hx-get="/torrents" hx-vals="{&#34;fstate&#34;:&#34;downloading&#34;}"`) {
		t.Error("filter from the URL should be marked in the sidebar")
	}
	if !strings.Contains(body, `<nav id="sidebar" class="sidebar" aria-label="Filters" hx-sync="#list:replace">`) || strings.Count(body, `id="sidebar"`) != 1 {
		t.Error("the page should render the sidebar once, inline")
	}
}

func TestActionsRequireSameOrigin(t *testing.T) {
	e := newEnv(t)
	form := url.Values{"h": {hashA}}
	cases := []struct {
		name    string
		headers map[string]string
		status  int
	}{
		{"no origin", map[string]string{"Origin": ""}, http.StatusForbidden},
		{"null origin", map[string]string{"Origin": "null"}, http.StatusForbidden},
		{"foreign origin", map[string]string{"Origin": "https://evil.example"}, http.StatusForbidden},
		{"foreign referer", map[string]string{"Origin": "", "Referer": "https://evil.example/x"}, http.StatusForbidden},
		{"same referer", map[string]string{"Origin": "", "Referer": "https://ui.example/"}, http.StatusOK},
		{"same origin", nil, http.StatusOK},
	}
	for _, tc := range cases {
		e.fake.Reset()
		resp, _ := e.post(t, "/actions/start", form, tc.headers)
		if resp.StatusCode != tc.status {
			t.Errorf("%s: status %d, want %d", tc.name, resp.StatusCode, tc.status)
		}
		if calls := e.fake.CallsTo("d.start"); (len(calls) > 0) != (tc.status == 200) {
			t.Errorf("%s: %d d.start calls", tc.name, len(calls))
		}
	}
	for _, p := range []string{"/actions/start", "/actions/add", "/actions/remove-data"} {
		if resp, _ := e.get(t, p); resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("GET %s: status %d, want 405 (S4)", p, resp.StatusCode)
		}
	}
}

func TestBulkActions(t *testing.T) {
	e := newEnv(t)
	for _, tc := range []struct{ path, method, verb string }{
		{"/actions/start", "d.start", "Started 2 torrents"},
		{"/actions/stop", "d.stop", "Stopped 2 torrents"},
		{"/actions/recheck", "d.check_hash", "Rechecking 2 torrents"},
		{"/actions/reannounce", "d.tracker_announce", "Reannounced 2 torrents"},
	} {
		e.fake.Reset()
		resp, body := e.post(t, tc.path, url.Values{"h": {strings.ToLower(hashA), hashB}}, nil)
		if resp.StatusCode != 200 || resp.Header.Get("HX-Trigger-After-Settle") != "refresh" || !strings.Contains(body, tc.verb) {
			t.Errorf("%s: status %d, trigger %q, body %q", tc.path, resp.StatusCode, resp.Header.Get("HX-Trigger"), body)
		}
		calls := e.fake.CallsTo(tc.method)
		if len(calls) != 2 || calls[0].Params[0] != hashA || calls[1].Params[0] != hashB || len(calls[0].Params) != 1 {
			t.Errorf("%s: calls %v", tc.path, calls)
		}
	}

	e.fake.Reset()
	if resp, body := e.post(t, "/actions/start", url.Values{"h": {"zzz"}}, nil); resp.StatusCode != 400 || len(e.fake.Calls()) != 0 || !strings.Contains(body, "not a torrent hash") {
		t.Error("an invalid hash must be rejected before any call")
	}
	if resp, body := e.post(t, "/actions/start", url.Values{}, nil); resp.StatusCode != 400 || !strings.Contains(body, "Select at least one") {
		t.Error("an empty selection must be rejected")
	}

	// A fault for one torrent is reported and does not stop the others.
	unknown := strings.Repeat("D", 39) + "4"
	resp, body := e.post(t, "/actions/stop", url.Values{"h": {hashA, unknown}}, nil)
	if resp.StatusCode != 200 || !strings.Contains(body, "Stopped 1 torrent.") || !strings.Contains(body, "rTorrent refused: invalid parameters") || !strings.Contains(body, `class="msg warn"`) {
		t.Errorf("partial failure: status %d body %q", resp.StatusCode, body)
	}
	resp, body = e.post(t, "/actions/stop", url.Values{"h": {unknown}}, nil)
	if resp.StatusCode != http.StatusBadGateway || !strings.Contains(body, `class="msg error"`) {
		t.Errorf("total failure: status %d body %q", resp.StatusCode, body)
	}
}

func TestRemoveActions(t *testing.T) {
	e := newEnv(t)
	resp, body := e.post(t, "/actions/remove", url.Values{"h": {hashA}}, nil)
	if resp.StatusCode != 200 || !strings.Contains(body, "Removed 1 torrent") {
		t.Fatalf("remove: %d %q", resp.StatusCode, body)
	}
	if calls := e.fake.CallsTo("d.erase"); len(calls) != 1 || calls[0].Params[0] != hashA {
		t.Errorf("d.erase calls %v", calls)
	}
	if len(e.fake.CallsTo("d.erase_with_data")) != 0 {
		t.Error("plain remove must not touch data")
	}
	resp, body = e.post(t, "/actions/remove-data", url.Values{"h": {hashB}}, nil)
	if resp.StatusCode != 200 || !strings.Contains(body, "Removed, with data 1 torrent") {
		t.Fatalf("remove-data: %d %q", resp.StatusCode, body)
	}
	if calls := e.fake.CallsTo("d.erase_with_data"); len(calls) != 1 || calls[0].Params[0] != hashB {
		t.Errorf("d.erase_with_data calls %v", calls)
	}
	if len(e.fake.Torrents()) != 0 {
		t.Error("both torrents should be gone")
	}
}

func TestLabelAction(t *testing.T) {
	e := newEnv(t)
	resp, body := e.post(t, "/actions/label", url.Values{"h": {hashA}, "setlabel": {"movies"}}, nil)
	if resp.StatusCode != 200 || !strings.Contains(body, `Set label &#34;movies&#34; on 1 torrent`) {
		t.Fatalf("status %d body %q", resp.StatusCode, body)
	}
	calls := e.fake.CallsTo("d.custom1.set")
	if len(calls) != 1 || calls[0].Params[0] != hashA || calls[0].Params[1] != "movies" {
		t.Errorf("calls %v", calls)
	}
	e.fake.Reset()
	if resp, body := e.post(t, "/actions/label", url.Values{"h": {hashA}, "setlabel": {"music"}}, nil); resp.StatusCode != 400 || len(e.fake.Calls()) != 0 || !strings.Contains(body, "unknown label") {
		t.Error("an unknown label must be rejected before any call (F8)")
	}
	resp, body = e.post(t, "/actions/label", url.Values{"h": {hashA, hashB}, "setlabel": {""}}, nil)
	if resp.StatusCode != 200 || !strings.Contains(body, "Cleared the label of 2 torrents") {
		t.Errorf("clear: %d %q", resp.StatusCode, body)
	}
	if calls := e.fake.CallsTo("d.custom1.set"); len(calls) != 2 || calls[1].Params[1] != "" {
		t.Errorf("clear calls %v", calls)
	}
}

// torrentFile is a minimal valid .torrent for uploads.
func torrentFile(name string) ([]byte, string) {
	data := []byte("d4:infod6:lengthi3e4:name" + strconv.Itoa(len(name)) + ":" + name + "12:piece lengthi16384e6:pieces20:aaaaaaaaaaaaaaaaaaaaee")
	tf, err := rtorrent.ParseTorrentFile(data)
	if err != nil {
		panic(err)
	}
	return data, tf.Hash
}

type part struct {
	field, filename string
	data            []byte
}

func (e *testEnv) postMultipart(t *testing.T, fields url.Values, files []part) (*http.Response, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, vs := range fields {
		for _, v := range vs {
			mw.WriteField(k, v)
		}
	}
	for _, p := range files {
		fw, _ := mw.CreateFormFile(p.field, p.filename)
		fw.Write(p.data)
	}
	mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/actions/add", &buf)
	req.Host = "ui.example"
	req.Header.Set("Origin", "http://ui.example")
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	e.srv.ServeHTTP(rec, req)
	body, _ := io.ReadAll(rec.Result().Body)
	return rec.Result(), string(body)
}

// appearOnLoad makes the fake behave like rTorrent: a loaded torrent shows
// up in the list.
func (e *testEnv) appearOnLoad(method string, hash, name string) {
	e.fake.Handle(method, func(params []any) (any, error) {
		e.fake.SetTorrents(append(e.fake.Torrents(), rtorrenttest.Torrent{"d.hash=": hash, "d.name=": name})...)
		return int64(0), nil
	})
}

func TestAddTorrentFile(t *testing.T) {
	e := newEnv(t)
	data, hash := torrentFile("test.iso")
	e.appearOnLoad("load.raw_start_verbose", hash, "test.iso")
	resp, body := e.postMultipart(t, url.Values{"label": {"tv"}, "path": {`/downloads/tv/It's "odd", yes`}, "start": {"1"}}, []part{{"torrent", "test.torrent", data}})
	if resp.StatusCode != 200 || !strings.Contains(body, "Added: test.iso") || resp.Header.Get("HX-Trigger-After-Settle") != "refresh" {
		t.Fatalf("status %d body %q", resp.StatusCode, body)
	}
	if !strings.Contains(body, `id="addform"`) || !strings.Contains(body, `hx-swap-oob="true"`) {
		t.Error("a successful add should reset the form")
	}
	calls := e.fake.CallsTo("load.raw_start_verbose")
	if len(calls) != 1 {
		t.Fatalf("load calls %v", calls)
	}
	want := []any{"", data, `d.directory.set="/downloads/tv/It's \"odd\", yes"`, `d.custom1.set="tv"`}
	if len(calls[0].Params) != 4 || calls[0].Params[0] != want[0] || !bytes.Equal(calls[0].Params[1].([]byte), data) || calls[0].Params[2] != want[2] || calls[0].Params[3] != want[3] {
		t.Errorf("load params %#v, want %#v", calls[0].Params, want)
	}
	if !strings.Contains(e.logs.String(), hash) {
		t.Error("the add should be logged with its hash")
	}

	// Stopped, default directory, no label: the raw_verbose form with no
	// extra commands. The torrent is already present, so nothing is sent.
	e.fake.Reset()
	resp, body = e.postMultipart(t, url.Values{}, []part{{"torrent", "test.torrent", data}})
	if resp.StatusCode != 200 || !strings.Contains(body, "Already in rTorrent: test.iso") || len(e.fake.CallsTo("load.raw_verbose")) != 0 {
		t.Errorf("duplicate: status %d body %q", resp.StatusCode, body)
	}
	data2, hash2 := torrentFile("other.iso")
	e.appearOnLoad("load.raw_verbose", hash2, "other.iso")
	resp, body = e.postMultipart(t, url.Values{}, []part{{"torrent", "other.torrent", data2}})
	if resp.StatusCode != 200 || !strings.Contains(body, "Added: other.iso") {
		t.Errorf("stopped add: status %d body %q", resp.StatusCode, body)
	}
	if calls := e.fake.CallsTo("load.raw_verbose"); len(calls) != 1 || len(calls[0].Params) != 2 {
		t.Errorf("stopped add params %v", calls)
	}
}

func TestAddDidNotAppear(t *testing.T) {
	e := newEnv(t)
	data, _ := torrentFile("ghost.iso")
	resp, body := e.postMultipart(t, url.Values{}, []part{{"torrent", "g.torrent", data}})
	if resp.StatusCode != http.StatusBadGateway || !strings.Contains(body, "did not appear") {
		t.Errorf("status %d body %q", resp.StatusCode, body)
	}
}

func TestAddMagnet(t *testing.T) {
	e := newEnv(t)
	hash := strings.Repeat("E", 39) + "5"
	magnet := "magnet:?xt=urn:btih:" + strings.ToLower(hash) + "&tr=" + url.QueryEscape("http://tracker-c.example.invalid/"+token+"/announce")
	e.appearOnLoad("load.start_verbose", hash, "meta")
	resp, body := e.postMultipart(t, url.Values{"magnet": {magnet}, "start": {"1"}, "label": {"movies"}}, nil)
	if resp.StatusCode != 200 || !strings.Contains(body, "Added: magnet "+hash[:8]) {
		t.Fatalf("status %d body %q", resp.StatusCode, body)
	}
	calls := e.fake.CallsTo("load.start_verbose")
	if len(calls) != 1 || calls[0].Params[1] != magnet || calls[0].Params[2] != `d.custom1.set="movies"` {
		t.Errorf("params %v", calls)
	}
	e.checkNoLeak(t, body)
	if strings.Contains(e.logs.String(), "magnet:?") {
		t.Error("magnet links must not be logged (N3, S5)")
	}
}

func TestAddRejectsBadInput(t *testing.T) {
	e := newEnv(t)
	data, _ := torrentFile("ok.iso")
	cases := []struct {
		name   string
		fields url.Values
		files  []part
		want   string
	}{
		{"nothing", url.Values{}, nil, "Choose at least one"},
		{"relative path", url.Values{"path": {"tv/x"}}, []part{{"torrent", "a", data}}, "must be absolute"},
		{"outside root", url.Values{"path": {"/etc/x"}}, []part{{"torrent", "a", data}}, "below /downloads/"},
		{"root itself", url.Values{"path": {"/downloads/"}}, []part{{"torrent", "a", data}}, "below /downloads/"},
		{"dotdot", url.Values{"path": {"/downloads/tv/../../etc"}}, []part{{"torrent", "a", data}}, "must not contain &#34;..&#34;"},
		{"dotdot hidden", url.Values{"path": {"/downloads/../downloads/tv"}}, []part{{"torrent", "a", data}}, "must not contain &#34;..&#34;"},
		{"control char", url.Values{"path": {"/downloads/tv/x\ny"}}, []part{{"torrent", "a", data}}, "control characters"},
		{"unknown label", url.Values{"label": {"music"}}, []part{{"torrent", "a", data}}, "unknown label"},
		{"not a torrent", url.Values{}, []part{{"torrent", "junk.torrent", []byte("hello")}}, "junk.torrent: not a torrent file"},
		{"bad magnet", url.Values{"magnet": {"http://example.invalid/?xt=urn:btih:" + hashA}}, nil, "not a magnet link"},
		{"too many files", url.Values{}, manyParts(21, data), "At most 20 files"},
	}
	for _, tc := range cases {
		e.fake.Reset()
		resp, body := e.postMultipart(t, tc.fields, tc.files)
		if resp.StatusCode != 400 {
			t.Errorf("%s: status %d body %q", tc.name, resp.StatusCode, body)
		}
		if !strings.Contains(body, tc.want) {
			t.Errorf("%s: body %q lacks %q", tc.name, body, tc.want)
		}
		for _, c := range e.fake.Calls() {
			if strings.HasPrefix(c.Method, "load.") {
				t.Errorf("%s: reached rTorrent with %s", tc.name, c.Method)
			}
		}
	}
}

func manyParts(n int, data []byte) []part {
	var out []part
	for i := 0; i < n; i++ {
		out = append(out, part{"torrent", "f.torrent", data})
	}
	return out
}

func TestValidateDirectory(t *testing.T) {
	s := &Server{root: "/downloads"}
	good := map[string]string{
		"":                         "",
		"  /downloads/tv  ":        "/downloads/tv",
		"/downloads/tv/":           "/downloads/tv",
		"/downloads//movies/./x":   "/downloads/movies/x",
		"/downloads/tv/a, b \"c\"": "/downloads/tv/a, b \"c\"",
	}
	for in, want := range good {
		if got, err := s.validateDirectory(in); err != nil || got != want {
			t.Errorf("validateDirectory(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"relative", "/", "/downloads", "/downloads/", "/downloadsx/tv", "/etc/passwd", "/downloads/tv/..", "../downloads/tv", "/downloads/tv/\x00", strings.Repeat("/downloads/a", 200)} {
		if got, err := s.validateDirectory(bad); err == nil {
			t.Errorf("validateDirectory(%q) accepted as %q", bad, got)
		}
	}
}

func TestParseQuery(t *testing.T) {
	q := parseQuery(url.Values{})
	if q.Sort != "added" || q.Order != "desc" {
		t.Errorf("defaults: %+v", q)
	}
	q = parseQuery(url.Values{"sort": {"name"}, "order": {"sideways"}, "state": {"bogus"}, "q": {"  x  "}})
	if q.Sort != "name" || q.Order != "asc" || q.State != "" || q.Search != "x" {
		t.Errorf("sanitised: %+v", q)
	}
	q = parseQuery(url.Values{"sort": {"nonsense"}, "label": {strings.Repeat("a", 500)}})
	if q.Sort != "added" || len(q.Label) != maxFilterLen {
		t.Errorf("clipped: %+v", q)
	}
	q = parseQuery(url.Values{"fstate": {""}, "state": {"seeding"}, "flabel": {"tv"}, "label": {"movies"}, "tracker": {"x"}})
	if q.State != "" || q.Label != "tv" || q.Tracker != "x" {
		t.Errorf("sidebar overrides: %+v", q)
	}
}

func TestFormatting(t *testing.T) {
	cases := map[int64]string{0: "0 B", 1023: "1023 B", 1024: "1.0 KiB", 1536: "1.5 KiB", 104857600: "100 MiB", 792723456: "756 MiB", 1 << 40: "1.0 TiB"}
	for n, want := range cases {
		if got := fmtBytes(n); got != want {
			t.Errorf("fmtBytes(%d) = %q, want %q", n, got, want)
		}
	}
	if fmtRate(0) != "" || fmtRate(2048) != "2.0 KiB/s" || fmtSpeed(0) != "0 B/s" {
		t.Error("rates")
	}
	if fmtPct(100) != "100%" || fmtPct(12.345) != "12.3%" || fmtRatio(1.5) != "1.50" {
		t.Error("percent/ratio")
	}
}
