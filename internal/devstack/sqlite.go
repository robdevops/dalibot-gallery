//go:build sqlite

package devstack

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	_ "github.com/mattn/go-sqlite3" // the "sqlite3" database/sql driver
)

// sqliteSchema is the statement the bot's SqliteStore creates its table with.
const sqliteSchema = `CREATE TABLE IF NOT EXISTS messages (chat_id TEXT, message_id TEXT, data TEXT, updated INTEGER, PRIMARY KEY (chat_id, message_id))`

// SQLiteDB is a stand-in for the database file of the bot's self-hosted
// mode, written the way the bot writes it. It is the dev stack's own file:
// SeedSQLite refuses a path that already exists, so this code can never write
// to a real bot's database.
type SQLiteDB struct {
	Path      string
	storeDays int
}

// SeedSQLite creates a new database at path, as the bot does (the file is
// private to its owner, the table is the bot's), and fills it with the same
// records Seed writes to DynamoDB: an item's ttl becomes the row's updated
// column (ttl minus the store days), and an item without a ttl a row without
// a date.
func SeedSQLite(ctx context.Context, path string, now time.Time, storeDays int) (*SQLiteDB, *Seeded, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, nil, fmt.Errorf("devstack creates its own SQLite database and never opens an existing one: %w", err)
	}
	if err := f.Close(); err != nil {
		return nil, nil, err
	}
	db := &SQLiteDB{Path: path, storeDays: storeDays}
	sd := build(now, storeDays)
	if err := db.write(ctx, sd.items, true); err != nil {
		return nil, nil, fmt.Errorf("seeding SQLite: %w", err)
	}
	return db, &sd.out, nil
}

// AddNew writes count new single-image records dated now, like the package's
// AddNew does for DynamoDB, and returns their file_ids.
func (db *SQLiteDB) AddNew(ctx context.Context, now time.Time, count int) ([]string, error) {
	var ids []string
	var items []map[string]types.AttributeValue
	for range count {
		item, fileID := newItem(now, db.storeDays)
		items = append(items, item)
		ids = append(ids, fileID)
	}
	if err := db.write(ctx, items, false); err != nil {
		return nil, fmt.Errorf("adding rows to SQLite: %w", err)
	}
	return ids, nil
}

// Hold takes the database's exclusive lock, as the bot has while it commits
// a write, and returns the function that gives it up. While it is held no
// other connection can read.
func (db *SQLiteDB) Hold(ctx context.Context) (release func() error, err error) {
	conn, err := db.open()
	if err != nil {
		return nil, err
	}
	if _, err := conn.ExecContext(ctx, "BEGIN EXCLUSIVE"); err != nil {
		conn.Close()
		return nil, err
	}
	return func() error {
		_, err := conn.ExecContext(context.Background(), "ROLLBACK")
		return errors.Join(err, conn.Close())
	}, nil
}

// open connects as the bot does: read-write, waiting up to 10 s for a lock.
func (db *SQLiteDB) open() (*sql.DB, error) {
	if strings.ContainsAny(db.Path, "?#%") {
		return nil, fmt.Errorf("devstack: the SQLite path %q must not contain '?', '#' or '%%'", db.Path)
	}
	conn, err := sql.Open("sqlite3", "file:"+db.Path+"?mode=rw&_busy_timeout=10000&_txlock=immediate")
	if err != nil {
		return nil, err
	}
	conn.SetMaxOpenConns(1)
	return conn, nil
}

// write stores items in one immediate transaction, the bot's way of writing.
func (db *SQLiteDB) write(ctx context.Context, items []map[string]types.AttributeValue, create bool) error {
	conn, err := db.open()
	if err != nil {
		return err
	}
	defer conn.Close()
	if create {
		if _, err := conn.ExecContext(ctx, sqliteSchema); err != nil {
			return err
		}
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	span := int64(db.storeDays) * 86400
	for _, item := range items {
		chat, message, data, updated, err := sqliteRow(item, span)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO messages VALUES (?, ?, ?, ?)`, chat, message, data, updated); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// sqliteRow turns a DynamoDB item into the row the bot's SqliteStore would
// hold for the same record: the id split into its two columns, every other
// attribute but the ttl as JSON, and the ttl as the time of the last write.
func sqliteRow(item map[string]types.AttributeValue, span int64) (chat, message, data string, updated any, err error) {
	record := map[string]any{}
	for k, v := range item {
		switch k {
		case "id":
			id, _ := v.(*types.AttributeValueMemberS)
			if id == nil {
				return "", "", "", nil, errors.New("devstack: an item's id is not a string")
			}
			// An id with no ':' cannot be a row; keep it as a chat with an empty message, which is as malformed.
			if chat, message, _ = strings.Cut(id.Value, ":"); strings.Contains(message, ":") {
				return "", "", "", nil, fmt.Errorf("devstack: unexpected id %q", id.Value)
			}
		case "ttl":
			n, _ := v.(*types.AttributeValueMemberN)
			if n == nil {
				return "", "", "", nil, errors.New("devstack: an item's ttl is not a number")
			}
			ttl, err := strconv.ParseInt(n.Value, 10, 64)
			if err != nil {
				return "", "", "", nil, err
			}
			updated = ttl - span
		default:
			if record[k], err = plain(v); err != nil {
				return "", "", "", nil, err
			}
		}
	}
	raw, err := json.Marshal(record)
	return chat, message, string(raw), updated, err
}

// plain is the JSON value of an attribute, for the types the seeder uses.
func plain(v types.AttributeValue) (any, error) {
	switch v := v.(type) {
	case *types.AttributeValueMemberS:
		return v.Value, nil
	case *types.AttributeValueMemberN:
		return json.Number(v.Value), nil
	case *types.AttributeValueMemberNULL:
		return nil, nil
	case *types.AttributeValueMemberM:
		m := make(map[string]any, len(v.Value))
		for k, e := range v.Value {
			p, err := plain(e)
			if err != nil {
				return nil, err
			}
			m[k] = p
		}
		return m, nil
	}
	return nil, fmt.Errorf("devstack: attribute type %T has no JSON form here", v)
}
