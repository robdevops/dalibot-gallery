package source

import (
	"encoding/base64"
	"errors"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/robdevops/dalibot-gallery/internal/styles"
)

// Image is one image of a record, as shown in the lightbox strip.
type Image struct {
	Style   string `json:"style"`   // style key; "_" is the unstyled image
	Label   string `json:"label"`   // display label of the style
	Key     string `json:"key"`     // the image's key, for /img/<key>… URLs
	Caption string `json:"caption"` // the prompt actually used for this image
}

// Tile is one generated image in the listing. Its JSON form is exactly what
// GET /api/tiles returns for a tile; it contains no file_id.
type Tile struct {
	Key     string  `json:"key"`     // this image's key; unique per tile
	ID      string  `json:"id"`      // the record's id, "<chat_id>:<message_id>"
	Style   string  `json:"style"`   // this image's style key
	Label   string  `json:"label"`   // this image's style label
	Caption string  `json:"caption"` // this image's caption
	Updated int64   `json:"updated"` // epoch seconds the record was last drawn to (ttl - store days)
	Prompt  string  `json:"prompt"`  // the record's original prompt
	Command string  `json:"command"` // the record's command, without a slash
	Current string  `json:"current"` // style key of the record's current image; always one of Styles
	Styles  []Image `json:"styles"`  // every image of the record, this one included, in display order

	pos int // index of this image in Styles: the last part of the sort position
}

// Alt is the image's alt text: its caption, falling back to the record's prompt.
func (t Tile) Alt() string {
	if strings.TrimSpace(t.Caption) != "" {
		return t.Caption
	}
	return t.Prompt
}

// Cursor is the opaque keyset cursor that names this tile's sort position.
func (t Tile) Cursor() string {
	return position{t.Updated, t.ID, t.pos}.encode()
}

// Options are the listing rules.
type Options struct {
	// StoreDays must equal the bot's DALIBOT_STORE_DAYS: updated = ttl - StoreDays*86400.
	StoreDays int
	// Chats, when not empty, is the allowlist of chat ids; records of other chats are dropped.
	Chats []string
	// DefaultCommand is shown for a record that has no command. Display only.
	DefaultCommand string
	// Now is the clock used for the ttl filter and the snapshot's build time. Nil means time.Now.
	Now func() time.Time
}

func (o Options) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

// position is a tile's place in the total order: updated desc, id desc, pos asc.
type position struct {
	updated int64
	id      string
	pos     int
}

// before reports whether p sorts before q (p is newer).
func (p position) before(q position) bool {
	if p.updated != q.updated {
		return p.updated > q.updated
	}
	if p.id != q.id {
		return p.id > q.id
	}
	return p.pos < q.pos
}

func (t *Tile) position() position { return position{t.Updated, t.ID, t.pos} }

const cursorVersion = "1"

// encode gives base64url("1|<updated>|<pos>|<id>").
func (p position) encode() string {
	raw := cursorVersion + "|" + strconv.FormatInt(p.updated, 10) + "|" + strconv.Itoa(p.pos) + "|" + p.id
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// ErrBadCursor is returned for a cursor that was not produced by this package.
var ErrBadCursor = errors.New("source: malformed cursor")

const maxCursorLen = 512

func decodeCursor(c string) (position, error) {
	if c == "" || len(c) > maxCursorLen {
		return position{}, ErrBadCursor
	}
	raw, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil {
		return position{}, ErrBadCursor
	}
	parts := strings.SplitN(string(raw), "|", 4)
	if len(parts) != 4 || parts[0] != cursorVersion || parts[3] == "" {
		return position{}, ErrBadCursor
	}
	updated, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return position{}, ErrBadCursor
	}
	pos, err := strconv.Atoi(parts[2])
	if err != nil || pos < 0 {
		return position{}, ErrBadCursor
	}
	return position{updated, parts[3], pos}, nil
}

// Snapshot is an immutable listing. All methods are safe for concurrent use.
// Slices returned by its methods share memory with the snapshot and must not be modified.
type Snapshot struct {
	tiles   []Tile
	byKey   map[string]int    // key -> index in tiles
	fileIDs map[string]string // key -> Telegram file_id; never leaves the server
	built   time.Time
	records int // records that contributed at least one tile
}

