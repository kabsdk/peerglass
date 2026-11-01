package web

import (
	"bytes"
	"cmp"
	"encoding/json"
	"hash/fnv"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kabsdk/peerglass/internal/rtorrent"
)

// Query is the list state the browser keeps in its filter form and sends
// with every refresh, so sort, filters and search survive updates (F3, F4).
type Query struct {
	Sort    string
	Order   string
	State   string
	Label   string
	Tracker string
	Search  string
}

// noneOption is the filter value meaning "no label" or "no tracker".
const noneOption = "(none)"

type column struct {
	Key   string
	Title string
	// Numeric columns (and dates) sort descending on the first click.
	Numeric bool
}

var columns = []column{
	{"name", "Name", false},
	{"size", "Size", true},
	{"progress", "Progress", true},
	{"state", "State", false},
	{"status", "Status", false},
	{"down", "Down", true},
	{"up", "Up", true},
	{"ratio", "Ratio", true},
	{"label", "Label", false},
	{"tracker", "Tracker", false},
	{"added", "Added", true},
}

var states = map[string]bool{
	"downloading": true, "seeding": true, "completed": true, "stopped": true, "checking": true, "inactive": true, "error": true,
}

const maxFilterLen = 200

// parseQuery reads the list state from the request. A header click sends
// sortby/orderby and a sidebar click fstate/flabel/ftracker; those win over
// the form's own hidden fields, which the response then updates.
func parseQuery(v url.Values) Query {
	q := Query{
		Sort:    firstOf(v, "sortby", "sort"),
		Order:   firstOf(v, "orderby", "order"),
		State:   override(v, "fstate", "state"),
		Label:   clip(override(v, "flabel", "label")),
		Tracker: clip(override(v, "ftracker", "tracker")),
		Search:  clip(strings.TrimSpace(v.Get("q"))),
	}
	col := columnByKey(q.Sort)
	if col == nil {
		col = columnByKey("added")
		q.Sort = col.Key
	}
	if q.Order != "asc" && q.Order != "desc" {
		q.Order = "asc"
		if col.Numeric {
			q.Order = "desc"
		}
	}
	if !states[q.State] {
		q.State = ""
	}
	return q
}

// override prefers the click parameter when it is present at all, even
// empty: "All" in the sidebar sends fstate= to clear the state filter.
func override(v url.Values, click, form string) string {
	if v.Has(click) {
		return v.Get(click)
	}
	return v.Get(form)
}

func firstOf(v url.Values, keys ...string) string {
	for _, k := range keys {
		if s := v.Get(k); s != "" {
			return s
		}
	}
	return ""
}

func clip(s string) string {
	if len(s) > maxFilterLen {
		return s[:maxFilterLen]
	}
	return s
}

func columnByKey(key string) *column {
	for i := range columns {
		if columns[i].Key == key {
			return &columns[i]
		}
	}
	return nil
}

// header is one sortable column heading.
type header struct {
	Key    string
	Title  string
	Active bool
	Order  string
	// Vals is the hx-vals JSON a click sends: the column and the order it
	// should get.
	Vals string
}

func headers(q Query) []header {
	out := make([]header, len(columns))
	for i, c := range columns {
		next := "asc"
		if c.Numeric {
			next = "desc"
		}
		if c.Key == q.Sort {
			if q.Order == "asc" {
				next = "desc"
			} else {
				next = "asc"
			}
		}
		vals, _ := json.Marshal(map[string]string{"sortby": c.Key, "orderby": next})
		out[i] = header{Key: c.Key, Title: c.Title, Active: c.Key == q.Sort, Order: q.Order, Vals: string(vals)}
	}
	return out
}

// row is one torrent as the table shows it. The selection is not part of
// it: the browser keeps the selection (static/rows.js) and never sends it with
// a GET, so a large one cannot outgrow request-line limits on the way.
type row struct {
	*rtorrent.Torrent
	Open bool
}

