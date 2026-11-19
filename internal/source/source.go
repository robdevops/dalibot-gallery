// Package source reads the bot's records and turns them into the gallery's
// listing: one tile per generated image, newest first, served from an
// in-memory snapshot that is rebuilt on a timer and swapped in atomically.
//
// A Source only reads raw records. Build applies every listing rule (ttl
// filter, chat allowlist, de-duplication, ordering), so the two backends,
// Dynamo (the bot's DynamoDB table) and SQLite (the database file of its
// self-hosted mode), implement nothing but Source.
//
// Telegram file_ids never leave the server: they are not part of Tile or
// Image, and the only way to get one is Snapshot.FileID.
package source

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"strings"
)

// ProbeID is the id the bot reads at start-up to check its table. It is never
// written, and is ignored if it ever appears.
const ProbeID = "check:check"

// KeyLen is the length of an image key.
const KeyLen = 22

// Record is one item of the bot's store, as stored.
type Record struct {
	ID      string // "<chat_id>:<message_id>"; "<chat_id>:f<file_unique_id>" for an imported picture
	Prompt  string // the user's original prompt
	Command string // the command used; may be empty in a malformed item
	Current string // style key shown in Telegram; may name a missing image
	TTL     int64  // epoch seconds; 0 when missing. From SQLite: the row's updated column plus the store days
	// Created is when the picture was first sent, for records the bot imported from
	// forwarded messages. 0 for ordinary records, which are dated from the ttl.
	Created int64
	Kind    string              // what the bot made: "" for a picture from a prompt, "variation", "palette"...
	Images  map[string]RawImage // style key -> image
}

// RawImage is one stored image of a record. FileID is a Telegram file_id and
// must never be sent to a browser.
type RawImage struct {
	FileID    string
	Caption   string // the prompt actually used for this style
	Technical bool   // drawn from a planned technical prompt: a diagram, cutaway, schematic... (see TechnicalHeader)
}

// TechnicalHeader starts the prompt the bot plans for a technical picture (a diagram, cutaway,
// cross-section, schematic, infographic or labelled picture), which it keeps as the image's "base"
// (lib/openrouter.py). Those are not shown in the gallery: they are for explaining, not looking at.
const TechnicalHeader = "Accurate technical illustration:"

func rawImage(fileID, caption, base string) RawImage {
	return RawImage{FileID: fileID, Caption: caption, Technical: strings.HasPrefix(base, TechnicalHeader)}
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