// emptySnapshot is what a Store serves before its first successful refresh.
var emptySnapshot = &Snapshot{byKey: map[string]int{}, fileIDs: map[string]string{}}

// Build applies the listing rules to raw records and returns the snapshot:
//
//   - drops the "check:check" probe, ids without a ':', chats outside the
//     allowlist, records whose ttl is before now and records with no images;
//   - orders records by (updated desc, id desc);
//   - removes an image whose file_id already appeared in a newer record (or
//     earlier in the same record), then drops records left with no images;
//   - resolves the current style (current, else "_", else the first image);
//   - emits one tile per remaining image: the current style first, then the
//     style-table order, then unknown keys alphabetically.
func Build(records []Record, opt Options) *Snapshot {
	now := opt.now()
	nowUnix := now.Unix()
	var allow map[string]bool
	if len(opt.Chats) > 0 {
		allow = make(map[string]bool, len(opt.Chats))
		for _, c := range opt.Chats {
			allow[c] = true
		}
	}
	span := int64(opt.StoreDays) * 86400

	type rec struct {
		Record
		updated int64
	}
	kept := make([]rec, 0, len(records))
	seenID := make(map[string]bool, len(records))
	for _, r := range records {
		if r.ID == ProbeID || seenID[r.ID] {
			continue
		}
		chat, message, ok := strings.CutLast(r.ID, ":")
		if !ok || chat == "" || message == "" {
			continue // not "<chat_id>:<message_id>"
		}
		if allow != nil && !allow[chat] {
			continue
		}
		if r.TTL < nowUnix { // DynamoDB deletes expired items lazily
			continue
		}
		if len(r.Images) == 0 { // created, not filled yet
			continue
		}
		seenID[r.ID] = true
		kept = append(kept, rec{r, r.TTL - span})
	}
	sort.Slice(kept, func(i, j int) bool {
		if kept[i].updated != kept[j].updated {
			return kept[i].updated > kept[j].updated
		}
		return kept[i].ID > kept[j].ID
	})

	s := &Snapshot{
		byKey:   make(map[string]int, len(kept)),
		fileIDs: make(map[string]string, len(kept)),
		built:   now,
	}
	seenFile := make(map[string]bool, len(kept))
	for _, r := range kept {
		// Stable order first, so de-duplication inside a record is deterministic.
		keys := make([]string, 0, len(r.Images))
		for k, img := range r.Images {
			if img.FileID != "" {
				keys = append(keys, k)
			}
		}
		sort.Slice(keys, func(i, j int) bool { return styles.Less(keys[i], keys[j]) })
		live := keys[:0]
		for _, k := range keys {
			if id := r.Images[k].FileID; !seenFile[id] {
				seenFile[id] = true
				live = append(live, k)
			}
		}
		if len(live) == 0 {
			continue
		}
		current := resolveCurrent(r.Current, live)
		// Move the current style to the front; the rest keep table order.
		ordered := make([]string, 0, len(live))
		ordered = append(ordered, current)
		for _, k := range live {
			if k != current {
				ordered = append(ordered, k)
			}
		}
		images := make([]Image, len(ordered))
		for i, k := range ordered {
			raw := r.Images[k]
			key := Key(raw.FileID)
			images[i] = Image{Style: k, Label: styles.Label(k), Key: key, Caption: raw.Caption}
			s.fileIDs[key] = raw.FileID
		}
		command := strings.TrimLeft(r.Command, "/")
		if command == "" {
			command = opt.DefaultCommand
		}
		for i, img := range images {
			s.byKey[img.Key] = len(s.tiles)
			s.tiles = append(s.tiles, Tile{
				Key: img.Key, ID: r.ID, Style: img.Style, Label: img.Label, Caption: img.Caption,
				Updated: r.updated, Prompt: r.Prompt, Command: command, Current: current,
				Styles: images, pos: i,
			})
		}
		s.records++
	}
	return s
}

