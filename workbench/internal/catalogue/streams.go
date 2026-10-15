package catalogue

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Stream states (members-and-streams.md, "Of a stream").
const (
	StreamDraft      = "draft"
	StreamProduction = "production"
	StreamPaused     = "paused"
	StreamSuspended  = "suspended"
)

// Stream is a registered stream. Its address and sample rate never change.
type Stream struct {
	ID           int64
	Slug         string
	OwnerID      int64
	OwnerName    string
	Input        string
	SampleRate   int
	Texts        StreamTexts
	AudioAllowed bool
	State        string
	AlwaysOn     bool
	Attested     time.Time
	Probe        json.RawMessage
	Created      time.Time
	Updated      time.Time
}

// StreamTexts are the parts of a stream its owner may edit at any time.
type StreamTexts struct {
	Name     string `json:"name"`
	Place    string `json:"place"`
	TimeZone string `json:"timezone"`
	// Lat and Lon locate the receiver, in decimal degrees; nil when not given.
	Lat         *float64 `json:"lat,omitempty"`
	Lon         *float64 `json:"lon,omitempty"`
	Description string   `json:"description"`
	Credit      string   `json:"credit"`
	CreditURL   string   `json:"credit_url"`
}

// EngineID is the stream's identifier towards the engine: the catalogue's
// own key, never a name a member can change (control-api.md, Identifiers).
func (s Stream) EngineID() string { return fmt.Sprintf("s-%04d", s.ID) }

// NewStream is what the registration form delivers.
type NewStream struct {
	OwnerID      int64
	Input        string
	SampleRate   int
	Texts        StreamTexts
	AudioAllowed bool
	Probe        json.RawMessage
}

// CreateStream registers a stream. It makes no analysis.
func (d *DB) CreateStream(ctx context.Context, n NewStream) (Stream, error) {
	now := d.now()
	var sid int64
	err := d.tx(ctx, func(tx *sql.Tx) error {
		slug, err := freeSlug(ctx, tx, n.Texts.Name)
		if err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO streams
			(slug, owner_id, input, sample_rate, name, place, timezone, lat, lon, description, credit, credit_url, audio_allowed, attested, probe, created, updated)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			slug, n.OwnerID, n.Input, n.SampleRate, n.Texts.Name, n.Texts.Place, n.Texts.TimeZone, n.Texts.Lat, n.Texts.Lon, n.Texts.Description,
			n.Texts.Credit, n.Texts.CreditURL, n.AudioAllowed, stamp(now), string(n.Probe), stamp(now), stamp(now))
		if err != nil {
			if strings.Contains(err.Error(), "UNIQUE constraint failed: streams.input") {
				return ErrDuplicateInput
			}
			return err
		}
		sid, _ = res.LastInsertId()
		after, _ := json.Marshal(map[string]any{"input": n.Input, "sample_rate": n.SampleRate, "texts": n.Texts, "audio_allowed": n.AudioAllowed})
		return audit(ctx, tx, now, n.OwnerID, "stream", sid, "create", "", string(after))
	})
	if err != nil {
		return Stream{}, err
	}
	return d.Stream(ctx, sid)
}

// ErrDuplicateInput: a stream with this address exists already. Streams
// are shared; the member is pointed to the existing one.
var ErrDuplicateInput = fmt.Errorf("a stream with this address exists already")

