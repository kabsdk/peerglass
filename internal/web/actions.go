package web

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"path"
	"strings"
	"unicode"

	"github.com/kabsdk/peerglass/internal/rtorrent"
)

// Input limits (S8).
const (
	maxActionBody   = 1 << 20  // selections and labels
	maxUploadBody   = 64 << 20 // one add request with several files
	maxTorrentSize  = 16 << 20 // rTorrent's XML-RPC limit
	maxTorrentFiles = 20
	maxMagnetLen    = 4096
	maxPathLen      = 1024
	maxFailureLines = 5
)

// flashData feeds the message shown after an action.
type flashData struct {
	// Kind is ok, warn or error.
	Kind  string
	Lines []string
	// ResetAdd re-sends an empty add form after a successful add.
	ResetAdd bool
	// Limits, when set, re-sends the speed limits popover, closed and
	// showing these limits.
	Limits *rtorrent.Limits
	Labels []string
	Root   string
}

func (s *Server) flash(w http.ResponseWriter, status int, f flashData) {
	if f.ResetAdd {
		f.Labels = s.labels
		f.Root = s.root
	}
	s.render(w, status, "flash", f)
}

// refresh tells htmx to reload the list right away instead of at the next
// poll. It fires after the response has settled, so out-of-band updates to
// the form (the open torrent, for one) are already in place when the list
// request reads them; a plain HX-Trigger fires before the swap.
func refresh(w http.ResponseWriter) {
	w.Header().Set("HX-Trigger-After-Settle", "refresh")
}

// selection reads and validates the selected hashes of an action request.
func (s *Server) selection(w http.ResponseWriter, r *http.Request) ([]string, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxActionBody)
	if err := r.ParseForm(); err != nil {
		s.flash(w, http.StatusBadRequest, flashData{Kind: "error", Lines: []string{"Could not read the request: " + err.Error()}})
		return nil, false
	}
	var hashes []string
	for _, h := range r.Form["h"] {
		if !rtorrent.ValidHash(h) {
			s.flash(w, http.StatusBadRequest, flashData{Kind: "error", Lines: []string{"The selection contains something that is not a torrent hash."}})
			return nil, false
		}
		hashes = append(hashes, strings.ToUpper(h))
	}
	if len(hashes) == 0 {
		s.flash(w, http.StatusBadRequest, flashData{Kind: "error", Lines: []string{"Select at least one torrent first."}})
		return nil, false
	}
	return hashes, true
}

// bulkAction runs one rTorrent command per selected torrent (F7, F9, F10)
// and reports what happened to each.
func (s *Server) bulkAction(verb string, fn func(context.Context, string) error) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hashes, ok := s.selection(w, r)
		if !ok {
			return
		}
		var failed []string
		for _, h := range hashes {
			if err := fn(r.Context(), h); err != nil {
				_, text := s.describe(err)
				failed = append(failed, h[:8]+"…: "+text)
			}
		}
		s.invalidate()
		s.log.Info("action", "verb", verb, "torrents", len(hashes), "failed", len(failed))
		refresh(w)
		s.report(w, fmt.Sprintf("%s %s", verb, plural(len(hashes)-len(failed), "torrent")), failed)
	})
}

// report renders the outcome of a bulk action: 200 when everything worked,
// 502 when nothing did, 200 with a warning in between.
func (s *Server) report(w http.ResponseWriter, summary string, failed []string) {
	f := flashData{Kind: "ok", Lines: []string{summary + "."}}
	status := http.StatusOK
	if len(failed) > 0 {
		f.Kind = "warn"
		if strings.HasPrefix(summary, "0 ") || strings.Contains(summary, " 0 torrent") {
			f.Kind = "error"
			status = http.StatusBadGateway
		}
		f.Lines = append(f.Lines, failedLines(failed)...)
	}
	s.flash(w, status, f)
}

func failedLines(failed []string) []string {
	if len(failed) > maxFailureLines {
		return append(failed[:maxFailureLines], fmt.Sprintf("… and %d more", len(failed)-maxFailureLines))
	}
	return failed
}