// resolveCurrent applies the fallback rule: current, else "_", else the first key present.
// keys is not empty and is in display order.
func resolveCurrent(current string, keys []string) string {
	has := func(k string) bool {
		return slices.Contains(keys, k)
	}
	if has(current) {
		return current
	}
	if has(styles.Base) {
		return styles.Base
	}
	return keys[0]
}

// Len is the number of tiles.
func (s *Snapshot) Len() int { return len(s.tiles) }

// Records is the number of records that have at least one tile.
func (s *Snapshot) Records() int { return s.records }

// Built is when the snapshot was built; zero for the empty snapshot served before the first refresh.
func (s *Snapshot) Built() time.Time { return s.built }

// index of the first tile that sorts strictly after p.
func (s *Snapshot) after(p position) int {
	return sort.Search(len(s.tiles), func(i int) bool { return p.before(s.tiles[i].position()) })
}

// Page returns about limit tiles that sort after cursor (from the newest when
// cursor is ""), and the cursor for the following page, or "" when there are
// no more tiles.
//
// A page always holds whole records: it takes records while they fit in limit,
// and at least one, so it can be a little shorter than limit, or longer when a
// single record has more images than limit. A short page is not the end of the
// listing; only next == "" is. Because the cursor names the last record
// delivered (a sort position, never an offset), tiles added or removed between
// two calls cause neither duplicates nor gaps, and neither does a record whose
// current style, and with it the order of its own tiles, changes in between.
//
// limit < 1 is treated as 1. A malformed cursor gives ErrBadCursor.
func (s *Snapshot) Page(cursor string, limit int) (tiles []Tile, next string, err error) {
	start := 0
	if cursor != "" {
		p, err := decodeCursor(cursor)
		if err != nil {
			return nil, "", err
		}
		// Past the whole record the cursor names, wherever in it the cursor points.
		p.pos = math.MaxInt
		start = s.after(p)
	}
	if limit < 1 {
		limit = 1
	}
	end := start
	for end < len(s.tiles) && end-start < limit {
		recordEnd := end + len(s.tiles[end].Styles) - s.tiles[end].pos
		if end > start && recordEnd-start > limit {
			break
		}
		end = recordEnd
	}
	tiles = s.tiles[start:end:end]
	if end < len(s.tiles) && end > start {
		next = s.tiles[end-1].Cursor()
	}
	return tiles, next, nil
}

// NewSince counts the tiles that sort before cursor (those newer than the tile
// the cursor names) and returns the cursor of the newest tile, or "" when the
// snapshot is empty. An empty cursor counts every tile.
func (s *Snapshot) NewSince(cursor string) (count int, newest string, err error) {
	if len(s.tiles) > 0 {
		newest = s.tiles[0].Cursor()
	}
	if cursor == "" {
		return len(s.tiles), newest, nil
	}
	p, err := decodeCursor(cursor)
	if err != nil {
		return 0, "", err
	}
	count = sort.Search(len(s.tiles), func(i int) bool { return !s.tiles[i].position().before(p) })
	return count, newest, nil
}

// Newest returns the first n tiles (fewer if the snapshot is smaller), for the warmer.
func (s *Snapshot) Newest(n int) []Tile {
	n = max(0, min(n, len(s.tiles)))
	return s.tiles[:n:n]
}

// Tile returns the tile with the given key.
func (s *Snapshot) Tile(key string) (Tile, bool) {
	i, ok := s.byKey[key]
	if !ok {
		return Tile{}, false
	}
	return s.tiles[i], true
}

// Has reports whether key names an image in this snapshot. Only such keys may be served.
func (s *Snapshot) Has(key string) bool {
	_, ok := s.byKey[key]
	return ok
}

// FileID returns the Telegram file_id behind key. The result is a secret of
// the server: pass it to the Telegram client only; never log it or send it to a browser.
func (s *Snapshot) FileID(key string) (string, bool) {
	id, ok := s.fileIDs[key]
	return id, ok
}

// Keys returns every key in the snapshot, newest first.
func (s *Snapshot) Keys() []string {
	keys := make([]string, len(s.tiles))
	for i := range s.tiles {
		keys[i] = s.tiles[i].Key
	}
	return keys
}
