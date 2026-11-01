package web

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/kabsdk/peerglass/internal/rtorrent"
)

// menuData feeds the context menu (menu.html).
type menuData struct {
	// Name and Label are the right-clicked torrent's; the menu is titled
	// with the name when it is the only one selected.
	Name  string
	Label string
	// Count is how many rows the browser has selected; the actions post the
	// selection itself (hx-include), so this only words the menu.
	Count  int
	Labels []string
}

// handleMenu renders the context menu for the selection that the
// right-clicked torrent belongs to. The buttons post to the same fixed
// actions as the toolbar (S1).
func (s *Server) handleMenu(w http.ResponseWriter, r *http.Request) {
	hash := validHashOr(r.PathValue("hash"))
	if hash == "" {
		s.render(w, http.StatusNotFound, "menu-gone", nil)
		return
	}
	t, err := s.client.Torrent(r.Context(), hash)
	if err != nil {
		var f *rtorrent.Fault
		if errors.As(err, &f) {
			s.render(w, http.StatusNotFound, "menu-gone", nil)
			return
		}
		_, text := s.describe(err)
		s.render(w, http.StatusBadGateway, "flash", flashData{Kind: "error", Lines: []string{text}})
		return
	}
	d := menuData{Name: t.Name, Label: t.Label, Labels: s.labels}
	if n, _ := strconv.Atoi(r.URL.Query().Get("n")); n > 1 {
		d.Count = n
	}
	s.render(w, http.StatusOK, "menu", d)
}
