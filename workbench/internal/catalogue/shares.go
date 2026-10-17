package catalogue

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// A stream's owner may share it with other members (members-and-streams.md,
// Sharing): they may then use it in their own analyses and see its page,
// but not change it.

// ErrAlreadyShared is returned when a stream is shared with a member twice.
var ErrAlreadyShared = errors.New("already shared with this member")

// ShareStream lets a member use a stream.
func (d *DB) ShareStream(ctx context.Context, streamID, userID, by int64) error {
	now := d.now()
	return d.tx(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM stream_shares WHERE stream_id = ? AND user_id = ?`, streamID, userID).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return ErrAlreadyShared
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO stream_shares (stream_id, user_id, created, created_by) VALUES (?, ?, ?, ?)`,
			streamID, userID, stamp(now), by); err != nil {
			return err
		}
		after, _ := json.Marshal(map[string]any{"member": userID})
		return audit(ctx, tx, now, by, "stream", streamID, "share", "", string(after))
	})
}

// UnshareStream ends a member's use of a stream. Their analyses keep it as
// a source, but cannot start with it.
func (d *DB) UnshareStream(ctx context.Context, streamID, userID, by int64) error {
	now := d.now()
	return d.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM stream_shares WHERE stream_id = ? AND user_id = ?`, streamID, userID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		before, _ := json.Marshal(map[string]any{"member": userID})
		return audit(ctx, tx, now, by, "stream", streamID, "unshare", string(before), "")
	})
}

// StreamShares lists the members a stream is shared with.
func (d *DB) StreamShares(ctx context.Context, streamID int64) ([]User, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT u.id, u.email, u.name, u.admin, u.created, u.suspended IS NOT NULL FROM stream_shares x JOIN users u ON u.id = x.user_id
		WHERE x.stream_id = ? ORDER BY u.name`, streamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// SharedWith lists the streams others have shared with a member.
func (d *DB) SharedWith(ctx context.Context, userID int64) ([]Stream, error) {
	return d.queryStreams(ctx, `WHERE s.id IN (SELECT stream_id FROM stream_shares WHERE user_id = ?)`, userID)
}

// MayUseStream reports whether a member owns a stream or has it shared.
func (d *DB) MayUseStream(ctx context.Context, userID int64, st Stream) (bool, error) {
	if st.OwnerID == userID {
		return true, nil
	}
	var n int
	err := d.db.QueryRowContext(ctx, `SELECT count(*) FROM stream_shares WHERE stream_id = ? AND user_id = ?`, st.ID, userID).Scan(&n)
	return n > 0, err
}
