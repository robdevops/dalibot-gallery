//go:build !sqlite

package source

import (
	"context"
	"errors"
)

const sqliteBuilt = false

// SQLite stands in for the SQLite reader in a binary built without it.
type SQLite struct{}

// NewSQLite always fails in a binary built without SQLite support.
func NewSQLite(SQLiteOptions) (*SQLite, error) {
	return nil, errors.New("this gallery was built without SQLite support; build it with -tags sqlite to read a self-hosted bot's database (see docs/sqlite.md)")
}

// Records is never reached: NewSQLite fails first.
func (*SQLite) Records(context.Context) ([]Record, error) {
	return nil, errors.New("built without SQLite support")
}
