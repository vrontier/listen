package catalogue

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"time"
)

// User is a member account.
type User struct {
	ID        int64
	Email     string
	Name      string
	Admin     bool
	Created   time.Time
	Suspended bool
}

// Invitation is an open or accepted invitation to become a member.
type Invitation struct {
	ID       int64
	Email    string
	Created  time.Time
	Expires  time.Time
	Accepted bool
}

// Session is a logged-in browser.
type Session struct {
	User User
	CSRF string
}

// NewToken returns a random secret for a link or a cookie, and the hash
// under which it is stored. Only the hash is ever written to the database.
func NewToken() (token, hash string) {
	b := make([]byte, 32)
	rand.Read(b)
	token = base64.RawURLEncoding.EncodeToString(b)
	return token, HashToken(token)
}

// HashToken is the stored form of a token.
func HashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

var ErrExpired = errors.New("expired or already used")

const userCols = `id, email, name, admin, created, suspended IS NOT NULL`

func scanUser(row interface{ Scan(...any) error }) (User, error) {
	var u User
	var created string
	err := row.Scan(&u.ID, &u.Email, &u.Name, &u.Admin, &created, &u.Suspended)
	u.Created = parseStamp(created)
	return u, err
}

// CreateUser adds a member. It is used for the first master admin; everyone
// else arrives through an invitation.
func (d *DB) CreateUser(ctx context.Context, email, name string, admin bool) (User, error) {
	res, err := d.db.ExecContext(ctx, `INSERT INTO users (email, name, admin, created) VALUES (?, ?, ?, ?)`,
		email, name, admin, stamp(d.now()))
	if err != nil {
		return User{}, err
	}
	id, _ := res.LastInsertId()
	return d.User(ctx, id)
}

func (d *DB) User(ctx context.Context, id int64) (User, error) {
	u, err := scanUser(d.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id = ?`, id))
	return u, notFound(err)
}

func (d *DB) UserByEmail(ctx context.Context, email string) (User, error) {
	u, err := scanUser(d.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE email = ?`, email))
	return u, notFound(err)
}