// totals counts every torrent, before filtering, for the header line and
// the sidebar.
type totals struct {
	All, Shown                                                   int
	Downloading, Seeding, Completed, Stopped, Checking, Inactive int
	Errors                                                       int
	NoLabel, NoTracker                                           int
	Labels, Trackers                                             map[string]int
	Down, Up                                                     int64
	// Limits are rTorrent's global limits, shown next to the rates.
	Limits rtorrent.Limits
}

// sideItem is one sidebar entry: a filter value with its count (F3).
type sideItem struct {
	Title  string
	Count  int
	Active bool
	// Vals is the hx-vals JSON a click sends, such as {"fstate":"seeding"}.
	Vals string
}

type sidebar struct {
	States, Labels, Trackers []sideItem
}

// overviewCacheTTL is how long one fetch of the torrents and limits serves
// every request. Polls and keystrokes from several tabs then share one call,
// and nothing shown is more than this much older than the poll interval.
const overviewCacheTTL = time.Second

// overview returns the torrents and global limits, from the cache when it is
// fresh.
func (s *Server) overview(r *http.Request) (rtorrent.Overview, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cachedAt.IsZero() || time.Since(s.cachedAt) > overviewCacheTTL {
		o, err := s.client.Overview(r.Context())
		if err != nil {
			return rtorrent.Overview{}, err
		}
		s.cached, s.cachedAt = o, time.Now()
	}
	return s.cached, nil
}

// invalidate drops the cached overview. Every action calls it, so the list
// refresh that follows shows the action's effect, not a second-old state.
func (s *Server) invalidate() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cachedAt = time.Time{}
}

// renderedRow is one table row as sent to a browser.
type renderedRow struct {
	hash string
	sum  uint64
	html []byte
}

// renderedList is what a browser shows under one version: the table head
// (sort marks) and the rows in order. The next poll from that browser is
// compared against it, and when the rows are the same in the same order only
// the changed ones are sent, as out-of-band swaps, instead of the whole
// table (F4, N1).
type renderedList struct {
	headSum uint64
	rows    []renderedRow
	at      time.Time
}

const (
	maxVersions = 32
	versionTTL  = 5 * time.Minute
)

func (s *Server) remember(version string, l *renderedList) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l.at = time.Now()
	s.versions[version] = l
	s.versionOrder = append(s.versionOrder, version)
	for len(s.versionOrder) > maxVersions {
		delete(s.versions, s.versionOrder[0])
		s.versionOrder = s.versionOrder[1:]
	}
}

func (s *Server) recall(version string) *renderedList {
	s.mu.Lock()
	defer s.mu.Unlock()
	l := s.versions[version]
	if l == nil || time.Since(l.at) > versionTTL {
		return nil
	}
	return l
}

// listData feeds the list templates.
type listData struct {
	Rows    []row
	Query   Query
	Headers []header
	Totals  totals
	Sidebar sidebar
	// Error is the F11 banner; when set, Rows is empty.
	Error string
	// Version fingerprints the rendered rows. The browser sends it back as
	// v, and when the rows have not changed the table is not swapped at
	// all, which spares the browser a large DOM update every poll.
	Version string
}

func matches(t *rtorrent.Torrent, q Query) bool {
	switch q.State {
	case "":
	case "error":
		if t.Message == "" {
			return false
		}
	case "completed":
		if !t.Complete {
			return false
		}
	default:
		if t.State().String() != q.State {
			return false
		}
	}
	switch q.Label {
	case "":
	case noneOption:
		if t.Label != "" {
			return false
		}
	default:
		if t.Label != q.Label {
			return false
		}
	}
	switch q.Tracker {
	case "":
	case noneOption:
		if t.TrackerHost != "" {
			return false
		}
	default:
		if t.TrackerHost != q.Tracker {
			return false
		}
	}
	if q.Search != "" {
		needle := strings.ToLower(q.Search)
		if !strings.Contains(strings.ToLower(t.Name), needle) && !strings.HasPrefix(strings.ToLower(t.Hash), needle) {
			return false
		}
	}
	return true
}

