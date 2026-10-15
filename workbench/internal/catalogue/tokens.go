package catalogue

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// Tokens (members-and-streams.md, Tokens) let programs and AI assistants
// read a member's analyses. A token belongs to one member, is limited to
// the data stream, to MCP, or both, is shown once and stored only as a
// hash, and can be revoked.

// Token scopes.
const (
	ScopeStream = "stream"
	ScopeMCP    = "mcp"
)

// TokenPrefix marks a Workbench token, so that one pasted into a public
// place can be found by secret scanning.
const TokenPrefix = "low_"

// APIToken is a token as its member sees it later: never the secret.
type APIToken struct {
	ID       int64
	UserID   int64
	Name     string
	Hint     string // the first characters, to tell tokens apart
	Scopes   []string
	Created  time.Time
	LastUsed time.Time // zero: never
	Revoked  time.Time // zero: valid
}

// Has reports whether the token carries a scope.
func (t APIToken) Has(scope string) bool {
	for _, s := range t.Scopes {
		if s == scope {
			return true
		}
	}
	return false
}

// CreateAPIToken makes a token and returns its secret, which is shown once.
func (d *DB) CreateAPIToken(ctx context.Context, userID int64, name string, scopes []string) (string, APIToken, error) {
	raw, _ := NewToken()
	secret := TokenPrefix + raw
	now := d.now()
	t := APIToken{UserID: userID, Name: name, Hint: secret[:len(TokenPrefix)+4], Scopes: scopes, Created: now}
	err := d.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `INSERT INTO api_tokens (user_id, name, hash, hint, scopes, created) VALUES (?, ?, ?, ?, ?, ?)`,
			userID, name, HashToken(secret), t.Hint, strings.Join(scopes, ","), stamp(now))
		if err != nil {
			return err
		}
		t.ID, _ = res.LastInsertId()
		after, _ := json.Marshal(map[string]any{"token": t.ID, "name": name, "hint": t.Hint, "scopes": scopes})
		return audit(ctx, tx, now, userID, "user", userID, "token create", "", string(after))
	})
	return secret, t, err
}

const tokenSelect = `SELECT id, user_id, name, hint, scopes, created, coalesce(last_used, ''), coalesce(revoked, '') FROM api_tokens `

func scanAPIToken(row interface{ Scan(...any) error }) (APIToken, error) {
	var t APIToken
	var scopes, created, used, revoked string
	if err := row.Scan(&t.ID, &t.UserID, &t.Name, &t.Hint, &scopes, &created, &used, &revoked); err != nil {
		return t, notFound(err)
	}
	if scopes != "" {
		t.Scopes = strings.Split(scopes, ",")
	}
	t.Created, t.LastUsed, t.Revoked = parseStamp(created), parseStamp(used), parseStamp(revoked)
	return t, nil
}

// APITokensOf lists a member's tokens, the revoked ones too, newest first.
func (d *DB) APITokensOf(ctx context.Context, userID int64) ([]APIToken, error) {
	rows, err := d.db.QueryContext(ctx, tokenSelect+`WHERE user_id = ? ORDER BY id DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []APIToken
	for rows.Next() {
		t, err := scanAPIToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ErrRevoked is returned for a token that was revoked, or whose member is
// suspended.
var ErrRevoked = errors.New("token revoked")

// APIToken finds the token with a secret and its member, and notes its
// use, at most once a minute.
func (d *DB) APIToken(ctx context.Context, secret string) (APIToken, User, error) {
	t, err := scanAPIToken(d.db.QueryRowContext(ctx, tokenSelect+`WHERE hash = ?`, HashToken(secret)))
	if err != nil {
		return t, User{}, err
	}
	if !t.Revoked.IsZero() {
		return t, User{}, ErrRevoked
	}
	u, err := d.User(ctx, t.UserID)
	if err != nil {
		return t, u, err
	}
	if u.Suspended {
		return t, u, ErrRevoked
	}
	now := d.now()
	if now.Sub(t.LastUsed) > time.Minute {
		d.db.ExecContext(ctx, `UPDATE api_tokens SET last_used = ? WHERE id = ?`, stamp(now), t.ID)
		t.LastUsed = now
	}
	return t, u, nil
}

// RevokeAPIToken ends a member's token for good.
func (d *DB) RevokeAPIToken(ctx context.Context, userID, id int64) error {
	now := d.now()
	return d.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE api_tokens SET revoked = ? WHERE id = ? AND user_id = ? AND revoked IS NULL`, stamp(now), id, userID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		after, _ := json.Marshal(map[string]any{"token": id})
		return audit(ctx, tx, now, userID, "user", userID, "token revoke", "", string(after))
	})
}
