package catalogue

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// A member's own settings (members-and-streams.md, Account): the display
// name, changed at once, and the email address, which is the login and
// changes only once the new address has confirmed it.

// ErrEmailTaken is returned when another account, or an open invitation,
// has the address.
var ErrEmailTaken = errors.New("address in use")

// EmailChange is a change of address waiting for its confirmation.
type EmailChange struct {
	Email   string
	Created time.Time
	Expires time.Time
}

// migrateEmailChanges adds the pending changes of address (2026-10-16).
func migrateEmailChanges(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS email_changes (
	token_hash TEXT PRIMARY KEY,
	user_id    INTEGER NOT NULL REFERENCES users(id),
	email      TEXT NOT NULL COLLATE NOCASE,
	created    TEXT NOT NULL,
	expires    TEXT NOT NULL,
	used       TEXT
)`)
	return err
}

// SetUserName changes a member's display name.
func (d *DB) SetUserName(ctx context.Context, userID int64, name string) error {
	now := d.now()
	return d.tx(ctx, func(tx *sql.Tx) error {
		var old string
		if err := tx.QueryRowContext(ctx, `SELECT name FROM users WHERE id = ?`, userID).Scan(&old); err != nil {
			return notFound(err)
		}
		if old == name {
			return nil
		}
		if _, err := tx.ExecContext(ctx, `UPDATE users SET name = ? WHERE id = ?`, name, userID); err != nil {
			return err
		}
		return audit(ctx, tx, now, userID, "user", userID, "name", old, name)
	})
}

// emailFree reports whether no other account and no open invitation has
// the address.
func emailFree(ctx context.Context, tx *sql.Tx, userID int64, email string, now time.Time) (bool, error) {
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM users WHERE email = ? AND id != ?`, email, userID).Scan(&n); err != nil || n > 0 {
		return false, err
	}
	err := tx.QueryRowContext(ctx, `SELECT count(*) FROM invitations WHERE email = ? AND accepted IS NULL AND expires > ?`, email, stamp(now)).Scan(&n)
	return n == 0, err
}

// RequestEmailChange records a change of address and returns the secret
// for the link to the new address. It replaces an earlier request.
func (d *DB) RequestEmailChange(ctx context.Context, userID int64, email string, valid time.Duration) (string, error) {
	token, hash := NewToken()
	now := d.now()
	err := d.tx(ctx, func(tx *sql.Tx) error {
		free, err := emailFree(ctx, tx, userID, email, now)
		if err != nil {
			return err
		}
		if !free {
			return ErrEmailTaken
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM email_changes WHERE user_id = ? AND used IS NULL`, userID); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO email_changes (token_hash, user_id, email, created, expires) VALUES (?, ?, ?, ?, ?)`,
			hash, userID, email, stamp(now), stamp(now.Add(valid)))
		return err
	})
	return token, err
}

// EmailChangesSince counts a member's requests since a time, so that the
// form cannot be used to send mail to anyone at will.
func (d *DB) EmailChangesSince(ctx context.Context, userID int64, since time.Time) (int, error) {
	var n int
	err := d.db.QueryRowContext(ctx, `SELECT count(*) FROM audit WHERE user_id = ? AND object = 'user' AND action = 'email requested' AND at > ?`,
		userID, stamp(since)).Scan(&n)
	return n, err
}

// NoteEmailRequest notes a request in the audit trail; it counts against
// the limit even when the request is cancelled.
func (d *DB) NoteEmailRequest(ctx context.Context, userID int64, email string) error {
	now := d.now()
	return d.tx(ctx, func(tx *sql.Tx) error {
		return audit(ctx, tx, now, userID, "user", userID, "email requested", "", email)
	})
}

// PendingEmailChange returns the member's change of address waiting for
// confirmation, if any.
func (d *DB) PendingEmailChange(ctx context.Context, userID int64) (EmailChange, bool, error) {
	var c EmailChange
	var created, expires string
	err := d.db.QueryRowContext(ctx, `SELECT email, created, expires FROM email_changes WHERE user_id = ? AND used IS NULL AND expires > ? ORDER BY created DESC LIMIT 1`,
		userID, stamp(d.now())).Scan(&c.Email, &created, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return c, false, nil
	}
	c.Created, c.Expires = parseStamp(created), parseStamp(expires)
	return c, err == nil, err
}

// CancelEmailChange drops the member's waiting change of address.
func (d *DB) CancelEmailChange(ctx context.Context, userID int64) error {
	_, err := d.db.ExecContext(ctx, `DELETE FROM email_changes WHERE user_id = ? AND used IS NULL`, userID)
	return err
}

// OpenEmailChange returns the new address of a valid, unused link.
func (d *DB) OpenEmailChange(ctx context.Context, token string) (string, error) {
	var email, expires string
	err := d.db.QueryRowContext(ctx, `SELECT email, expires FROM email_changes WHERE token_hash = ? AND used IS NULL`, HashToken(token)).Scan(&email, &expires)
	if errors.Is(err, sql.ErrNoRows) || err == nil && d.now().After(parseStamp(expires)) {
		return "", ErrExpired
	}
	return email, err
}

// ConfirmEmailChange makes the new address the member's and ends all of
// the member's sessions; it returns the member as now and the old address.
func (d *DB) ConfirmEmailChange(ctx context.Context, token string) (User, string, error) {
	var userID int64
	var old string
	now := d.now()
	err := d.tx(ctx, func(tx *sql.Tx) error {
		var email, expires string
		err := tx.QueryRowContext(ctx, `SELECT user_id, email, expires FROM email_changes WHERE token_hash = ? AND used IS NULL`,
			HashToken(token)).Scan(&userID, &email, &expires)
		if errors.Is(err, sql.ErrNoRows) || err == nil && now.After(parseStamp(expires)) {
			return ErrExpired
		}
		if err != nil {
			return err
		}
		var suspended bool
		if err := tx.QueryRowContext(ctx, `SELECT email, suspended IS NOT NULL FROM users WHERE id = ?`, userID).Scan(&old, &suspended); err != nil {
			return err
		}
		if suspended {
			return ErrExpired
		}
		free, err := emailFree(ctx, tx, userID, email, now)
		if err != nil {
			return err
		}
		if !free {
			return ErrEmailTaken
		}
		if _, err := tx.ExecContext(ctx, `UPDATE users SET email = ? WHERE id = ?`, email, userID); err != nil {
			if strings.Contains(err.Error(), "UNIQUE") {
				return ErrEmailTaken
			}
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE email_changes SET used = ? WHERE token_hash = ?`, stamp(now), HashToken(token)); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, userID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM login_links WHERE user_id = ? AND used IS NULL`, userID); err != nil {
			return err
		}
		return audit(ctx, tx, now, userID, "user", userID, "email", old, email)
	})
	if err != nil {
		return User{}, "", err
	}
	u, err := d.User(ctx, userID)
	return u, old, err
}
