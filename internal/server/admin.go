package server

import (
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/robdevops/dalibot-gallery/internal/hidden"
	"github.com/robdevops/dalibot-gallery/internal/source"
)

// The admin's routes. They exist only when an admin token is configured and
// there is a hidden list, and only for a request with admin access; route
// answers everyone else before handleAdmin is reached.
//
//	GET  /admin/hidden             the page of hidden images, each with "Restore"
//	GET  /api/admin/hidden         the same list as JSON
//	POST /api/admin/hide/<key>     hide an image that is in the listing
//	POST /api/admin/restore/<key>  put a hidden image back
//
// Hiding never touches the bot's data: it adds the image's key to the
// gallery's own list (internal/hidden) and builds the snapshot again, so the
// change is in force when the response is sent.
const (
	adminHiddenPage = "/admin/hidden"
	adminAPI        = "/api/admin/"
	adminListPath   = adminAPI + "hidden"
	adminHidePath   = adminAPI + "hide/"
	adminRestore    = adminAPI + "restore/"
)

// adminRoutes reports whether the admin's routes exist at all.
func (s *Server) adminRoutes() bool { return s.cfg.AdminEnabled() && s.hidden != nil }

func isAdminPath(p string) bool {
	return p == adminHiddenPage || strings.HasPrefix(p, adminAPI)
}

// hiddenItem is one hidden image in GET /api/admin/hidden. It has no record
// id: the list is about images.
type hiddenItem struct {
	Key      string `json:"key"`
	Style    string `json:"style"`
	Label    string `json:"label"`
	Caption  string `json:"caption"`
	Prompt   string `json:"prompt"`
	Command  string `json:"command"`
	Updated  int64  `json:"updated"`
	HiddenAt int64  `json:"hidden_at"` // epoch seconds
}

// hiddenResponse is the body of GET /api/admin/hidden.
type hiddenResponse struct {
	// Hidden: the hidden images that are still in the bot's records, most
	// recently hidden first. Only these can be shown.
	Hidden []hiddenItem `json:"hidden"`
	// Gone: entries of the list whose image is no longer in the bot's
	// records as the gallery reads them (expired, deleted by the bot, or
	// outside GALLERY_CHATS). They stay on the list in case the image returns.
	Gone int    `json:"gone"`
	V    string `json:"v"`
}

// changeResponse is the body of a successful hide or restore.
type changeResponse struct {
	Key    string `json:"key"`
	Hidden bool   `json:"hidden"`
}

// hiddenItems lists the hidden images of snap, most recently hidden first,
// and counts the list's other entries.
func (s *Server) hiddenItems(snap *source.Snapshot) (items []hiddenItem, gone int) {
	items = []hiddenItem{}
	for _, t := range snap.Hidden() {
		at, ok := s.hidden.At(t.Key)
		if !ok {
			continue // restored since the snapshot was built
		}
		items = append(items, hiddenItem{
			Key: t.Key, Style: t.Style, Label: t.Label, Caption: t.Caption, Prompt: t.Prompt,
			Command: t.Command, Updated: t.Updated, HiddenAt: at,
		})
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].HiddenAt > items[j].HiddenAt })
	return items, max(0, s.hidden.Len()-len(items))
}

