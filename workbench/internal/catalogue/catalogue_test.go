package catalogue

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

// A catalogue made before the coordinates existed gets their columns.
func TestMigrateAddsCoordinates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	old, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(`CREATE TABLE streams (id INTEGER PRIMARY KEY, name TEXT)`); err != nil {
		t.Fatal(err)
	}
	old.Exec(`INSERT INTO streams (name) VALUES ('x')`)
	old.Close()

	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.db.QueryRowContext(context.Background(), `SELECT count(*) FROM streams WHERE lat IS NULL AND lon IS NULL`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("coordinates columns: %v %d", err, n)
	}
	// Opening again changes nothing.
	db.Close()
	if db, err = Open(path); err != nil {
		t.Fatal(err)
	}
}

// Analyses made under one stream become analyses with that one source,
// keeping their identifiers, settings and revisions.
func TestMigrateAnalyses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	old, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE users (id INTEGER PRIMARY KEY AUTOINCREMENT, email TEXT NOT NULL UNIQUE, name TEXT NOT NULL, admin INTEGER NOT NULL DEFAULT 0, created TEXT NOT NULL, suspended TEXT)`,
		`CREATE TABLE streams (id INTEGER PRIMARY KEY AUTOINCREMENT, slug TEXT NOT NULL UNIQUE, owner_id INTEGER NOT NULL, input TEXT NOT NULL UNIQUE, sample_rate INTEGER NOT NULL,
			name TEXT NOT NULL, place TEXT NOT NULL DEFAULT '', timezone TEXT NOT NULL DEFAULT '', description TEXT NOT NULL DEFAULT '', credit TEXT NOT NULL DEFAULT '',
			credit_url TEXT NOT NULL DEFAULT '', audio_allowed INTEGER NOT NULL DEFAULT 0, state TEXT NOT NULL DEFAULT 'draft', always_on INTEGER NOT NULL DEFAULT 0,
			attested TEXT NOT NULL, probe TEXT NOT NULL DEFAULT '{}', created TEXT NOT NULL, updated TEXT NOT NULL)`,
		`CREATE TABLE analyses (id INTEGER PRIMARY KEY AUTOINCREMENT, stream_id INTEGER NOT NULL REFERENCES streams(id) ON DELETE CASCADE, owner_id INTEGER NOT NULL,
			name TEXT NOT NULL, state TEXT NOT NULL DEFAULT 'private', revision INTEGER NOT NULL DEFAULT 1, created TEXT NOT NULL, updated TEXT NOT NULL)`,
		`CREATE TABLE revisions (analysis_id INTEGER NOT NULL REFERENCES analyses(id) ON DELETE CASCADE, revision INTEGER NOT NULL, spec TEXT NOT NULL,
			created TEXT NOT NULL, created_by INTEGER NOT NULL, PRIMARY KEY (analysis_id, revision))`,
		`INSERT INTO users (email, name, created) VALUES ('m@example.org', 'M', '2026-10-14T00:00:00Z')`,
		`INSERT INTO streams (slug, owner_id, input, sample_rate, name, attested, created, updated) VALUES ('s', 1, 'https://s.example.org/a', 32000, 'S', 'x', 'x', 'x')`,
		`INSERT INTO streams (slug, owner_id, input, sample_rate, name, attested, created, updated) VALUES ('t', 1, 'https://s.example.org/b', 32000, 'T', 'x', 'x', 'x')`,
		`INSERT INTO analyses (id, stream_id, owner_id, name, revision, created, updated) VALUES (5, 2, 1, 'First analysis', 2, '2026-10-14T00:00:00Z', '2026-10-14T00:00:00Z')`,
		`INSERT INTO revisions VALUES (5, 1, '{"memory":{"mode":"disk"}}', '2026-10-14T00:00:00Z', 1)`,
		`INSERT INTO revisions VALUES (5, 2, '{"rules":[{"kind":"comb","hz":50}]}', '2026-10-14T00:00:00Z', 1)`,
	} {
		if _, err := old.Exec(q); err != nil {
			t.Fatalf("%v: %s", err, q)
		}
	}
	old.Close()

	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	a, err := db.Analysis(context.Background(), 5)
	if err != nil {
		t.Fatal(err)
	}
	if a.EngineID() != "a-0005" || a.Name != "T" || a.Revision != 2 || len(a.Sources) != 1 || a.Sources[0].Stream.ID != 2 || string(a.Sources[0].Spec) != `{"rules":[{"kind":"comb","hz":50}]}` {
		t.Fatalf("migrated: %+v", a)
	}
	revs, _ := db.Revisions(context.Background(), 5)
	if len(revs) != 2 || string(revs[1].Spec) != `{"sources":{"2":{"memory":{"mode":"disk"}}}}` {
		t.Fatalf("revisions: %+v", revs)
	}
	// A new analysis gets a fresh identifier.
	b, err := db.CreateAnalysis(context.Background(), 1, "y", "", []int64{1}, nil)
	if err != nil || b.ID <= 5 {
		t.Fatalf("new analysis after migration: %v %d", err, b.ID)
	}
}
