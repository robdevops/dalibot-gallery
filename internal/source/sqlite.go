//go:build sqlite

package source

const sqliteBuilt = true

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"strconv"
	"time"

	sqlite3 "github.com/mattn/go-sqlite3" // also registers the "sqlite3" database/sql driver
)

// sqliteQuery reads every row of the bot's one table. The casts make the
// column types certain whatever was stored, so no row can fail to scan.
const sqliteQuery = `SELECT CAST(chat_id AS TEXT), CAST(message_id AS TEXT), CAST(data AS TEXT), CAST(updated AS INTEGER) FROM messages`

// SQLite reads the database of the bot's self-hosted mode (lib/store.py,
// class SqliteStore):
//
//	messages(chat_id TEXT, message_id TEXT, data TEXT, updated INTEGER, PRIMARY KEY (chat_id, message_id))
//
// data is the record as JSON; updated is when the bot last wrote the row.
//
// The file belongs to the bot. It is opened read-only for each read and
// closed again, so the gallery never creates, changes or write-locks it, holds
// a shared lock only while one SELECT runs, and notices a file that was
// replaced. The bot's rollback journal is never touched: if the bot was
// killed in the middle of a write, reads fail until the bot has opened the
// database again and so repaired it. A database in WAL mode is refused.
type SQLite struct {
	path string
	dsn  string
	span int64
	busy time.Duration
	log  *slog.Logger
}

// NewSQLite builds a reader. It does not touch the file: the first read
// happens in Records, so a database that is missing when the gallery starts
// is picked up as soon as it appears.
func NewSQLite(opt SQLiteOptions) (*SQLite, error) {
	if opt.Path == "" {
		return nil, errors.New("source: the SQLite database path is required")
	}
	if opt.StoreDays < 1 {
		return nil, errors.New("source: store days must be at least 1")
	}
	if opt.BusyTimeout <= 0 {
		opt.BusyTimeout = DefaultSQLiteBusyTimeout
	}
	log := opt.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &SQLite{
		path: opt.Path,
		dsn:  sqliteDSN(opt.Path),
		span: int64(opt.StoreDays) * 86400,
		busy: opt.BusyTimeout,
		log:  log,
	}, nil
}

// sqliteDSN is the read-only connection string for path.
//
//   - mode=ro opens the file with O_RDONLY and never creates it. (Not
//     immutable=1: that would skip locking, and the bot writes to the file.)
//   - _query_only=1 makes SQLite itself refuse every statement that writes.
//   - _busy_timeout=0: a locked file is reported at once, and Records does
//     the waiting itself, so that the wait ends when its context does.
//
// The path is percent-encoded, so that '?', '#' and '%' in it stay part of the name.
func sqliteDSN(path string) string {
	return "file:" + (&url.URL{Path: path}).EscapedPath() + "?mode=ro&_query_only=1&_busy_timeout=0"
}

// sqliteRetry is the pause between two attempts to read a locked file.
const sqliteRetry = 20 * time.Millisecond

// sqliteData is the JSON in the data column. "base" and "kind" are
// deliberately not decoded. created is only a hint for ordering.
type sqliteData struct {
	Prompt  string                 `json:"prompt"`
	Command string                 `json:"command"`
	Current string                 `json:"current"`
	Created json.RawMessage        `json:"created"`
	Images  map[string]sqliteImage `json:"images"`
}

type sqliteImage struct {
	FileID  string `json:"file_id"`
	Caption string `json:"caption"`
}

// Records reads every row. A row that does not have the bot's shape is
// skipped (and counted in one warning), so one bad row cannot empty the
// gallery. If the file is missing, unreadable or locked for longer than the
// busy timeout, the error is returned and the caller keeps what it has.
func (s *SQLite) Records(ctx context.Context) ([]Record, error) {
	deadline := time.Now().Add(s.busy)
	for {
		records, skipped, err := s.read(ctx)
		if err == nil {
			if skipped > 0 {
				s.log.Warn("skipped rows that do not have the bot's shape", "count", skipped)
			}
			return records, nil
		}
		// Busy means the bot is in the middle of a write: try again shortly.
		if sqliteBusy(err) && ctx.Err() == nil && time.Now().Before(deadline) {
			select {
			case <-ctx.Done():
			case <-time.After(sqliteRetry):
			}
			continue
		}
		return nil, fmt.Errorf("source: reading the SQLite database %q: %w", s.path, err)
	}
}

// sqliteBusy reports whether err is SQLite saying that another connection holds a lock.
func sqliteBusy(err error) bool {
	e, ok := errors.AsType[sqlite3.Error](err)
	return ok && (e.Code == sqlite3.ErrBusy || e.Code == sqlite3.ErrLocked)
}

// errSQLiteWAL is returned for a database in write-ahead-log mode.
var errSQLiteWAL = errors.New("the database is in WAL mode, which cannot be read without creating and writing its -shm and -wal files; " +
	"the bot uses SQLite's default rollback journal (to go back: PRAGMA journal_mode=DELETE)")

// checkJournalMode refuses a database in WAL mode before SQLite opens it:
// even a read-only connection creates and writes the -shm and -wal files next
// to such a database, and the gallery writes nothing there. The bot never
// turns WAL on. Bytes 18 and 19 of the header are 2 in WAL mode and 1
// otherwise. Anything else that is wrong with the file is left for SQLite to
// report.
func checkJournalMode(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var header [20]byte
	if _, err := io.ReadFull(f, header[:]); err != nil || string(header[:16]) != "SQLite format 3\x00" {
		return nil
	}
	if header[18] == 2 || header[19] == 2 {
		return errSQLiteWAL
	}
	return nil
}

// read opens the file, runs the one SELECT and closes the file again.
func (s *SQLite) read(ctx context.Context) (records []Record, skipped int, err error) {
	if err := checkJournalMode(s.path); err != nil {
		return nil, 0, err
	}
	db, err := sql.Open("sqlite3", s.dsn)
	if err != nil {
		return nil, 0, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	rows, err := db.QueryContext(ctx, sqliteQuery)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var chat, message, data sql.NullString
		var updated sql.NullInt64
		if err := rows.Scan(&chat, &message, &data, &updated); err != nil {
			return nil, 0, err
		}
		r, err := decodeRow(chat.String, message.String, data.String, updated.Int64, s.span)
		if err != nil {
			skipped++
			continue
		}
		records = append(records, r)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return records, skipped, rows.Close()
}

// decodeRow maps one row to a Record. The row's date is its updated column;
// Build's rules are written in terms of the ttl (expired when ttl < now, dated
// ttl - store days), so the ttl is given as updated + store days, which is
// when the bot would purge the row.
func decodeRow(chat, message, data string, updated, span int64) (Record, error) {
	var d sqliteData
	if err := json.Unmarshal([]byte(data), &d); err != nil {
		return Record{}, err
	}
	r := Record{
		ID:      chat + ":" + message,
		Prompt:  d.Prompt,
		Command: d.Command,
		Current: d.Current,
		TTL:     updated + span,
		Created: sqliteCreated(d.Created),
	}
	if len(d.Images) > 0 {
		r.Images = make(map[string]RawImage, len(d.Images))
		for k, img := range d.Images {
			r.Images[k] = RawImage(img)
		}
	}
	return r, nil
}

// sqliteCreated reads the optional "created" value. As with DynamoDB, one
// that is missing or not a whole number counts as absent.
func sqliteCreated(raw json.RawMessage) int64 {
	v, err := strconv.ParseInt(string(raw), 10, 64)
	if err != nil {
		return 0
	}
	return v
}
