package source

import (
	"log/slog"
	"time"
)

// SQLiteBuilt reports whether this binary can read SQLite. Support is compiled
// in only with the build tag "sqlite", because it builds SQLite itself from C,
// which takes a while and is wasted on a gallery that reads DynamoDB.
const SQLiteBuilt = sqliteBuilt

// DefaultSQLiteBusyTimeout is how long a read waits for the bot to finish a
// write before it gives up. The bot holds its write lock for one small
// transaction, so this is rarely reached.
const DefaultSQLiteBusyTimeout = 5 * time.Second

// SQLiteOptions configure the SQLite reader.
type SQLiteOptions struct {
	// Path is the bot's database file (its DALIBOT_DB). Required.
	Path string
	// StoreDays must equal the bot's DALIBOT_STORE_DAYS. The bot removes old
	// rows only when it starts, so a row last written more than StoreDays ago
	// is treated as expired. Required.
	StoreDays int
	// BusyTimeout is how long to wait for a lock held by the bot.
	// Zero means DefaultSQLiteBusyTimeout.
	BusyTimeout time.Duration
	// Logger receives a warning for rows that cannot be decoded. May be nil.
	Logger *slog.Logger
}

