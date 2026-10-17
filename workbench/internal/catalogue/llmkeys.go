package catalogue

import (
	"context"
	"database/sql"
	"encoding/json"
	"strconv"
	"time"
)

// Language-model keys (members-and-streams.md, Language-model keys): one
// per provider and member, for the interpretation of the member's own
// analyses. The catalogue holds a key only sealed; sealing and opening is
// the caller's, with the server's sealing key.

// LLMKey is a member's key as the page shows it: never the key itself.
type LLMKey struct {
	UserID   int64
	Provider string
	Hint     string
	Created  time.Time
	Checked  time.Time // when the provider last accepted it
	Refused  time.Time // zero: not refused since it was last accepted
}

// LLMKeyPurpose binds a sealed key to its member and provider.
func LLMKeyPurpose(userID int64, provider string) string {
	return "llm-key:" + strconv.FormatInt(userID, 10) + ":" + provider
}

// SetLLMKey saves or replaces a member's key for a provider, which the
// provider has just accepted.
func (d *DB) SetLLMKey(ctx context.Context, userID int64, provider string, sealed []byte, hint string) error {
	now := d.now()
	return d.tx(ctx, func(tx *sql.Tx) error {
		var old string
		err := tx.QueryRowContext(ctx, `SELECT hint FROM llm_keys WHERE user_id = ? AND provider = ?`, userID, provider).Scan(&old)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO llm_keys (user_id, provider, sealed, hint, created, checked) VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT(user_id, provider) DO UPDATE SET sealed = excluded.sealed, hint = excluded.hint,
				created = excluded.created, checked = excluded.checked, refused = NULL`,
			userID, provider, sealed, hint, stamp(now), stamp(now)); err != nil {
			return err
		}
		var before string
		if old != "" {
			b, _ := json.Marshal(map[string]any{"provider": provider, "ends": old})
			before = string(b)
		}
		after, _ := json.Marshal(map[string]any{"provider": provider, "ends": hint})
		return audit(ctx, tx, now, userID, "user", userID, "llm key set", before, string(after))
	})
}

// LLMKeysOf lists a member's keys in no particular order.
func (d *DB) LLMKeysOf(ctx context.Context, userID int64) ([]LLMKey, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT user_id, provider, hint, created, checked, coalesce(refused, '') FROM llm_keys WHERE user_id = ?`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LLMKey
	for rows.Next() {
		var k LLMKey
		var created, checked, refused string
		if err := rows.Scan(&k.UserID, &k.Provider, &k.Hint, &created, &checked, &refused); err != nil {
			return nil, err
		}
		k.Created, k.Checked, k.Refused = parseStamp(created), parseStamp(checked), parseStamp(refused)
		out = append(out, k)
	}
	return out, rows.Err()
}

// SealedLLMKey returns a member's key for a provider, still sealed.
func (d *DB) SealedLLMKey(ctx context.Context, userID int64, provider string) ([]byte, error) {
	var sealed []byte
	err := d.db.QueryRowContext(ctx, `SELECT sealed FROM llm_keys WHERE user_id = ? AND provider = ?`, userID, provider).Scan(&sealed)
	return sealed, notFound(err)
}

// NoteLLMKeyCheck records the answer of a later check of a saved key.
func (d *DB) NoteLLMKeyCheck(ctx context.Context, userID int64, provider string, accepted bool) error {
	col := "checked"
	if !accepted {
		col = "refused"
	}
	q := `UPDATE llm_keys SET ` + col + ` = ?`
	if accepted {
		q += `, refused = NULL`
	}
	res, err := d.db.ExecContext(ctx, q+` WHERE user_id = ? AND provider = ?`, stamp(d.now()), userID, provider)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteLLMKey removes a member's key for a provider.
func (d *DB) DeleteLLMKey(ctx context.Context, userID int64, provider string) error {
	now := d.now()
	return d.tx(ctx, func(tx *sql.Tx) error {
		var hint string
		if err := tx.QueryRowContext(ctx, `SELECT hint FROM llm_keys WHERE user_id = ? AND provider = ?`, userID, provider).Scan(&hint); err != nil {
			return notFound(err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM llm_keys WHERE user_id = ? AND provider = ?`, userID, provider); err != nil {
			return err
		}
		before, _ := json.Marshal(map[string]any{"provider": provider, "ends": hint})
		return audit(ctx, tx, now, userID, "user", userID, "llm key delete", string(before), "")
	})
}
