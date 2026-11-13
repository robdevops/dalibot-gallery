package server

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
)

// Cursors leave the server sealed.
//
// A cursor of the source package names a sort position and so holds the
// record id, which contains the Telegram chat id; base64 hides nothing. The
// server therefore encrypts every cursor it sends (next, newest, the
// "Load more" link) and accepts only cursors it sealed itself. That does two
// things for a public gallery:
//
//   - a visitor cannot read a chat id out of a cursor;
//   - a visitor cannot make a cursor. A home-made one would be an oracle:
//     the date of a tile is public, so by asking for the page after
//     "<that date>, <guessed id>" and seeing whether the tile is on it, the
//     id could be found by bisection.
//
// A keyed hash of the id would hide it as well, but positions are ordered by
// the id itself, so the server could no longer find the place of a record
// that has since gone from the listing. Encryption keeps the keyset paging
// exactly as it was.
//
// The seal is AES-256-GCM with a nonce derived from the cursor itself (a
// MAC of the plain text under a second key). It is deterministic, so the
// same position always gives the same cursor and a nonce is never used for
// two different texts. Both keys are derived from the Telegram bot token,
// which every gallery has and never sends anywhere but Telegram: cursors
// stay valid across restarts and between instances with the same bot, and
// no key needs storing. The same sealing is used in every mode, so there is
// one cursor format and a shared "Load more" link never carries a chat id.
//
// The plain text is padded before sealing (to cursorPad bytes, or a multiple
// of it for an unusually long one), because GCM keeps the length of what it
// encrypts. Unpadded, the length of a cursor would give away the length of
// the record id, and so tell a group chat (-100 and ten digits) from a
// private one. Every real cursor is shorter than cursorPad, so all cursors
// have the same length.
type cursorBox struct {
	aead cipher.AEAD
	mac  []byte
}

const (
	cursorEncLabel   = "dalibot-gallery cursor encryption v1"
	cursorNonceLabel = "dalibot-gallery cursor nonce v1"
	cursorAAD        = "dalibot-gallery cursor v1"
	maxSealedCursor  = 1024 // above any cursor the source package accepts, padded and sealed
	// cursorPad is the size cursors are padded to. A cursor of the source
	// package is base64 of "1|<updated>|<pos>|<chat id>:<message id>", about
	// 60 bytes for the longest ids Telegram uses today.
	cursorPad = 128
)

func derive(secret []byte, label string) []byte {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(label))
	return m.Sum(nil)
}

// newCursorBox derives the keys from secret. An empty secret (only possible
// outside a validated configuration) gets random keys, good for this process.
func newCursorBox(secret string) (*cursorBox, error) {
	key := []byte(secret)
	if len(key) == 0 {
		key = make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, err
		}
	}
	block, err := aes.NewCipher(derive(key, cursorEncLabel))
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &cursorBox{aead: aead, mac: derive(key, cursorNonceLabel)}, nil
}

// seal turns a cursor of the source package into the one sent to browsers.
// "" (no cursor) stays "".
func (b *cursorBox) seal(plain string) string {
	if plain == "" {
		return ""
	}
	nonce := derive(b.mac, plain)[:b.aead.NonceSize()]
	// Padded with zero bytes, which base64 text never contains.
	padded := make([]byte, (len(plain)+cursorPad-1)/cursorPad*cursorPad)
	copy(padded, plain)
	return base64.RawURLEncoding.EncodeToString(b.aead.Seal(nonce, nonce, padded, []byte(cursorAAD)))
}

// open is the reverse. It reports false for anything this server (or one
// with the same bot token) did not seal. "" stays "".
func (b *cursorBox) open(sealed string) (string, bool) {
	if sealed == "" {
		return "", true
	}
	if len(sealed) > maxSealedCursor {
		return "", false
	}
	raw, err := base64.RawURLEncoding.DecodeString(sealed)
	if err != nil || len(raw) < b.aead.NonceSize()+b.aead.Overhead() {
		return "", false
	}
	n := b.aead.NonceSize()
	plain, err := b.aead.Open(nil, raw[:n], raw[n:], []byte(cursorAAD))
	plain = bytes.TrimRight(plain, "\x00")
	if err != nil || len(plain) == 0 {
		return "", false
	}
	return string(plain), true
}
