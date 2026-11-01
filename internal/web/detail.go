package web

import (
	"errors"
	"net/http"

	"github.com/kabsdk/peerglass/internal/rtorrent"
)

type tab struct {
	Key    string
	Title  string
	Active bool
}

// detailData feeds the detail panel (F5).
type detailData struct {
	Hash        string
	Tab         string
	Tabs        []tab
	Torrent     *rtorrent.Torrent
	Trackers    []rtorrent.Tracker
	Files       []rtorrent.File
	FinishedDir string
}

// emptyDetail is the panel with nothing selected, or a note on why the
// selection went away.
type emptyDetail struct {
	Message string
}

func tabsFor(active string) []tab {
	out := []tab{{"general", "General", false}, {"trackers", "Trackers", false}, {"files", "Files", false}}
	for i := range out {
		out[i].Active = out[i].Key == active
	}
	return out
}

func (s *Server) buildDetail(r *http.Request, hash, tabKey string) (*detailData, error) {
	ctx := r.Context()
	t, err := s.client.Torrent(ctx, hash)
	if err != nil {
		return nil, err
	}
	switch tabKey {
	case "trackers", "files":
	default:
		tabKey = "general"
	}
	d := &detailData{Hash: hash, Tab: tabKey, Tabs: tabsFor(tabKey), Torrent: &t}
	switch tabKey {
	case "trackers":
		d.Trackers, err = s.client.Trackers(ctx, hash)
	case "files":
		d.Files, err = s.client.Files(ctx, hash)
	default:
		// d.finished_dir lives in rtorrent.rc; without it there is simply
		// nothing to show.
		if !t.Complete {
			if dir, derr := s.client.FinishedDir(ctx, hash); derr == nil {
				d.FinishedDir = dir
			}
		}
	}
	if err != nil {
		return nil, err
	}
	return d, nil
}

// handleDetail serves the detail panel, which refreshes itself. A torrent
// that is gone empties the panel; an unreachable rTorrent leaves the panel
// as it is and shows the banner (F11).
func (s *Server) handleDetail(w http.ResponseWriter, r *http.Request) {
	hash := validHashOr(r.PathValue("hash"))
	if hash == "" {
		s.render(w, http.StatusNotFound, "detail-empty", emptyDetail{Message: "That is not a torrent hash."})
		return
	}
	d, err := s.buildDetail(r, hash, r.URL.Query().Get("tab"))
	if err != nil {
		var f *rtorrent.Fault
		if errors.As(err, &f) {
			s.log.Info("detail: torrent gone", "hash", hash, "fault", f.Message)
			s.render(w, http.StatusNotFound, "detail-empty", emptyDetail{Message: "This torrent is no longer in rTorrent."})
			return
		}
		_, text := s.describe(err)
		w.Header().Set("HX-Reswap", "none")
		s.render(w, http.StatusBadGateway, "list-error", &listData{Error: text + ". Retrying every 3 seconds."})
		return
	}
	// A click on a row or a tab refreshes the list at once, so the row is
	// highlighted without waiting for the next poll. The panel's own poll
	// identifies itself and must not.
	if r.Header.Get("HX-Trigger") != "detail" {
		refresh(w)
	}
	s.render(w, http.StatusOK, "detail", d)
}

func (s *Server) handleDetailEmpty(w http.ResponseWriter, r *http.Request) {
	s.render(w, http.StatusOK, "detail-empty", emptyDetail{})
}

func (s *Server) handleFlashClear(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
}
