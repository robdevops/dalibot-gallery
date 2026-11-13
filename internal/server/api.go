package server

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/robdevops/dalibot-gallery/internal/source"
)

// tilesResponse is the body of GET /api/tiles.
type tilesResponse struct {
	Tiles  []source.Tile `json:"tiles"`
	Next   *string       `json:"next"`
	Newest *string       `json:"newest"`
	V      string        `json:"v"`
}

// newResponse is the body of GET /api/tiles/new.
type newResponse struct {
	Count  int     `json:"count"`
	Newest *string `json:"newest"`
}

// newTilesResponse is newResponse with tiles=1: "tiles" is always present, even when empty.
type newTilesResponse struct {
	Count  int           `json:"count"`
	Newest *string       `json:"newest"`
	V      string        `json:"v"`
	Tiles  []source.Tile `json:"tiles"`
}

// cursorOrNull maps "" to JSON null.
func cursorOrNull(c string) *string {
	if c == "" {
		return nil
	}
	return &c
}

// newestCursor is the sealed cursor of the snapshot's first tile, "" when it is empty.
func (s *Server) newestCursor(snap *source.Snapshot) string {
	if first := snap.Newest(1); len(first) > 0 {
		return s.cursors.seal(first[0].Cursor())
	}
	return ""
}

// tilesFor is the tiles as this viewer may see them. In a public gallery
// the record id, which contains the Telegram chat id, is sent to admins
// only. When viewing needs the token, everyone who is let in gets it.
func (s *Server) tilesFor(level access, tiles []source.Tile) []source.Tile {
	if tiles == nil {
		return []source.Tile{}
	}
	if s.cfg.Public && level < accessAdmin {
		return source.WithoutIDs(tiles)
	}
	return tiles
}

func (s *Server) handleTiles(w http.ResponseWriter, r *http.Request, level access) {
	q := r.URL.Query()
	limit := DefaultLimit
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			s.jsonError(w, r, http.StatusBadRequest, "limit must be a whole number of at least 1")
			return
		}
		limit = min(n, MaxLimit)
	}
	snap := s.store.Snapshot()
	cursor, ok := s.cursors.open(q.Get("cursor"))
	if !ok {
		s.jsonError(w, r, http.StatusBadRequest, "bad cursor")
		return
	}
	tiles, next, err := snap.Page(cursor, limit)
	if err != nil {
		s.jsonError(w, r, http.StatusBadRequest, "bad cursor")
		return
	}
	s.json(w, r, http.StatusOK, tilesResponse{
		Tiles: s.tilesFor(level, tiles), Next: cursorOrNull(s.cursors.seal(next)), Newest: cursorOrNull(s.newestCursor(snap)), V: s.tag,
	})
}

func (s *Server) handleNew(w http.ResponseWriter, r *http.Request, level access) {
	q := r.URL.Query()
	snap := s.store.Snapshot()
	since, ok := s.cursors.open(q.Get("since"))
	if !ok {
		s.jsonError(w, r, http.StatusBadRequest, "bad cursor")
		return
	}
	count, newest, err := snap.NewSince(since)
	if err != nil {
		s.jsonError(w, r, http.StatusBadRequest, "bad cursor")
		return
	}
	newest = s.cursors.seal(newest)
	if q.Get("tiles") != "1" {
		s.json(w, r, http.StatusOK, newResponse{Count: count, Newest: cursorOrNull(newest)})
		return
	}
	tiles := s.tilesFor(level, snap.Newest(min(count, MaxLimit)))
	s.json(w, r, http.StatusOK, newTilesResponse{Count: count, Newest: cursorOrNull(newest), V: s.tag, Tiles: tiles})
}

// indexData and indexTile are what index.html is executed with.
type indexData struct {
	V     string
	Tiles []indexTile
	Next  string // sealed cursor of the next page, "" at the end
	// Admin: the visitor has admin access. The page carries it as
	// <html data-admin="1">, which is how the script knows without asking.
	Admin bool
	// AdminLogin: an admin token is configured and the visitor is not an
	// admin, so the page offers a discreet link to /login.
	AdminLogin bool
	// SignOut: the visitor has a session cookie, so the page offers to drop it.
	SignOut bool
}

