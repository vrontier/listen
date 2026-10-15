package catalogue

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
)

// ServerCap is a share of the host: for the Workbench's streams and
// analyses together, set by the master admin, or for one member's
// (members-and-streams.md, Resources and caps). Zero means no cap.
type ServerCap struct {
	CPUCores float64 `json:"cpu_cores"`
	MemoryMB int     `json:"memory_mb"`
}

// Set reports whether any part of the cap is set.
func (c ServerCap) Set() bool { return c.CPUCores > 0 || c.MemoryMB > 0 }

const (
	keyServerCap = "server_cap"
	keyMemberCap = "member_cap" // for every member without one of their own
)

func memberKey(userID int64) string { return keyMemberCap + ":" + strconv.FormatInt(userID, 10) }

func (d *DB) capAt(ctx context.Context, key string) (ServerCap, bool, error) {
	var c ServerCap
	var v string
	err := d.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return c, false, nil
	}
	if err != nil {
		return c, false, err
	}
	return c, true, json.Unmarshal([]byte(v), &c)
}

func (d *DB) setCapAt(ctx context.Context, by int64, key, label string, c *ServerCap) error {
	now := d.now()
	return d.tx(ctx, func(tx *sql.Tx) error {
		var old string
		err := tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&old)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if c == nil {
			if _, err := tx.ExecContext(ctx, `DELETE FROM settings WHERE key = ?`, key); err != nil {
				return err
			}
			return audit(ctx, tx, now, by, "setting", 0, label, old, "")
		}
		v, _ := json.Marshal(c)
		if _, err := tx.ExecContext(ctx, `INSERT INTO settings (key, value, updated, updated_by) VALUES (?, ?, ?, ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated = excluded.updated, updated_by = excluded.updated_by`,
			key, string(v), stamp(now), by); err != nil {
			return err
		}
		return audit(ctx, tx, now, by, "setting", 0, label, old, string(v))
	})
}

func (d *DB) ServerCap(ctx context.Context) (ServerCap, error) {
	c, _, err := d.capAt(ctx, keyServerCap)
	return c, err
}

func (d *DB) SetServerCap(ctx context.Context, by int64, c ServerCap) error {
	return d.setCapAt(ctx, by, keyServerCap, "server cap", &c)
}

// DefaultMemberCap is the cap of every member who has none of their own.
func (d *DB) DefaultMemberCap(ctx context.Context) (ServerCap, error) {
	c, _, err := d.capAt(ctx, keyMemberCap)
	return c, err
}

func (d *DB) SetDefaultMemberCap(ctx context.Context, by int64, c ServerCap) error {
	return d.setCapAt(ctx, by, keyMemberCap, "member cap", &c)
}

// MemberCap is the cap that applies to a member, and whether it is their
// own rather than the default.
func (d *DB) MemberCap(ctx context.Context, userID int64) (ServerCap, bool, error) {
	c, own, err := d.capAt(ctx, memberKey(userID))
	if err != nil || own {
		return c, own, err
	}
	c, err = d.DefaultMemberCap(ctx)
	return c, false, err
}

// SetMemberCap gives a member a cap of their own; nil returns them to the
// default.
func (d *DB) SetMemberCap(ctx context.Context, by, userID int64, c *ServerCap) error {
	return d.setCapAt(ctx, by, memberKey(userID), "member cap of "+strconv.FormatInt(userID, 10), c)
}
