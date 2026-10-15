package catalogue

import (
	"context"
	"database/sql"
	"time"
)

// AuditEntry is one recorded creation, change or deletion.
type AuditEntry struct {
	ID       int64
	At       time.Time
	UserName string
	Object   string
	ObjectID int64
	Action   string
	Before   string
	After    string
}

func audit(ctx context.Context, tx *sql.Tx, at time.Time, userID int64, object string, objectID int64, action, before, after string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO audit (at, user_id, object, object_id, action, before, after) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		stamp(at), userID, object, objectID, action, before, after)
	return err
}

func (d *DB) queryAudit(ctx context.Context, where string, args ...any) ([]AuditEntry, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT a.id, a.at, coalesce(u.name, ''), a.object, a.object_id, a.action, a.before, a.after
		FROM audit a LEFT JOIN users u ON u.id = a.user_id `+where+` ORDER BY a.id DESC LIMIT 200`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditEntry
	for rows.Next() {
		var e AuditEntry
		var at string
		if err := rows.Scan(&e.ID, &at, &e.UserName, &e.Object, &e.ObjectID, &e.Action, &e.Before, &e.After); err != nil {
			return nil, err
		}
		e.At = parseStamp(at)
		out = append(out, e)
	}
	return out, rows.Err()
}

// AuditOf lists the entries about one object, newest first.
func (d *DB) AuditOf(ctx context.Context, object string, id int64) ([]AuditEntry, error) {
	return d.queryAudit(ctx, `WHERE a.object = ? AND a.object_id = ?`, object, id)
}

// AuditAll lists the latest entries, for the master admin.
func (d *DB) AuditAll(ctx context.Context) ([]AuditEntry, error) {
	return d.queryAudit(ctx, ``)
}

// Note records an entry that goes with no other change, such as a trial
// started or ended.
func (d *DB) Note(ctx context.Context, by int64, object string, objectID int64, action, after string) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		return audit(ctx, tx, d.now(), by, object, objectID, action, "", after)
	})
}