func compareBy(key string) func(a, b *rtorrent.Torrent) int {
	switch key {
	case "name":
		return func(a, b *rtorrent.Torrent) int {
			return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
		}
	case "size":
		return func(a, b *rtorrent.Torrent) int { return cmp.Compare(a.Size, b.Size) }
	case "progress":
		return func(a, b *rtorrent.Torrent) int { return cmp.Compare(a.Progress(), b.Progress()) }
	case "state":
		return func(a, b *rtorrent.Torrent) int { return cmp.Compare(a.State(), b.State()) }
	case "status":
		return func(a, b *rtorrent.Torrent) int { return strings.Compare(a.Status(), b.Status()) }
	case "down":
		return func(a, b *rtorrent.Torrent) int { return cmp.Compare(a.DownRate, b.DownRate) }
	case "up":
		return func(a, b *rtorrent.Torrent) int { return cmp.Compare(a.UpRate, b.UpRate) }
	case "ratio":
		return func(a, b *rtorrent.Torrent) int { return cmp.Compare(a.Ratio, b.Ratio) }
	case "label":
		return func(a, b *rtorrent.Torrent) int { return strings.Compare(a.Label, b.Label) }
	case "tracker":
		return func(a, b *rtorrent.Torrent) int { return strings.Compare(a.TrackerHost, b.TrackerHost) }
	}
	return func(a, b *rtorrent.Torrent) int { return a.Added.Compare(b.Added) }
}

func sortRows(rows []row, q Query) {
	by := compareBy(q.Sort)
	slices.SortStableFunc(rows, func(a, b row) int {
		c := by(a.Torrent, b.Torrent)
		if q.Order == "desc" {
			c = -c
		}
		if c == 0 {
			c = strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
		}
		return c
	})
}

// listOptions are the request parts beyond the Query that shape the list.
type listOptions struct {
	Open string
	// Version is the fingerprint of the rows the browser shows now.
	Version string
}

// buildList fetches every torrent in one call (N1) and applies the query.
// On error the returned data carries the banner text and the error is
// returned too, so the caller can pick the status.
func (s *Server) buildList(r *http.Request, q Query, o listOptions) (*listData, error) {
	d := &listData{Query: q, Headers: headers(q)}
	d.Totals.Labels = map[string]int{}
	d.Totals.Trackers = map[string]int{}
	ov, err := s.overview(r)
	if err != nil {
		_, d.Error = s.describe(err)
		d.Error += ". Retrying every 2 seconds."
		d.Sidebar = s.buildSidebar(q, d.Totals)
		return d, err
	}
	torrents := ov.Torrents
	d.Totals.Limits = ov.Limits
	d.Totals.All = len(torrents)
	for i := range torrents {
		t := &torrents[i]
		d.Totals.Down += t.DownRate
		d.Totals.Up += t.UpRate
		switch t.State() {
		case rtorrent.StateDownloading:
			d.Totals.Downloading++
		case rtorrent.StateSeeding:
			d.Totals.Seeding++
		case rtorrent.StateStopped:
			d.Totals.Stopped++
		case rtorrent.StateChecking:
			d.Totals.Checking++
		case rtorrent.StateInactive:
			d.Totals.Inactive++
		}
		if t.Message != "" {
			d.Totals.Errors++
		}
		if t.Complete {
			d.Totals.Completed++
		}
		if t.Label == "" {
			d.Totals.NoLabel++
		} else {
			d.Totals.Labels[t.Label]++
		}
		if t.TrackerHost == "" {
			d.Totals.NoTracker++
		} else {
			d.Totals.Trackers[t.TrackerHost]++
		}
		if !matches(t, q) {
			continue
		}
		d.Rows = append(d.Rows, row{Torrent: t, Open: t.Hash == o.Open})
	}
	d.Totals.Shown = len(d.Rows)
	sortRows(d.Rows, q)
	d.Sidebar = s.buildSidebar(q, d.Totals)
	return d, nil
}