// validateLabel accepts "" or one of the configured labels (F6, F8).
func (s *Server) validateLabel(label string) (string, error) {
	if label == "" {
		return "", nil
	}
	for _, l := range s.labels {
		if l == label {
			return l, nil
		}
	}
	return "", fmt.Errorf("unknown label %q; the labels are %s", label, strings.Join(s.labels, ", "))
}

// validateDirectory accepts "" (rTorrent's default) or an absolute path
// strictly below the root, with no "..", and returns it cleaned (S6).
// rTorrent's own guard in rtorrent.rc is the second line of defence.
func (s *Server) validateDirectory(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", nil
	}
	if len(p) > maxPathLen {
		return "", errors.New("the path is too long")
	}
	for _, r := range p {
		if unicode.IsControl(r) {
			return "", errors.New("the path contains control characters")
		}
	}
	if !strings.HasPrefix(p, "/") {
		return "", fmt.Errorf("the path must be absolute, below %s/", s.root)
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return "", errors.New("the path must not contain \"..\"")
		}
	}
	clean := path.Clean(p)
	if !strings.HasPrefix(clean, s.root+"/") || clean == s.root+"/" {
		return "", fmt.Errorf("the path must be below %s/", s.root)
	}
	return clean, nil
}

// handleLabel sets or clears the label of the selected torrents (F8).
func (s *Server) handleLabel(w http.ResponseWriter, r *http.Request) {
	hashes, ok := s.selection(w, r)
	if !ok {
		return
	}
	label, err := s.validateLabel(r.Form.Get("setlabel"))
	if err != nil {
		s.flash(w, http.StatusBadRequest, flashData{Kind: "error", Lines: []string{err.Error()}})
		return
	}
	var failed []string
	for _, h := range hashes {
		if err := s.client.SetLabel(r.Context(), h, label); err != nil {
			_, text := s.describe(err)
			failed = append(failed, h[:8]+"…: "+text)
		}
	}
	s.invalidate()
	s.log.Info("action", "verb", "label", "label", label, "torrents", len(hashes), "failed", len(failed))
	refresh(w)
	verb := fmt.Sprintf("Set label %q on", label)
	if label == "" {
		verb = "Cleared the label of"
	}
	s.report(w, fmt.Sprintf("%s %s", verb, plural(len(hashes)-len(failed), "torrent")), failed)
}

type upload struct {
	filename string
	data     []byte
	info     rtorrent.TorrentFile
}