func (d *DB) Users(ctx context.Context) ([]User, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT `+userCols+` FROM users ORDER BY id`)
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

// Invite records an invitation and returns the secret for its link.
func (d *DB) Invite(ctx context.Context, email string, by int64, valid time.Duration) (string, error) {
	token, hash := NewToken()
	now := d.now()
	err := d.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `INSERT INTO invitations (email, token_hash, invited_by, created, expires) VALUES (?, ?, ?, ?, ?)`,
			email, hash, by, stamp(now), stamp(now.Add(valid)))
		if err != nil {
			return err
		}
		id, _ := res.LastInsertId()
		return audit(ctx, tx, now, by, "invitation", id, "create", "", email)
	})
	return token, err
}

func (d *DB) Invitations(ctx context.Context) ([]Invitation, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT id, email, created, expires, accepted IS NOT NULL FROM invitations ORDER BY id DESC LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Invitation
	for rows.Next() {
		var i Invitation
		var c, e string
		if err := rows.Scan(&i.ID, &i.Email, &c, &e, &i.Accepted); err != nil {
			return nil, err
		}
		i.Created, i.Expires = parseStamp(c), parseStamp(e)
		out = append(out, i)
	}
	return out, rows.Err()
}

// OpenInvitation returns the email address of a valid, unused invitation.
func (d *DB) OpenInvitation(ctx context.Context, token string) (string, error) {
	var email, expires string
	err := d.db.QueryRowContext(ctx, `SELECT email, expires FROM invitations WHERE token_hash = ? AND accepted IS NULL`,
		HashToken(token)).Scan(&email, &expires)
	if errors.Is(err, sql.ErrNoRows) || err == nil && d.now().After(parseStamp(expires)) {
		return "", ErrExpired
	}
	return email, err
}

// AcceptInvitation turns an invitation into an account.
func (d *DB) AcceptInvitation(ctx context.Context, token, name string) (User, error) {
	var id int64
	now := d.now()
	err := d.tx(ctx, func(tx *sql.Tx) error {
		var invID int64
		var email, expires string
		err := tx.QueryRowContext(ctx, `SELECT id, email, expires FROM invitations WHERE token_hash = ? AND accepted IS NULL`,
			HashToken(token)).Scan(&invID, &email, &expires)
		if errors.Is(err, sql.ErrNoRows) || err == nil && now.After(parseStamp(expires)) {
			return ErrExpired
		}
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE invitations SET accepted = ? WHERE id = ?`, stamp(now), invID); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO users (email, name, created) VALUES (?, ?, ?)`, email, name, stamp(now))
		if err != nil {
			return err
		}
		id, _ = res.LastInsertId()
		return audit(ctx, tx, now, id, "user", id, "create", "", email)
	})
	if err != nil {
		return User{}, err
	}
	return d.User(ctx, id)
}

// LoginLink records a one-time login link for a member.
func (d *DB) LoginLink(ctx context.Context, userID int64, valid time.Duration) (string, error) {
	token, hash := NewToken()
	now := d.now()
	_, err := d.db.ExecContext(ctx, `INSERT INTO login_links (token_hash, user_id, created, expires) VALUES (?, ?, ?, ?)`,
		hash, userID, stamp(now), stamp(now.Add(valid)))
	return token, err
}

// RecentLoginLink reports whether a link was sent to the member since t,
// so that the login form cannot be used to flood a mailbox.
func (d *DB) RecentLoginLink(ctx context.Context, userID int64, since time.Time) (bool, error) {
	var n int
	err := d.db.QueryRowContext(ctx, `SELECT count(*) FROM login_links WHERE user_id = ? AND created > ?`, userID, stamp(since)).Scan(&n)
	return n > 0, err
}

// UseLoginLink consumes a login link and returns its member.
func (d *DB) UseLoginLink(ctx context.Context, token string) (User, error) {
	var userID int64
	now := d.now()
	err := d.tx(ctx, func(tx *sql.Tx) error {
		var expires string
		err := tx.QueryRowContext(ctx, `SELECT user_id, expires FROM login_links WHERE token_hash = ? AND used IS NULL`,
			HashToken(token)).Scan(&userID, &expires)
		if errors.Is(err, sql.ErrNoRows) || err == nil && now.After(parseStamp(expires)) {
			return ErrExpired
		}
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE login_links SET used = ? WHERE token_hash = ?`, stamp(now), HashToken(token))
		return err
	})
	if err != nil {
		return User{}, err
	}
	return d.User(ctx, userID)
}

// CreateSession starts a session and returns the secret for its cookie.
func (d *DB) CreateSession(ctx context.Context, userID int64, valid time.Duration) (string, error) {
	token, hash := NewToken()
	csrf, _ := NewToken()
	now := d.now()
	_, err := d.db.ExecContext(ctx, `INSERT INTO sessions (id_hash, user_id, csrf, created, expires) VALUES (?, ?, ?, ?, ?)`,
		hash, userID, csrf, stamp(now), stamp(now.Add(valid)))
	return token, err
}

// Session returns the live session for a cookie value.
func (d *DB) Session(ctx context.Context, token string) (Session, error) {
	var s Session
	var userID int64
	var expires string
	err := d.db.QueryRowContext(ctx, `SELECT user_id, csrf, expires FROM sessions WHERE id_hash = ?`, HashToken(token)).
		Scan(&userID, &s.CSRF, &expires)
	if err != nil {
		return s, notFound(err)
	}
	if d.now().After(parseStamp(expires)) {
		return s, ErrExpired
	}
	s.User, err = d.User(ctx, userID)
	if err == nil && s.User.Suspended {
		return s, ErrExpired
	}
	return s, err
}

func (d *DB) EndSession(ctx context.Context, token string) error {
	_, err := d.db.ExecContext(ctx, `DELETE FROM sessions WHERE id_hash = ?`, HashToken(token))
	return err
}

// Sweep removes expired sessions and links.
func (d *DB) Sweep(ctx context.Context) error {
	now := stamp(d.now())
	if _, err := d.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires < ?`, now); err != nil {
		return err
	}
	_, err := d.db.ExecContext(ctx, `DELETE FROM login_links WHERE expires < ?`, now)
	return err
}