func freeSlug(ctx context.Context, tx *sql.Tx, name string) (string, error) {
	base := Slugify(name)
	for i := 1; ; i++ {
		slug := base
		if i > 1 {
			slug = fmt.Sprintf("%s-%d", base, i)
		}
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM streams WHERE slug = ?`, slug).Scan(&n); err != nil {
			return "", err
		}
		if n == 0 {
			return slug, nil
		}
	}
}

// Slugify makes a URL path segment from a name.
func Slugify(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case r == 'ä':
			b.WriteString("ae")
		case r == 'ö':
			b.WriteString("oe")
		case r == 'ü':
			b.WriteString("ue")
		case r == 'ß':
			b.WriteString("ss")
		default:
			if !dash && b.Len() > 0 {
				b.WriteByte('-')
				dash = true
			}
		}
		if b.Len() >= 40 {
			break
		}
	}
	s := strings.Trim(b.String(), "-")
	if s == "" {
		s = "stream"
	}
	return s
}

const streamCols = `s.id, s.slug, s.owner_id, u.name, s.input, s.sample_rate, s.name, s.place, s.timezone, s.lat, s.lon, s.description,
	s.credit, s.credit_url, s.audio_allowed, s.state, s.always_on, s.attested, s.probe, s.created, s.updated`

func scanStream(row interface{ Scan(...any) error }) (Stream, error) {
	var s Stream
	var attested, probe, created, updated string
	err := row.Scan(&s.ID, &s.Slug, &s.OwnerID, &s.OwnerName, &s.Input, &s.SampleRate, &s.Texts.Name, &s.Texts.Place,
		&s.Texts.TimeZone, &s.Texts.Lat, &s.Texts.Lon, &s.Texts.Description, &s.Texts.Credit, &s.Texts.CreditURL, &s.AudioAllowed, &s.State,
		&s.AlwaysOn, &attested, &probe, &created, &updated)
	s.Attested, s.Created, s.Updated = parseStamp(attested), parseStamp(created), parseStamp(updated)
	s.Probe = json.RawMessage(probe)
	return s, err
}

func (d *DB) Stream(ctx context.Context, id int64) (Stream, error) {
	s, err := scanStream(d.db.QueryRowContext(ctx, `SELECT `+streamCols+` FROM streams s JOIN users u ON u.id = s.owner_id WHERE s.id = ?`, id))
	return s, notFound(err)
}

func (d *DB) StreamBySlug(ctx context.Context, slug string) (Stream, error) {
	s, err := scanStream(d.db.QueryRowContext(ctx, `SELECT `+streamCols+` FROM streams s JOIN users u ON u.id = s.owner_id WHERE s.slug = ?`, slug))
	return s, notFound(err)
}

func (d *DB) StreamByInput(ctx context.Context, input string) (Stream, error) {
	s, err := scanStream(d.db.QueryRowContext(ctx, `SELECT `+streamCols+` FROM streams s JOIN users u ON u.id = s.owner_id WHERE s.input = ?`, input))
	return s, notFound(err)
}

func (d *DB) queryStreams(ctx context.Context, where string, args ...any) ([]Stream, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT `+streamCols+` FROM streams s JOIN users u ON u.id = s.owner_id `+where+` ORDER BY s.name`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Stream
	for rows.Next() {
		s, err := scanStream(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// StreamsOf lists the streams a member owns.
func (d *DB) StreamsOf(ctx context.Context, userID int64) ([]Stream, error) {
	return d.queryStreams(ctx, `WHERE s.owner_id = ?`, userID)
}

// AllStreams lists every stream, for the engine's wanted state and the admin.
func (d *DB) AllStreams(ctx context.Context) ([]Stream, error) {
	return d.queryStreams(ctx, ``)
}

// UpdateStreamTexts changes the editable texts of a stream.
func (d *DB) UpdateStreamTexts(ctx context.Context, id, by int64, t StreamTexts, audioAllowed bool) error {
	now := d.now()
	return d.tx(ctx, func(tx *sql.Tx) error {
		old, err := scanStream(tx.QueryRowContext(ctx, `SELECT `+streamCols+` FROM streams s JOIN users u ON u.id = s.owner_id WHERE s.id = ?`, id))
		if err != nil {
			return notFound(err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE streams SET name = ?, place = ?, timezone = ?, lat = ?, lon = ?, description = ?, credit = ?, credit_url = ?,
			audio_allowed = ?, updated = ? WHERE id = ?`,
			t.Name, t.Place, t.TimeZone, t.Lat, t.Lon, t.Description, t.Credit, t.CreditURL, audioAllowed, stamp(now), id); err != nil {
			return err
		}
		before, _ := json.Marshal(map[string]any{"texts": old.Texts, "audio_allowed": old.AudioAllowed})
		after, _ := json.Marshal(map[string]any{"texts": t, "audio_allowed": audioAllowed})
		return audit(ctx, tx, now, by, "stream", id, "change", string(before), string(after))
	})
}

// SetStreamState moves a stream to another state.
func (d *DB) SetStreamState(ctx context.Context, id, by int64, state string, alwaysOn bool) error {
	now := d.now()
	return d.tx(ctx, func(tx *sql.Tx) error {
		var oldState string
		var oldAlways bool
		if err := tx.QueryRowContext(ctx, `SELECT state, always_on FROM streams WHERE id = ?`, id).Scan(&oldState, &oldAlways); err != nil {
			return notFound(err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE streams SET state = ?, always_on = ?, updated = ? WHERE id = ?`, state, alwaysOn, stamp(now), id); err != nil {
			return err
		}
		before, _ := json.Marshal(map[string]any{"state": oldState, "always_on": oldAlways})
		after, _ := json.Marshal(map[string]any{"state": state, "always_on": alwaysOn})
		return audit(ctx, tx, now, by, "stream", id, "state", string(before), string(after))
	})
}

// DeleteStream removes a stream and its analyses. The audit trail keeps
// what was deleted.
func (d *DB) DeleteStream(ctx context.Context, id, by int64) error {
	now := d.now()
	return d.tx(ctx, func(tx *sql.Tx) error {
		old, err := scanStream(tx.QueryRowContext(ctx, `SELECT `+streamCols+` FROM streams s JOIN users u ON u.id = s.owner_id WHERE s.id = ?`, id))
		if err != nil {
			return notFound(err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM streams WHERE id = ?`, id); err != nil {
			return err
		}
		before, _ := json.Marshal(map[string]any{"input": old.Input, "sample_rate": old.SampleRate, "texts": old.Texts})
		return audit(ctx, tx, now, by, "stream", id, "delete", string(before), "")
	})
}
