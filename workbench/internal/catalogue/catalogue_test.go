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