// buildSidebar lists every filter value with its count: the states, the
// configured labels (then any others the torrents carry) and the tracker
// hosts. The active value of each group is marked.
func (s *Server) buildSidebar(q Query, t totals) sidebar {
	item := func(param, value, title string, count int, active bool) sideItem {
		vals, _ := json.Marshal(map[string]string{param: value})
		return sideItem{Title: title, Count: count, Active: active, Vals: string(vals)}
	}
	var sb sidebar
	for _, st := range []struct {
		key, title string
		count      int
	}{
		{"", "All", t.All},
		{"downloading", "Downloading", t.Downloading},
		{"seeding", "Seeding", t.Seeding},
		{"completed", "Completed", t.Completed},
		{"stopped", "Stopped", t.Stopped},
		{"checking", "Checking", t.Checking},
		{"inactive", "Inactive", t.Inactive},
		{"error", "With errors", t.Errors},
	} {
		sb.States = append(sb.States, item("fstate", st.key, st.title, st.count, q.State == st.key))
	}

	sb.Labels = append(sb.Labels,
		item("flabel", "", "All", t.All, q.Label == ""),
		item("flabel", noneOption, "No label", t.NoLabel, q.Label == noneOption))
	seen := map[string]bool{}
	for _, l := range s.labels {
		sb.Labels = append(sb.Labels, item("flabel", l, l, t.Labels[l], q.Label == l))
		seen[l] = true
	}
	extra := map[string]bool{}
	for l := range t.Labels {
		if !seen[l] {
			extra[l] = true
		}
	}
	if q.Label != "" && q.Label != noneOption && !seen[q.Label] {
		extra[q.Label] = true
	}
	for _, l := range sortedKeys(extra) {
		sb.Labels = append(sb.Labels, item("flabel", l, l, t.Labels[l], q.Label == l))
	}

	sb.Trackers = append(sb.Trackers,
		item("ftracker", "", "All", t.All, q.Tracker == ""),
		item("ftracker", noneOption, "No tracker", t.NoTracker, q.Tracker == noneOption))
	hosts := map[string]bool{}
	for h := range t.Trackers {
		hosts[h] = true
	}
	if q.Tracker != "" && q.Tracker != noneOption {
		hosts[q.Tracker] = true
	}
	for _, h := range sortedKeys(hosts) {
		sb.Trackers = append(sb.Trackers, item("ftracker", h, h, t.Trackers[h], q.Tracker == h))
	}
	return sb
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func validHashOr(h string) string {
	if rtorrent.ValidHash(h) {
		return strings.ToUpper(h)
	}
	return ""
}

func (s *Server) listOptionsFrom(v url.Values) listOptions {
	return listOptions{
		Open:    validHashOr(v.Get("open")),
		Version: v.Get("v"),
	}
}

// handleList serves the table fragment the page polls. When rTorrent is
// unreachable it answers with just the banner and tells htmx not to swap
// the table, so the last good list stays visible until the next poll
// succeeds (F11).
// renderList renders the table as head, rows and foot, exactly as the
// list-body template composes them, and fingerprints the result.
func (s *Server) renderList(d *listData) (head, foot []byte, l *renderedList, version string, err error) {
	var hb, fb bytes.Buffer
	if err := s.tmpl.ExecuteTemplate(&hb, "list-head", d); err != nil {
		return nil, nil, nil, "", err
	}
	if err := s.tmpl.ExecuteTemplate(&fb, "list-foot", d); err != nil {
		return nil, nil, nil, "", err
	}
	l = &renderedList{rows: make([]renderedRow, 0, len(d.Rows))}
	all := fnv.New64a()
	all.Write(hb.Bytes())
	all.Write(fb.Bytes())
	h := fnv.New64a()
	h.Write(hb.Bytes())
	l.headSum = h.Sum64()
	var rb bytes.Buffer
	for _, row := range d.Rows {
		rb.Reset()
		if err := s.tmpl.ExecuteTemplate(&rb, "row", row); err != nil {
			return nil, nil, nil, "", err
		}
		h.Reset()
		h.Write(rb.Bytes())
		sum := h.Sum64()
		all.Write(rb.Bytes())
		l.rows = append(l.rows, renderedRow{hash: row.Hash, sum: sum, html: bytes.Clone(rb.Bytes())})
	}
	return hb.Bytes(), fb.Bytes(), l, strconv.FormatUint(all.Sum64(), 36), nil
}

// sameShape reports whether two renderings have the same head and the same
// rows in the same order, so one can be patched into the other row by row.
func sameShape(a, b *renderedList) bool {
	if a.headSum != b.headSum || len(a.rows) != len(b.rows) {
		return false
	}
	for i := range a.rows {
		if a.rows[i].hash != b.rows[i].hash {
			return false
		}
	}
	return true
}

var trOpen = []byte("<tr ")

// handleList serves the table the page polls. Against the version the
// browser reports it sends, in order of preference: nothing but the
// out-of-band parts when nothing changed; only the changed rows when the
// rows are the same in the same order; otherwise the whole table. When
// rTorrent is unreachable it sends just the banner and tells htmx not to
// swap, so the last good list stays visible until a poll succeeds (F11).
func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	v := r.URL.Query()
	o := s.listOptionsFrom(v)
	d, err := s.buildList(r, parseQuery(v), o)
	if err != nil {
		w.Header().Set("HX-Reswap", "none")
		s.render(w, http.StatusBadGateway, "list-error", d)
		return
	}
	head, foot, l, version, err := s.renderList(d)
	if err != nil {
		s.log.Error("template", "name", "list", "err", err)
		http.Error(w, "template error", http.StatusInternalServerError)
		return
	}
	d.Version = version
	var oob bytes.Buffer
	if err := s.tmpl.ExecuteTemplate(&oob, "list-oob", d); err != nil {
		s.log.Error("template", "name", "list-oob", "err", err)
		http.Error(w, "template error", http.StatusInternalServerError)
		return
	}
	s.remember(version, l)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if o.Version == version {
		w.Header().Set("HX-Reswap", "none")
		w.WriteHeader(http.StatusOK)
		oob.WriteTo(w)
		return
	}
	if prev := s.recall(o.Version); prev != nil && sameShape(prev, l) {
		w.Header().Set("HX-Reswap", "none")
		w.WriteHeader(http.StatusOK)
		for i, row := range l.rows {
			if row.sum != prev.rows[i].sum {
				w.Write(bytes.Replace(row.html, trOpen, []byte(`<tr hx-swap-oob="true" `), 1))
			}
		}
		oob.WriteTo(w)
		return
	}
	w.WriteHeader(http.StatusOK)
	w.Write(head)
	for _, row := range l.rows {
		w.Write(row.html)
	}
	w.Write(foot)
	oob.WriteTo(w)
}

