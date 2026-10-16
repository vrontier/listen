package catalogue

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"
)

// Control by token (members-and-streams.md, Tokens): a member may let a
// token define and change analyses and start and stop previews and runs,
// through MCP. Each token has its own limits; a limit of 0 is none.

// ScopeControl lets a token change things; it goes with ScopeMCP.
const ScopeControl = "control"

// TokenLimits bound what a token with control may do. Zero means no limit.
type TokenLimits struct {
	RunHours       int `json:"run_hours"`        // the longest run it may start; 0: also runs until stopped
	RunsPerDay     int `json:"runs_per_day"`     // runs started in the last 24 hours
	RunsAtOnce     int `json:"runs_at_once"`     // its runs going on at the same time
	PreviewsPerDay int `json:"previews_per_day"` // previews started in the last 24 hours
	ChangesPerDay  int `json:"changes_per_day"`  // analyses made or changed in the last 24 hours
}

// DefaultTokenLimits are offered when a member makes a token with control.
var DefaultTokenLimits = TokenLimits{RunHours: 24, RunsPerDay: 5, RunsAtOnce: 1, PreviewsPerDay: 20, ChangesPerDay: 50}

// Unlimited reports whether no limit is set.
func (l TokenLimits) Unlimited() bool { return l == TokenLimits{} }

func parseLimits(s string) TokenLimits {
	var l TokenLimits
	json.Unmarshal([]byte(s), &l)
	return l
}

func (l TokenLimits) json() string {
	b, _ := json.Marshal(l)
	return string(b)
}

// migrateControl adds the token limits and the token that started a run
// (2026-10-16).
func migrateControl(db *sql.DB) error {
	for _, c := range []struct{ table, col, def string }{
		{"api_tokens", "limits", "TEXT NOT NULL DEFAULT '{}'"},
		{"runs", "token_id", "INTEGER"},
	} {
		var n int
		if err := db.QueryRow(`SELECT count(*) FROM pragma_table_info(?) WHERE name = ?`, c.table, c.col).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			if _, err := db.Exec(`ALTER TABLE ` + c.table + ` ADD COLUMN ` + c.col + ` ` + c.def); err != nil {
				return err
			}
		}
	}
	return nil
}

// UpdateAPIToken changes what a member's valid token may do and its limits.
func (d *DB) UpdateAPIToken(ctx context.Context, userID, id int64, scopes []string, limits TokenLimits) error {
	now := d.now()
	return d.tx(ctx, func(tx *sql.Tx) error {
		var oldScopes, oldLimits string
		err := tx.QueryRowContext(ctx, `SELECT scopes, limits FROM api_tokens WHERE id = ? AND user_id = ? AND revoked IS NULL`, id, userID).Scan(&oldScopes, &oldLimits)
		if err != nil {
			return notFound(err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE api_tokens SET scopes = ?, limits = ? WHERE id = ?`, strings.Join(scopes, ","), limits.json(), id); err != nil {
			return err
		}
		before, _ := json.Marshal(map[string]any{"token": id, "scopes": strings.Split(oldScopes, ","), "limits": json.RawMessage(oldLimits)})
		after, _ := json.Marshal(map[string]any{"token": id, "scopes": scopes, "limits": limits})
		return audit(ctx, tx, now, userID, "user", userID, "token change", string(before), string(after))
	})
}

// StartRunByToken starts a run as StartRunWith does, noting the token
// that started it.
func (d *DB) StartRunByToken(ctx context.Context, analysisID, by, tokenID int64, ends time.Time, offlineStop int) (Run, error) {
	run, err := d.StartRunWith(ctx, analysisID, by, ends, offlineStop)
	if err != nil {
		return run, err
	}
	if _, err := d.db.ExecContext(ctx, `UPDATE runs SET token_id = ? WHERE id = ?`, tokenID, run.ID); err != nil {
		return run, err
	}
	return d.Run(ctx, run.ID)
}

// TokenRuns counts the runs a token started since a time, and its runs
// going on now.
func (d *DB) TokenRuns(ctx context.Context, tokenID int64, since time.Time) (started, active int, err error) {
	if err = d.db.QueryRowContext(ctx, `SELECT count(*) FROM runs WHERE token_id = ? AND started >= ?`, tokenID, stamp(since)).Scan(&started); err != nil {
		return
	}
	err = d.db.QueryRowContext(ctx, `SELECT count(*) FROM runs r WHERE r.token_id = ? AND `+activeWhere, tokenID, stamp(d.now())).Scan(&active)
	return
}

// TokenCalls counts a token's successful MCP calls of the given tools
// since a time, from the notes of access.
func (d *DB) TokenCalls(ctx context.Context, tokenID int64, tools []string, since time.Time) (int, error) {
	if len(tools) == 0 {
		return 0, nil
	}
	q := `SELECT count(*) FROM token_access WHERE token_id = ? AND via = 'mcp' AND status = 'ok' AND at >= ? AND what IN (?` + strings.Repeat(", ?", len(tools)-1) + `)`
	args := []any{tokenID, since.UTC().Format(time.RFC3339Nano)}
	for _, t := range tools {
		args = append(args, t)
	}
	var n int
	err := d.db.QueryRowContext(ctx, q, args...).Scan(&n)
	return n, err
}

// AuditByToken records in the audit trail that a change was made through
// a token, next to the change's own entry.
func (d *DB) AuditByToken(ctx context.Context, userID int64, object string, objectID int64, tool string, tokenID int64, tokenName string, detail any) error {
	now := d.now()
	after, _ := json.Marshal(map[string]any{"token": tokenID, "token_name": tokenName, "tool": tool, "detail": detail})
	return d.tx(ctx, func(tx *sql.Tx) error {
		return audit(ctx, tx, now, userID, object, objectID, "by token: "+tool, "", string(after))
	})
}
