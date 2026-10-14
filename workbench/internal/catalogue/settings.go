package catalogue

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// ServerCap is the share of the host the Workbench's streams and analyses
// may use together, set by the master admin (members-and-streams.md,
// Resources and caps). Zero means no cap is set.
type ServerCap struct {
	CPUCores float64 `json:"cpu_cores"`
	MemoryMB int     `json:"memory_mb"`
}

const keyServerCap = "server_cap"

func (d *DB) ServerCap(ctx context.Context) (ServerCap, error) {
	var c ServerCap
	var v string
	err := d.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, keyServerCap).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	return c, json.Unmarshal([]byte(v), &c)
}

func (d *DB) SetServerCap(ctx context.Context, by int64, c ServerCap) error {
	now := d.now()
	return d.tx(ctx, func(tx *sql.Tx) error {
		var old string
		err := tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, keyServerCap).Scan(&old)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		v, _ := json.Marshal(c)
		if _, err := tx.ExecContext(ctx, `INSERT INTO settings (key, value, updated, updated_by) VALUES (?, ?, ?, ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated = excluded.updated, updated_by = excluded.updated_by`,
			keyServerCap, string(v), stamp(now), by); err != nil {
			return err
		}
		return audit(ctx, tx, now, by, "setting", 0, "server cap", old, string(v))
	})
}
