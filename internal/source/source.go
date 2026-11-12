// Package source reads the bot's records and turns them into the gallery's
// listing: one tile per generated image, newest first, served from an
// in-memory snapshot that is rebuilt on a timer and swapped in atomically.
//
// A Source only reads raw records. Build applies every listing rule (ttl
// filter, chat allowlist, de-duplication, ordering), so a second backend
// (for example the bot's SQLite store) needs to implement nothing but Source.
//
// Telegram file_ids never leave the server: they are not part of Tile or
// Image, and the only way to get one is Snapshot.FileID.
package source

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
)

// ProbeID is the id the bot reads at start-up to check its table. It is never
// written, and is ignored if it ever appears.
const ProbeID = "check:check"

// KeyLen is the length of an image key.
const KeyLen = 22

// Record is one item of the bot's store, as stored.
type Record struct {
	ID      string              // "<chat_id>:<message_id>"
	Prompt  string              // the user's original prompt
	Command string              // the command used; may be empty in a malformed item
	Current string              // style key shown in Telegram; may name a missing image
	TTL     int64               // epoch seconds; 0 when missing
	Images  map[string]RawImage // style key -> image
}

// RawImage is one stored image of a record. FileID is a Telegram file_id and
// must never be sent to a browser.
type RawImage struct {
	FileID  string
	Caption string // the prompt actually used for this style
}

// Source reads every record of the bot's store. Implementations must not
// write to the store. Records may be returned in any order and may include
// expired, empty or probe records: Build filters them.
type Source interface {
	Records(ctx context.Context) ([]Record, error)
}

// Static is a fixed list of records, for tests.
type Static []Record

// Records returns a copy of the list.
func (s Static) Records(context.Context) ([]Record, error) {
	return append([]Record(nil), s...), nil
}

// Key is the public name of an image: base64url(sha256(file_id))[:22].
// It names the image in URLs and in the disk cache, and cannot be turned back
// into the file_id.
func Key(fileID string) string {
	sum := sha256.Sum256([]byte(fileID))
	return base64.RawURLEncoding.EncodeToString(sum[:])[:KeyLen]
}

// ValidKey reports whether s has the form of a key (22 base64url characters).
// It says nothing about whether the key is in the snapshot: use Snapshot.Has.
func ValidKey(s string) bool {
	if len(s) != KeyLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}