// handleAdmin serves the admin's routes to an admin.
func (s *Server) handleAdmin(w http.ResponseWriter, r *http.Request, p string) {
	api := strings.HasPrefix(p, adminAPI)
	get := r.Method == http.MethodGet || r.Method == http.MethodHead
	method := func(allow string) {
		w.Header().Set("Allow", allow)
		if api {
			s.jsonError(w, r, http.StatusMethodNotAllowed, "method not allowed")
		} else {
			plain(w, http.StatusMethodNotAllowed, "method not allowed\n")
		}
	}
	switch {
	case p == adminHiddenPage || p == adminListPath:
		if !get {
			method("GET, HEAD")
			return
		}
		items, gone := s.hiddenItems(s.store.Snapshot())
		if api {
			s.json(w, r, http.StatusOK, hiddenResponse{Hidden: items, Gone: gone, V: s.tag})
			return
		}
		s.handleHiddenPage(w, r, items, gone)
	case strings.HasPrefix(p, adminHidePath):
		if r.Method != http.MethodPost {
			method("POST")
			return
		}
		s.handleChange(w, r, p[len(adminHidePath):], true)
	case strings.HasPrefix(p, adminRestore):
		if r.Method != http.MethodPost {
			method("POST")
			return
		}
		s.handleChange(w, r, p[len(adminRestore):], false)
	default:
		if !get {
			method("GET, HEAD")
			return
		}
		s.notFound(w, r, api)
	}
}

// handleChange hides or restores one image and answers once the listing
// reflects it.
func (s *Server) handleChange(w http.ResponseWriter, r *http.Request, key string, hide bool) {
	// A change posted by another site's page is refused, as at /login. (The
	// session cookie is SameSite=Strict, but a Basic password the browser
	// remembers is not.)
	if err := crossOrigin.Check(r); err != nil {
		s.jsonError(w, r, http.StatusForbidden, "cross-origin request refused")
		return
	}
	if !source.ValidKey(key) {
		s.jsonError(w, r, http.StatusNotFound, "not found")
		return
	}
	var err error
	if hide {
		// Only an image of the bot's records can be hidden. Hiding one that
		// is hidden already changes nothing and succeeds.
		if !s.store.Snapshot().Known(key) && !s.hidden.Has(key) {
			s.jsonError(w, r, http.StatusNotFound, "not found")
			return
		}
		_, err = s.hidden.Hide(key)
	} else {
		// A hidden image can be restored even after it has left the bot's records.
		if !s.hidden.Has(key) {
			s.jsonError(w, r, http.StatusNotFound, "not found")
			return
		}
		_, err = s.hidden.Restore(key)
	}
	switch {
	case errors.Is(err, hidden.ErrFull):
		s.jsonError(w, r, http.StatusConflict, "too many hidden images")
		return
	case err != nil:
		s.log.Error("saving the list of hidden images failed; nothing was changed", "key", key, "hide", hide, "error", err)
		s.jsonError(w, r, http.StatusInternalServerError, "the change could not be saved")
		return
	}
	// In force before the answer: the listing and the image routes are served from the new snapshot.
	s.store.Rebuild()
	if hide {
		s.log.Info("image hidden", "key", key, "remote", s.clientAddr(r))
	} else {
		s.log.Info("image restored", "key", key, "remote", s.clientAddr(r))
	}
	s.json(w, r, http.StatusOK, changeResponse{Key: key, Hidden: hide})
}

// hiddenData and hiddenTile are what hidden.html is executed with.
type hiddenData struct {
	Tiles   []hiddenTile
	Gone    int  // entries whose image is no longer in the bot's records
	SignOut bool // the admin has a session cookie to drop
}

type hiddenTile struct {
	Key, Alt, Text, Src240, Src480, Full string
}

func (s *Server) handleHiddenPage(w http.ResponseWriter, r *http.Request, items []hiddenItem, gone int) {
	data := hiddenData{Tiles: make([]hiddenTile, 0, len(items)), Gone: gone, SignOut: s.cookieAccess(r) > accessNone}
	for _, it := range items {
		alt := it.Caption
		if strings.TrimSpace(alt) == "" {
			alt = it.Prompt
		}
		data.Tiles = append(data.Tiles, hiddenTile{
			Key:    it.Key,
			Alt:    alt,
			Text:   "/" + it.Command + " · " + it.Label,
			Src240: "/img/" + it.Key + "-240.webp?v=" + s.tag,
			Src480: "/img/" + it.Key + "-480.webp?v=" + s.tag,
			Full:   "/img/" + it.Key + ".jpg",
		})
	}
	s.html(w, http.StatusOK, s.hiddenPage, data)
}
