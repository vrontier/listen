package catalogue

import (
	"context"
	"database/sql"
	"time"
)

// Usage is what was used: CPU in percent of one core, memory in MB,
// incoming bandwidth in kbit/s.
type Usage struct {
	CPUPercent float64
	MemoryMB   float64
	InKbps     float64
}

// UsageSample is a member's (or with user 0 the engine's) average use in
// one minute.
type UsageSample struct {
	Minute time.Time
	Usage
}

// AddUsage stores the averages of one minute, by member; key 0 is the
// engine as a whole.
func (d *DB) AddUsage(ctx context.Context, minute time.Time, byUser map[int64]Usage) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		for id, u := range byUser {
			if _, err := tx.ExecContext(ctx, `INSERT INTO usage_minutes (user_id, minute, cpu_percent, memory_mb, in_kbps) VALUES (?, ?, ?, ?, ?)
				ON CONFLICT(user_id, minute) DO UPDATE SET cpu_percent = excluded.cpu_percent, memory_mb = excluded.memory_mb, in_kbps = excluded.in_kbps`,
				id, stamp(minute.UTC().Truncate(time.Minute)), u.CPUPercent, u.MemoryMB, u.InKbps); err != nil {
				return err
			}
		}
		return nil
	})
}

// UsageSince returns a member's (or the engine's, with 0) samples since a
// time, oldest first. Minutes in which nothing ran have none.
func (d *DB) UsageSince(ctx context.Context, userID int64, since time.Time) ([]UsageSample, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT minute, cpu_percent, memory_mb, in_kbps FROM usage_minutes
		WHERE user_id = ? AND minute >= ? ORDER BY minute`, userID, stamp(since))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UsageSample
	for rows.Next() {
		var s UsageSample
		var m string
		if err := rows.Scan(&m, &s.CPUPercent, &s.MemoryMB, &s.InKbps); err != nil {
			return nil, err
		}
		s.Minute = parseStamp(m)
		out = append(out, s)
	}
	return out, rows.Err()
}

// SweepUsage removes samples older than a month and a day.
func (d *DB) SweepUsage(ctx context.Context) error {
	_, err := d.db.ExecContext(ctx, `DELETE FROM usage_minutes WHERE minute < ?`, stamp(d.now().Add(-32*24*time.Hour)))
	return err
}