// handleAdd adds .torrent files and/or a magnet link (F6). Everything is
// validated before the first call to rTorrent, and afterwards the list is
// checked so the user learns whether each torrent really appeared.
func (s *Server) handleAdd(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBody)
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		var tooBig *http.MaxBytesError
		msg := "Could not read the upload: " + err.Error()
		if errors.As(err, &tooBig) {
			msg = fmt.Sprintf("The upload is larger than %s.", fmtBytes(maxUploadBody))
		}
		s.flash(w, http.StatusBadRequest, flashData{Kind: "error", Lines: []string{msg}})
		return
	}
	defer r.MultipartForm.RemoveAll()
	bad := func(msg string) {
		s.flash(w, http.StatusBadRequest, flashData{Kind: "error", Lines: []string{msg}})
	}

	label, err := s.validateLabel(r.FormValue("label"))
	if err != nil {
		bad(err.Error())
		return
	}
	dir, err := s.validateDirectory(r.FormValue("path"))
	if err != nil {
		bad(err.Error())
		return
	}
	opts := rtorrent.LoadOptions{Directory: dir, Label: label, Start: r.FormValue("start") != ""}

	var files []*multipart.FileHeader
	for _, fh := range r.MultipartForm.File["torrent"] {
		if fh.Filename == "" && fh.Size == 0 {
			continue // an empty file field
		}
		files = append(files, fh)
	}
	magnet := strings.TrimSpace(r.FormValue("magnet"))
	if len(files) == 0 && magnet == "" {
		bad("Choose at least one .torrent file or paste a magnet link.")
		return
	}
	if len(files) > maxTorrentFiles {
		bad(fmt.Sprintf("At most %d files per add.", maxTorrentFiles))
		return
	}
	uploads := make([]upload, 0, len(files))
	for _, fh := range files {
		data, err := readUpload(fh)
		if err != nil {
			bad(fmt.Sprintf("%s: %s", fh.Filename, err))
			return
		}
		info, err := rtorrent.ParseTorrentFile(data)
		if err != nil {
			bad(fmt.Sprintf("%s: %s", fh.Filename, err))
			return
		}
		uploads = append(uploads, upload{filename: fh.Filename, data: data, info: info})
	}
	var magnetHash string
	if magnet != "" {
		if len(magnet) > maxMagnetLen {
			bad("The magnet link is too long.")
			return
		}
		magnetHash, err = rtorrent.MagnetHash(magnet)
		if err != nil {
			bad("That is not a magnet link; it should start with magnet:?xt=urn:btih:")
			return
		}
	}

	ctx := r.Context()
	before, err := s.hashesPresent(ctx)
	if err != nil {
		status, text := s.describe(err)
		s.flash(w, status, flashData{Kind: "error", Lines: []string{text}})
		return
	}
	var lines, failed []string
	type pending struct{ hash, what string }
	var added []pending
	for _, u := range uploads {
		what := u.info.Name
		if before[u.info.Hash] {
			lines = append(lines, "Already in rTorrent: "+what)
			continue
		}
		if err := s.client.LoadTorrent(ctx, u.data, opts); err != nil {
			_, text := s.describe(err)
			failed = append(failed, what+": "+text)
			continue
		}
		added = append(added, pending{u.info.Hash, what})
	}
	if magnetHash != "" {
		// The link itself is never shown or logged: its tracker URLs can
		// carry passkeys (S5).
		what := "magnet " + magnetHash[:8] + "…"
		if before[magnetHash] {
			lines = append(lines, "Already in rTorrent: "+what)
		} else if err := s.client.LoadMagnet(ctx, magnet, opts); err != nil {
			_, text := s.describe(err)
			failed = append(failed, what+": "+text)
		} else {
			added = append(added, pending{magnetHash, what})
		}
	}
	s.invalidate()
	after, err := s.hashesPresent(ctx)
	for _, a := range added {
		switch {
		case err != nil:
			lines = append(lines, "Sent to rTorrent: "+a.what)
		case after[a.hash]:
			lines = append(lines, "Added: "+a.what)
			s.log.Info("added", "hash", a.hash, "name", a.what, "label", label, "dir", dir, "start", opts.Start)
		default:
			failed = append(failed, a.what+": rTorrent accepted it but it did not appear. A torrent erased earlier cannot come back until rTorrent restarts.")
		}
	}
	refresh(w)
	f := flashData{Kind: "ok", Lines: append(lines, failedLines(failed)...), ResetAdd: len(failed) == 0}
	status := http.StatusOK
	if len(failed) > 0 {
		f.Kind = "warn"
		if len(lines) == 0 {
			f.Kind = "error"
			status = http.StatusBadGateway
		}
	}
	s.flash(w, status, f)
}

func readUpload(fh *multipart.FileHeader) ([]byte, error) {
	if fh.Size > maxTorrentSize {
		return nil, fmt.Errorf("larger than %s, rTorrent's limit", fmtBytes(maxTorrentSize))
	}
	f, err := fh.Open()
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxTorrentSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxTorrentSize {
		return nil, fmt.Errorf("larger than %s, rTorrent's limit", fmtBytes(maxTorrentSize))
	}
	return data, nil
}

func (s *Server) hashesPresent(ctx context.Context) (map[string]bool, error) {
	torrents, err := s.client.Torrents(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(torrents))
	for _, t := range torrents {
		out[strings.ToUpper(t.Hash)] = true
	}
	return out, nil
}