// pageData feeds the full page.
type pageData struct {
	List   *listData
	Labels []string
	Root   string
	Open   string
	Detail *detailData
	Empty  emptyDetail
	// Theme is "light" or "dark" when the browser chose one (F14), or ""
	// to follow the system.
	Theme string
}

// themeOf reads the browser's theme choice from its cookie, which
// static/theme.js sets. Anything but "light" or "dark" means the system's.
func themeOf(r *http.Request) string {
	c, err := r.Cookie("theme")
	if err != nil {
		return ""
	}
	switch c.Value {
	case "light", "dark":
		return c.Value
	}
	return ""
}

// handlePage renders the whole UI with the list already in it. The same
// query parameters as /torrents apply, so a URL can carry sort, filters
// and the open torrent.
func (s *Server) handlePage(w http.ResponseWriter, r *http.Request) {
	v := r.URL.Query()
	o := s.listOptionsFrom(v)
	d, _ := s.buildList(r, parseQuery(v), o)
	if _, _, l, version, err := s.renderList(d); err == nil {
		d.Version = version
		s.remember(version, l)
	}
	p := pageData{List: d, Labels: s.labels, Root: s.root, Open: o.Open, Theme: themeOf(r)}
	if o.Open != "" {
		if det, err := s.buildDetail(r, o.Open, v.Get("tab")); err == nil {
			p.Detail = det
		} else {
			p.Open = ""
		}
	}
	s.render(w, http.StatusOK, "page", p)
}
