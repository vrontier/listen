package catalogue

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

// The shared library (members-and-streams.md, Sharing): a member puts a
// set of filter rules into it with a name and a note; others take a copy
// into their own analyses. A copy, not a link, so that a later change in
// the library never changes someone's analysis.

// LibrarySet is one set of filter rules in the library.
type LibrarySet struct {
	ID         int64
	OwnerID    int64
	OwnerName  string
	Name       string
	Note       string
	Rules      json.RawMessage // as the engine takes them
	SampleRate int             // of the source it came from; tolerances in bins depend on it
	Taken      int             // how often a copy was taken
	Created    time.Time
}

// AddLibrarySet puts a set of rules into the library.
func (d *DB) AddLibrarySet(ctx context.Context, owner int64, name, note string, rules json.RawMessage, rate int) (int64, error) {
	now := d.now()
	var id int64
	err := d.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `INSERT INTO library_sets (owner_id, name, note, rules, sample_rate, created) VALUES (?, ?, ?, ?, ?, ?)`,
			owner, name, note, string(rules), rate, stamp(now))
		if err != nil {
			return err
		}
		id, _ = res.LastInsertId()
		after, _ := json.Marshal(map[string]any{"name": name, "note": note, "rules": rules, "sample_rate": rate})
		return audit(ctx, tx, now, owner, "library", id, "add", "", string(after))
	})
	return id, err
}

const librarySelect = `SELECT l.id, l.owner_id, u.name, l.name, l.note, l.rules, l.sample_rate, l.taken, l.created
	FROM library_sets l JOIN users u ON u.id = l.owner_id `

func scanLibrarySet(row interface{ Scan(...any) error }) (LibrarySet, error) {
	var l LibrarySet
	var rules, created string
	if err := row.Scan(&l.ID, &l.OwnerID, &l.OwnerName, &l.Name, &l.Note, &rules, &l.SampleRate, &l.Taken, &created); err != nil {
		return l, notFound(err)
	}
	l.Rules, l.Created = json.RawMessage(rules), parseStamp(created)
	return l, nil
}

// LibrarySets lists the library, newest first.
func (d *DB) LibrarySets(ctx context.Context) ([]LibrarySet, error) {
	rows, err := d.db.QueryContext(ctx, librarySelect+`ORDER BY l.id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LibrarySet
	for rows.Next() {
		l, err := scanLibrarySet(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// LibrarySet returns one set.
func (d *DB) LibrarySet(ctx context.Context, id int64) (LibrarySet, error) {
	return scanLibrarySet(d.db.QueryRowContext(ctx, librarySelect+`WHERE l.id = ?`, id))
}

// DeleteLibrarySet takes a set out of the library. Copies already taken
// stay in the analyses they were taken into.
func (d *DB) DeleteLibrarySet(ctx context.Context, id, by int64) error {
	now := d.now()
	return d.tx(ctx, func(tx *sql.Tx) error {
		var name, rules string
		if err := tx.QueryRowContext(ctx, `SELECT name, rules FROM library_sets WHERE id = ?`, id).Scan(&name, &rules); err != nil {
			return notFound(err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM library_sets WHERE id = ?`, id); err != nil {
			return err
		}
		before, _ := json.Marshal(map[string]any{"name": name, "rules": json.RawMessage(rules)})
		return audit(ctx, tx, now, by, "library", id, "delete", string(before), "")
	})
}

// NoteTaken counts a copy taken into an analysis.
func (d *DB) NoteTaken(ctx context.Context, id, by, analysisID int64) error {
	now := d.now()
	return d.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE library_sets SET taken = taken + 1 WHERE id = ?`, id); err != nil {
			return err
		}
		after, _ := json.Marshal(map[string]any{"analysis": analysisID})
		return audit(ctx, tx, now, by, "library", id, "take", "", string(after))
	})
}
