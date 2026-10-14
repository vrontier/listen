// Package catalogue is the Workbench's own database: members, invitations,
// sessions, streams, analyses with their revisions, and the audit trail.
// It belongs to the Workbench process alone; the engine never reads it.
package catalogue

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// ErrNotFound is returned when a looked-up row does not exist.
var ErrNotFound = errors.New("not found")

// DB wraps the SQLite database.
type DB struct {
	db  *sql.DB
	now func() time.Time
}

const schema = `
CREATE TABLE IF NOT EXISTS users (
	id        INTEGER PRIMARY KEY AUTOINCREMENT,
	email     TEXT NOT NULL UNIQUE COLLATE NOCASE,
	name      TEXT NOT NULL,
	admin     INTEGER NOT NULL DEFAULT 0,
	created   TEXT NOT NULL,
	suspended TEXT
);
CREATE TABLE IF NOT EXISTS invitations (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	email      TEXT NOT NULL COLLATE NOCASE,
	token_hash TEXT NOT NULL UNIQUE,
	invited_by INTEGER REFERENCES users(id),
	created    TEXT NOT NULL,
	expires    TEXT NOT NULL,
	accepted   TEXT
);
CREATE TABLE IF NOT EXISTS login_links (
	token_hash TEXT PRIMARY KEY,
	user_id    INTEGER NOT NULL REFERENCES users(id),
	created    TEXT NOT NULL,
	expires    TEXT NOT NULL,
	used       TEXT
);
CREATE TABLE IF NOT EXISTS sessions (
	id_hash TEXT PRIMARY KEY,
	user_id INTEGER NOT NULL REFERENCES users(id),
	csrf    TEXT NOT NULL,
	created TEXT NOT NULL,
	expires TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS streams (
	id            INTEGER PRIMARY KEY AUTOINCREMENT,
	slug          TEXT NOT NULL UNIQUE,
	owner_id      INTEGER NOT NULL REFERENCES users(id),
	input         TEXT NOT NULL UNIQUE,
	sample_rate   INTEGER NOT NULL,
	name          TEXT NOT NULL,
	place         TEXT NOT NULL DEFAULT '',
	timezone      TEXT NOT NULL DEFAULT '',
	lat           REAL,
	lon           REAL,
	description   TEXT NOT NULL DEFAULT '',
	credit        TEXT NOT NULL DEFAULT '',
	credit_url    TEXT NOT NULL DEFAULT '',
	audio_allowed INTEGER NOT NULL DEFAULT 0,
	state         TEXT NOT NULL DEFAULT 'draft',
	always_on     INTEGER NOT NULL DEFAULT 0,
	attested      TEXT NOT NULL,
	probe         TEXT NOT NULL DEFAULT '{}',
	created       TEXT NOT NULL,
	updated       TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS analyses (
	id        INTEGER PRIMARY KEY AUTOINCREMENT,
	stream_id INTEGER NOT NULL REFERENCES streams(id) ON DELETE CASCADE,
	owner_id  INTEGER NOT NULL REFERENCES users(id),
	name      TEXT NOT NULL,
	state     TEXT NOT NULL DEFAULT 'private',
	revision  INTEGER NOT NULL DEFAULT 1,
	created   TEXT NOT NULL,
	updated   TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS revisions (
	analysis_id INTEGER NOT NULL REFERENCES analyses(id) ON DELETE CASCADE,
	revision    INTEGER NOT NULL,
	spec        TEXT NOT NULL,
	created     TEXT NOT NULL,
	created_by  INTEGER NOT NULL REFERENCES users(id),
	PRIMARY KEY (analysis_id, revision)
);
CREATE TABLE IF NOT EXISTS audit (
	id        INTEGER PRIMARY KEY AUTOINCREMENT,
	at        TEXT NOT NULL,
	user_id   INTEGER,
	object    TEXT NOT NULL,
	object_id INTEGER NOT NULL,
	action    TEXT NOT NULL,
	before    TEXT NOT NULL DEFAULT '',
	after     TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS audit_object ON audit(object, object_id);
CREATE TABLE IF NOT EXISTS settings (
	key        TEXT PRIMARY KEY,
	value      TEXT NOT NULL,
	updated    TEXT NOT NULL,
	updated_by INTEGER REFERENCES users(id)
);
`

// Open opens (or creates) the catalogue at path. ":memory:" gives a
// throwaway database for tests.
func Open(path string) (*DB, error) {
	dsn := "file:" + path + "?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	if path == ":memory:" {
		dsn = "file::memory:?_pragma=foreign_keys(1)"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// One writer at a time; SQLite serialises anyway, and an in-memory
	// database exists only on its own connection.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("catalogue schema: %w", err)
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("catalogue migration: %w", err)
	}
	return &DB{db: db, now: time.Now}, nil
}

func (d *DB) Close() error { return d.db.Close() }

// migrate adds what later versions added to tables made by earlier ones.
func migrate(db *sql.DB) error {
	cols := map[string]bool{}
	rows, err := db.Query(`SELECT name FROM pragma_table_info('streams')`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var n string
		rows.Scan(&n)
		cols[n] = true
	}
	rows.Close()
	for _, c := range []string{"lat", "lon"} {
		if !cols[c] {
			if _, err := db.Exec(`ALTER TABLE streams ADD COLUMN ` + c + ` REAL`); err != nil {
				return err
			}
		}
	}
	return nil
}

// stamp formats t the way every time column is stored.
func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func parseStamp(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}

func (d *DB) tx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}
