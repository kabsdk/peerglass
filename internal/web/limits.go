package web

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/kabsdk/peerglass/internal/rtorrent"
)

// parseLimit reads one limit field in KiB/s. Empty means unlimited, as 0
// does.
func parseLimit(name, v string) (int64, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 || n > rtorrent.MaxLimitKiB {
		return 0, fmt.Errorf("the %s limit must be a whole number of KiB/s from 0 to %d", name, rtorrent.MaxLimitKiB)
	}
	return n, nil
}

// handleLimits sets rTorrent's global speed limits until it restarts (F13).
// "Unlimited" posts clear=1 and lifts both.
func (s *Server) handleLimits(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxActionBody)
	if err := r.ParseForm(); err != nil {
		s.flash(w, http.StatusBadRequest, flashData{Kind: "error", Lines: []string{"Could not read the request: " + err.Error()}})
		return
	}
	var down, up int64
	if r.Form.Get("clear") == "" {
		var err error
		if down, err = parseLimit("download", r.Form.Get("down")); err == nil {
			up, err = parseLimit("upload", r.Form.Get("up"))
		}
		if err != nil {
			s.flash(w, http.StatusBadRequest, flashData{Kind: "error", Lines: []string{err.Error() + "."}})
			return
		}
	}
	if err := s.client.SetLimits(r.Context(), down, up); err != nil {
		status, text := s.describe(err)
		s.flash(w, status, flashData{Kind: "error", Lines: []string{text}})
		return
	}
	s.invalidate()
	s.log.Info("action", "verb", "limits", "down_kib", down, "up_kib", up)
	refresh(w)
	limits := rtorrent.Limits{Down: down * 1024, Up: up * 1024, Known: true}
	s.flash(w, http.StatusOK, flashData{
		Kind:   "ok",
		Lines:  []string{fmt.Sprintf("Limits: download %s, upload %s, until rTorrent restarts.", limitText(limits.Down), limitText(limits.Up))},
		Limits: &limits,
	})
}

// handleLimitsForm serves the limits form with the limits in force, when the
// popover opens.
func (s *Server) handleLimitsForm(w http.ResponseWriter, r *http.Request) {
	ov, err := s.overview(r)
	if err != nil {
		s.describe(err)
		w.Header().Set("HX-Reswap", "none")
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	s.render(w, http.StatusOK, "limitsform", ov.Limits)
}

func limitText(bytesPerSec int64) string {
	if bytesPerSec <= 0 {
		return "unlimited"
	}
	return fmtSpeed(bytesPerSec)
}
