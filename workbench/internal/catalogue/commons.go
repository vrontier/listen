package catalogue

import (
	"context"
	"database/sql"
	"encoding/json"
)

// Streams are a commons (decided 2026-10-18, members-and-streams.md,
// Sources as a commons): registered once, used by every member in their own
// analyses. Only a stream its steward keeps private for now is theirs alone.
// The table stream_shares of the earlier sharing by address stays in the
// schema, unused.

// OpenStreams lists the streams of other members that every member may use.
func (d *DB) OpenStreams(ctx context.Context, userID int64) ([]Stream, error) {
	return d.queryStreams(ctx, `WHERE s.owner_id != ? AND s.private = 0`, userID)
}

// MayUseStream reports whether a member may use a stream in their
// analyses: it is open, or they are its steward.
func (d *DB) MayUseStream(ctx context.Context, userID int64, st Stream) (bool, error) {
	return st.OwnerID == userID || !st.Private, nil
}

// SetStreamPrivate opens a stream to every member, or keeps it to its
// steward. The caller checks that nobody else's analysis uses it before
// making it private.
func (d *DB) SetStreamPrivate(ctx context.Context, id, by int64, private bool) error {
	now := d.now()
	return d.tx(ctx, func(tx *sql.Tx) error {
		var old bool
		if err := tx.QueryRowContext(ctx, `SELECT private FROM streams WHERE id = ?`, id).Scan(&old); err != nil {
			return notFound(err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE streams SET private = ?, updated = ? WHERE id = ?`, private, stamp(now), id); err != nil {
			return err
		}
		before, _ := json.Marshal(map[string]any{"private": old})
		after, _ := json.Marshal(map[string]any{"private": private})
		return audit(ctx, tx, now, by, "stream", id, "visibility", string(before), string(after))
	})
}
