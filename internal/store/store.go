// Package store persists gateway state in SQLite (spec §15).
package store

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schema string

var (
	ErrNotFound = errors.New("not found")
	ErrExists   = errors.New("already exists")
	ErrInUse    = errors.New("in use")
)

type Store struct {
	db  *sql.DB
	now func() time.Time
}

// Open opens (and migrates) the database. A single connection serialises
// all access, which is plenty for this workload and avoids SQLITE_BUSY.
func Open(path string) (*Store, error) {
	dsn := "file:" + path + "?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: apply schema: %w", err)
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: migrate: %w", err)
	}
	return &Store{db: db, now: time.Now}, nil
}

// migrate adds columns introduced after the first release to existing
// databases (CREATE TABLE IF NOT EXISTS leaves old tables untouched).
func migrate(db *sql.DB) error {
	rows, err := db.Query(`PRAGMA table_info(apps)`)
	if err != nil {
		return err
	}
	have := map[string]bool{}
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			rows.Close()
			return err
		}
		have[name] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, c := range []struct{ name, def string }{
		{"secret_apply_state", "TEXT NOT NULL DEFAULT ''"},
		{"secret_apply_at", "INTEGER"},
		{"secret_apply_error", "TEXT NOT NULL DEFAULT ''"},
	} {
		if have[c.name] {
			continue
		}
		if _, err := db.Exec(`ALTER TABLE apps ADD COLUMN ` + c.name + ` ` + c.def); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) Close() error { return s.db.Close() }

// withTx runs fn in a transaction. fn must only use tx (never s.db), because
// the pool has a single connection.
func (s *Store) withTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

func isUnique(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func timePtr(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := time.Unix(v.Int64, 0)
	return &t
}

type scanner interface{ Scan(dest ...any) error }
