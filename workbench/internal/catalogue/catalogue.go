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
	attested      TEXT NOT NULL,
	probe         TEXT NOT NULL DEFAULT '{}',
	created       TEXT NOT NULL,
	updated       TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS analyses (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	owner_id    INTEGER NOT NULL REFERENCES users(id),
	name        TEXT NOT NULL,
	description TEXT NOT NULL DEFAULT '',
	state       TEXT NOT NULL DEFAULT 'private',  -- private, published, withdrawn
	revision    INTEGER NOT NULL DEFAULT 1,
	created     TEXT NOT NULL,
	updated     TEXT NOT NULL,
	slug         TEXT UNIQUE,                   -- its public address, once published
	published    TEXT,
	withdrawn    TEXT,
	publish_data INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS analysis_sources (
	analysis_id INTEGER NOT NULL REFERENCES analyses(id) ON DELETE CASCADE,
	stream_id   INTEGER NOT NULL REFERENCES streams(id) ON DELETE CASCADE,
	position    INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (analysis_id, stream_id)
);
CREATE INDEX IF NOT EXISTS analysis_sources_stream ON analysis_sources(stream_id);
CREATE TABLE IF NOT EXISTS revisions (
	analysis_id INTEGER NOT NULL REFERENCES analyses(id) ON DELETE CASCADE,
	revision    INTEGER NOT NULL,
	spec        TEXT NOT NULL,
	created     TEXT NOT NULL,
	created_by  INTEGER NOT NULL REFERENCES users(id),
	PRIMARY KEY (analysis_id, revision)
);
CREATE TABLE IF NOT EXISTS runs (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	analysis_id INTEGER NOT NULL REFERENCES analyses(id) ON DELETE CASCADE,
	started     TEXT NOT NULL,
	started_by  INTEGER NOT NULL REFERENCES users(id),
	ends        TEXT,                        -- planned end; NULL: until stopped
	stopped     TEXT,                        -- NULL while it runs
	stopped_by  INTEGER REFERENCES users(id), -- NULL when it ended at its planned end
	revision    INTEGER NOT NULL,            -- the analysis's revision at the start
	dataset       TEXT NOT NULL DEFAULT 'recording', -- recording, freezing, ready, failed, deleted, none
	dataset_bytes INTEGER NOT NULL DEFAULT 0,
	dataset_note  TEXT NOT NULL DEFAULT '',
	summary       TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX IF NOT EXISTS runs_analysis ON runs(analysis_id);
CREATE TABLE IF NOT EXISTS usage_minutes (
	user_id     INTEGER NOT NULL,            -- 0: the whole engine
	minute      TEXT NOT NULL,
	cpu_percent REAL NOT NULL,               -- 100 is one core, averaged over the minute
	memory_mb   REAL NOT NULL,
	in_kbps     REAL NOT NULL,
	PRIMARY KEY (user_id, minute)
);
CREATE TABLE IF NOT EXISTS stream_shares (
	stream_id  INTEGER NOT NULL REFERENCES streams(id) ON DELETE CASCADE,
	user_id    INTEGER NOT NULL REFERENCES users(id),
	created    TEXT NOT NULL,
	created_by INTEGER NOT NULL REFERENCES users(id),
	publish_ok TEXT,                         -- when the owner agreed to the member publishing analyses of it
	PRIMARY KEY (stream_id, user_id)
);
CREATE TABLE IF NOT EXISTS reports (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	analysis_id INTEGER REFERENCES analyses(id) ON DELETE SET NULL,
	title       TEXT NOT NULL,               -- of the analysis when reported
	message     TEXT NOT NULL,
	contact     TEXT NOT NULL DEFAULT '',
	created     TEXT NOT NULL,
	closed      TEXT,
	closed_by   INTEGER REFERENCES users(id)
);
CREATE TABLE IF NOT EXISTS memory_sizes (
	analysis_id INTEGER NOT NULL REFERENCES analyses(id) ON DELETE CASCADE,
	stream_id   INTEGER NOT NULL REFERENCES streams(id) ON DELETE CASCADE,
	key         TEXT NOT NULL,               -- the engine's memory key
	bytes       INTEGER NOT NULL,
	updated     TEXT NOT NULL,               -- when the engine last reported it
	PRIMARY KEY (analysis_id, stream_id, key)
);
CREATE TABLE IF NOT EXISTS library_sets (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	owner_id    INTEGER NOT NULL REFERENCES users(id),
	name        TEXT NOT NULL,
	note        TEXT NOT NULL DEFAULT '',
	rules       TEXT NOT NULL,               -- JSON, as the engine takes them
	sample_rate INTEGER NOT NULL,
	taken       INTEGER NOT NULL DEFAULT 0,
	created     TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS api_tokens (
	id        INTEGER PRIMARY KEY AUTOINCREMENT,
	user_id   INTEGER NOT NULL REFERENCES users(id),
	name      TEXT NOT NULL,
	hash      TEXT NOT NULL UNIQUE,
	hint      TEXT NOT NULL,                 -- the first characters, shown to tell tokens apart
	scopes    TEXT NOT NULL,                 -- stream, mcp, comma-separated
	created   TEXT NOT NULL,
	last_used TEXT,
	revoked   TEXT
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
	// "Always on" went on 2026-10-15: sources connect only for runs and
	// previews.
	if cols["always_on"] {
		if _, err := db.Exec(`ALTER TABLE streams DROP COLUMN always_on`); err != nil {
			return err
		}
	}
	if err := migrateAnalyses(db); err != nil {
		return err
	}
	if err := migratePublishing(db); err != nil {
		return err
	}
	return migrateRuns(db)
}

// migratePublishing adds what publishing needs (2026-10-15) to tables made
// before.
func migratePublishing(db *sql.DB) error {
	has := func(table, col string) (bool, error) {
		var n int
		err := db.QueryRow(`SELECT count(*) FROM pragma_table_info(?) WHERE name = ?`, table, col).Scan(&n)
		return n > 0, err
	}
	for _, c := range []struct{ table, col, def string }{
		{"analyses", "slug", "TEXT"},
		{"analyses", "published", "TEXT"},
		{"analyses", "withdrawn", "TEXT"},
		{"analyses", "publish_data", "INTEGER NOT NULL DEFAULT 0"},
		{"stream_shares", "publish_ok", "TEXT"},
	} {
		ok, err := has(c.table, c.col)
		if err != nil {
			return err
		}
		if !ok {
			if _, err := db.Exec(`ALTER TABLE ` + c.table + ` ADD COLUMN ` + c.col + ` ` + c.def); err != nil {
				return err
			}
		}
	}
	// SQLite cannot add a UNIQUE column; the index does the same.
	_, err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS analyses_slug ON analyses(slug)`)
	return err
}

// migrateRuns adds the data set columns to runs made before 2026-10-15
// evening. Runs already ended then have no data set.
func migrateRuns(db *sql.DB) error {
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM pragma_table_info('runs') WHERE name = 'dataset'`).Scan(&n); err != nil || n > 0 {
		return err
	}
	for _, q := range []string{
		`ALTER TABLE runs ADD COLUMN dataset TEXT NOT NULL DEFAULT 'recording'`,
		`ALTER TABLE runs ADD COLUMN dataset_bytes INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE runs ADD COLUMN dataset_note TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE runs ADD COLUMN summary TEXT NOT NULL DEFAULT '{}'`,
		`UPDATE runs SET dataset = 'none', dataset_note = 'made before runs kept data sets' WHERE stopped IS NOT NULL OR (ends IS NOT NULL AND ends <= strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))`,
	} {
		if _, err := db.Exec(q); err != nil {
			return err
		}
	}
	return nil
}

// migrateAnalyses turns analyses made under one stream (until 2026-10-14)
// into analyses with sources: each becomes an analysis with that one
// source, keeping its identifier, so the engine finds its memories again.
// Its revisions become {"sources": {"<stream id>": <the old settings>}}, and
// one still called "First analysis" takes its source's name.
func migrateAnalyses(db *sql.DB) error {
	var old int
	if err := db.QueryRow(`SELECT count(*) FROM pragma_table_info('analyses') WHERE name = 'stream_id'`).Scan(&old); err != nil || old == 0 {
		return err
	}
	if _, err := db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		return err
	}
	defer db.Exec(`PRAGMA foreign_keys = ON`)
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{
		`INSERT OR IGNORE INTO analysis_sources (analysis_id, stream_id, position) SELECT id, stream_id, 0 FROM analyses`,
		// The analysis made with every stream was called "First analysis";
		// in a list of analyses it is named after its source instead.
		`UPDATE analyses SET name = (SELECT s.name FROM streams s WHERE s.id = analyses.stream_id) WHERE name = 'First analysis'`,
		`UPDATE revisions SET spec = json_object('sources', json_object(
			CAST((SELECT stream_id FROM analyses a WHERE a.id = revisions.analysis_id) AS TEXT), json(spec)))`,
		`CREATE TABLE analyses_new (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			owner_id    INTEGER NOT NULL REFERENCES users(id),
			name        TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			state       TEXT NOT NULL DEFAULT 'private',
			revision    INTEGER NOT NULL DEFAULT 1,
			created     TEXT NOT NULL,
			updated     TEXT NOT NULL)`,
		`INSERT INTO analyses_new (id, owner_id, name, state, revision, created, updated)
			SELECT id, owner_id, name, state, revision, created, updated FROM analyses`,
		`DROP TABLE analyses`,
		`ALTER TABLE analyses_new RENAME TO analyses`,
	} {
		if _, err := tx.Exec(q); err != nil {
			return fmt.Errorf("%w: %.60s", err, q)
		}
	}
	return tx.Commit()
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