type indexTile struct {
	Key, Alt, Src240, Src480, Full string
}

// handleIndex renders the shell. The first page of tiles is inside <noscript>,
// with a "Load more" link carrying ?cursor=.
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request, level access) {
	snap := s.store.Snapshot()
	cursor, ok := s.cursors.open(r.URL.Query().Get("cursor"))
	if !ok {
		plain(w, http.StatusBadRequest, "bad cursor\n")
		return
	}
	tiles, next, err := snap.Page(cursor, DefaultLimit)
	if err != nil {
		plain(w, http.StatusBadRequest, "bad cursor\n")
		return
	}
	data := indexData{
		V: s.tag, Next: s.cursors.seal(next), Tiles: make([]indexTile, 0, len(tiles)),
		Admin:      level == accessAdmin,
		AdminLogin: s.cfg.AdminEnabled() && level < accessAdmin,
		SignOut:    s.cookieAccess(r) > accessNone,
	}
	for _, t := range tiles {
		data.Tiles = append(data.Tiles, indexTile{
			Key:    t.Key,
			Alt:    t.Alt(),
			Src240: "/img/" + t.Key + "-240.webp?v=" + s.tag,
			Src480: "/img/" + t.Key + "-480.webp?v=" + s.tag,
			Full:   "/img/" + t.Key + ".jpg",
		})
	}
	s.html(w, http.StatusOK, s.index, data)
}

// html executes a template into a buffer first, so a failure cannot leave half a page.
func (s *Server) html(w http.ResponseWriter, status int, t *template.Template, data any) {
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		s.log.Error("rendering a page failed", "template", t.Name(), "error", err)
		plain(w, http.StatusInternalServerError, "internal error\n")
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Length", strconv.Itoa(buf.Len()))
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

var gzipPool = sync.Pool{New: func() any {
	zw, _ := gzip.NewWriterLevel(nil, gzip.DefaultCompression)
	return zw
}}

// acceptsGzip reports whether the request's Accept-Encoding allows gzip.
func acceptsGzip(r *http.Request) bool {
	for _, line := range r.Header.Values("Accept-Encoding") {
		for part := range strings.SplitSeq(line, ",") {
			name, params, _ := strings.Cut(strings.TrimSpace(part), ";")
			if name = strings.ToLower(strings.TrimSpace(name)); name != "gzip" && name != "*" {
				continue
			}
			q := strings.ReplaceAll(strings.ToLower(params), " ", "")
			if v, ok := strings.CutPrefix(q, "q="); ok {
				if f, err := strconv.ParseFloat(v, 64); err == nil && f == 0 {
					return false
				}
			}
			return true
		}
	}
	return false
}

// json writes v as JSON. JSON is the only kind of response that is gzip-encoded.
func (s *Server) json(w http.ResponseWriter, r *http.Request, status int, v any) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(true)
	if err := enc.Encode(v); err != nil {
		s.log.Error("encoding a JSON response failed", "error", err)
		plain(w, http.StatusInternalServerError, "internal error\n")
		return
	}
	body := buf.Bytes()
	h := w.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("Vary", "Accept-Encoding")
	if acceptsGzip(r) && len(body) >= 256 {
		var zbuf bytes.Buffer
		zw := gzipPool.Get().(*gzip.Writer)
		zw.Reset(&zbuf)
		_, werr := zw.Write(body)
		cerr := zw.Close()
		gzipPool.Put(zw)
		if werr == nil && cerr == nil {
			h.Set("Content-Encoding", "gzip")
			body = zbuf.Bytes()
		}
	}
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func (s *Server) jsonError(w http.ResponseWriter, r *http.Request, status int, msg string) {
	s.json(w, r, status, map[string]string{"error": msg})
}
