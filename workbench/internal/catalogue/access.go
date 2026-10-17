package catalogue

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"
)

// Every access with a member's token, through MCP or the data stream, is
// noted, so that the member sees what their programs and assistants read
// (members-and-streams.md, Tokens). The notes are kept for AccessKeep.

// AccessKeep is how long notes of access are kept.
const AccessKeep = 30 * 24 * time.Hour

// Access states.
const (
	AccessOpen    = "open" // a data stream still being sent
	AccessOK      = "ok"
	AccessError   = "error"   // answered with an error
	AccessRefused = "refused" // the token may not do this
	AccessCut     = "cut"     // open when the Workbench stopped
)

// Access is one note.
type Access struct {
	ID        int64
	UserID    int64
	TokenID   int64
	TokenName string
	TokenHint string
	At        time.Time
	Via       string // mcp or stream
	What      string
	Detail    string
	Client    string
	Status    string
	Note      string
	Ms        int64
	Bytes     int64
}

// Clip shortens a text for a note to n bytes at most, on a rune boundary.
func Clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return strings.TrimSpace(s[:n]) + "…"
}

// LogAccess notes an access and returns its id.
func (d *DB) LogAccess(ctx context.Context, a Access) (int64, error) {
	if a.At.IsZero() {
		a.At = d.now()
	}
	res, err := d.db.ExecContext(ctx, `INSERT INTO token_access (user_id, token_id, at, via, what, detail, client, status, note, ms, bytes)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.UserID, a.TokenID, a.At.UTC().Format(time.RFC3339Nano), a.Via, Clip(a.What, 200), Clip(a.Detail, 1000), Clip(a.Client, 200),
		a.Status, Clip(a.Note, 500), a.Ms, a.Bytes)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// FinishAccess notes how an access that was open ended.
func (d *DB) FinishAccess(ctx context.Context, id int64, status, note string, ms, bytes int64) error {
	_, err := d.db.ExecContext(ctx, `UPDATE token_access SET status = ?, note = ?, ms = ?, bytes = ? WHERE id = ?`,
		status, Clip(note, 500), ms, bytes, id)
	return err
}

// CutOpenAccess marks the accesses still open as cut: the Workbench
// stopped while they were sent.
func (d *DB) CutOpenAccess(ctx context.Context) error {
	_, err := d.db.ExecContext(ctx, `UPDATE token_access SET status = ? WHERE status = ?`, AccessCut, AccessOpen)
	return err
}

// AccessFilter narrows a member's notes.
type AccessFilter struct {
	TokenID int64  // 0: all tokens
	Via     string // "": both
	Failed  bool   // only errors, refusals and cut streams
	Before  int64  // notes older than this id; 0: the newest
	Limit   int
}

// AccessOf lists a member's notes, newest first.
func (d *DB) AccessOf(ctx context.Context, userID int64, f AccessFilter) ([]Access, error) {
	q := `SELECT a.id, a.user_id, a.token_id, t.name, t.hint, a.at, a.via, a.what, a.detail, a.client, a.status, a.note, a.ms, a.bytes
		FROM token_access a JOIN api_tokens t ON t.id = a.token_id WHERE a.user_id = ?`
	args := []any{userID}
	if f.TokenID > 0 {
		q += ` AND a.token_id = ?`
		args = append(args, f.TokenID)
	}
	if f.Via != "" {
		q += ` AND a.via = ?`
		args = append(args, f.Via)
	}
	if f.Failed {
		q += ` AND a.status IN ('error', 'refused', 'cut')`
	}
	if f.Before > 0 {
		q += ` AND a.id < ?`
		args = append(args, f.Before)
	}
	if f.Limit <= 0 {
		f.Limit = 100
	}
	q += ` ORDER BY a.id DESC LIMIT ?`
	args = append(args, f.Limit)
	rows, err := d.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Access
	for rows.Next() {
		var a Access
		var at string
		if err := rows.Scan(&a.ID, &a.UserID, &a.TokenID, &a.TokenName, &a.TokenHint, &at, &a.Via, &a.What, &a.Detail, &a.Client,
			&a.Status, &a.Note, &a.Ms, &a.Bytes); err != nil {
			return nil, err
		}
		a.At, _ = time.Parse(time.RFC3339Nano, at)
		out = append(out, a)
	}
	return out, rows.Err()
}

// TokenActivity sums up the access with one token.
type TokenActivity struct {
	TokenID  int64
	Name     string
	Hint     string
	Revoked  bool
	Day      int // accesses in the last 24 hours
	DayFails int
	Month    int // in the last 30 days
	Open     int // data streams being sent now
	Last     time.Time
}

// ActivityOf sums up a member's access per token, for every token that was
// used in the last 30 days.
func (d *DB) ActivityOf(ctx context.Context, userID int64) ([]TokenActivity, error) {
	now := d.now()
	day := now.Add(-24 * time.Hour).UTC().Format(time.RFC3339Nano)
	month := now.Add(-AccessKeep).UTC().Format(time.RFC3339Nano)
	rows, err := d.db.QueryContext(ctx, `SELECT t.id, t.name, t.hint, t.revoked IS NOT NULL,
		sum(a.at >= ?), sum(a.at >= ? AND a.status IN ('error', 'refused', 'cut')), count(*), sum(a.status = 'open'), max(a.at)
		FROM token_access a JOIN api_tokens t ON t.id = a.token_id
		WHERE a.user_id = ? AND a.at >= ? GROUP BY t.id ORDER BY max(a.at) DESC`, day, day, userID, month)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TokenActivity
	for rows.Next() {
		var t TokenActivity
		var last string
		if err := rows.Scan(&t.TokenID, &t.Name, &t.Hint, &t.Revoked, &t.Day, &t.DayFails, &t.Month, &t.Open, &last); err != nil {
			return nil, err
		}
		t.Last, _ = time.Parse(time.RFC3339Nano, last)
		out = append(out, t)
	}
	return out, rows.Err()
}

// SweepAccess removes notes older than AccessKeep.
func (d *DB) SweepAccess(ctx context.Context) error {
	_, err := d.db.ExecContext(ctx, `DELETE FROM token_access WHERE at < ? AND status != 'open'`, d.now().Add(-AccessKeep).UTC().Format(time.RFC3339Nano))
	return err
}
