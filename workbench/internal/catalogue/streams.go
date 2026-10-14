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

// Analysis is one member's analysis of a stream.
type Analysis struct {
	ID       int64
	StreamID int64
	OwnerID  int64
	Name     string
	State    string
	Revision int
	Spec     json.RawMessage // the analysis as sent to the engine
	Created  time.Time
	Updated  time.Time
}

func (a Analysis) EngineID() string { return fmt.Sprintf("a-%04d", a.ID) }

// Label is what the engine returns with the analysis's events, so that a
// result can be traced to its revision.
func (a Analysis) Label() string { return fmt.Sprintf("rev-%d", a.Revision) }

// Revision is one saved version of an analysis's settings.
type Revision struct {
	Revision  int
	Spec      json.RawMessage
	Created   time.Time
	CreatedBy string
}

// NewStream is what the registration form delivers.
type NewStream struct {
	OwnerID      int64
	Input        string
	SampleRate   int
	Texts        StreamTexts
	AudioAllowed bool
	Probe        json.RawMessage
	AnalysisName string
	AnalysisSpec json.RawMessage
}

// CreateStream registers a stream with its owner's first analysis.
func (d *DB) CreateStream(ctx context.Context, n NewStream) (Stream, Analysis, error) {
	now := d.now()
	var sid, aid int64
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
		if err := audit(ctx, tx, now, n.OwnerID, "stream", sid, "create", "", string(after)); err != nil {
			return err
		}
		aid, err = createAnalysis(ctx, tx, now, sid, n.OwnerID, n.AnalysisName, n.AnalysisSpec)
		return err
	})
	if err != nil {
		return Stream{}, Analysis{}, err
	}
	s, err := d.Stream(ctx, sid)
	if err != nil {
		return Stream{}, Analysis{}, err
	}
	a, err := d.Analysis(ctx, aid)
	return s, a, err
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

func createAnalysis(ctx context.Context, tx *sql.Tx, now time.Time, streamID, ownerID int64, name string, spec json.RawMessage) (int64, error) {
	res, err := tx.ExecContext(ctx, `INSERT INTO analyses (stream_id, owner_id, name, created, updated) VALUES (?, ?, ?, ?, ?)`,
		streamID, ownerID, name, stamp(now), stamp(now))
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	if _, err := tx.ExecContext(ctx, `INSERT INTO revisions (analysis_id, revision, spec, created, created_by) VALUES (?, 1, ?, ?, ?)`,
		id, string(spec), stamp(now), ownerID); err != nil {
		return 0, err
	}
	return id, audit(ctx, tx, now, ownerID, "analysis", id, "create", "", string(spec))
}

const analysisCols = `a.id, a.stream_id, a.owner_id, a.name, a.state, a.revision, r.spec, a.created, a.updated`
const analysisFrom = ` FROM analyses a JOIN revisions r ON r.analysis_id = a.id AND r.revision = a.revision `

func scanAnalysis(row interface{ Scan(...any) error }) (Analysis, error) {
	var a Analysis
	var spec, created, updated string
	err := row.Scan(&a.ID, &a.StreamID, &a.OwnerID, &a.Name, &a.State, &a.Revision, &spec, &created, &updated)
	a.Spec = json.RawMessage(spec)
	a.Created, a.Updated = parseStamp(created), parseStamp(updated)
	return a, err
}

func (d *DB) Analysis(ctx context.Context, id int64) (Analysis, error) {
	a, err := scanAnalysis(d.db.QueryRowContext(ctx, `SELECT `+analysisCols+analysisFrom+`WHERE a.id = ?`, id))
	return a, notFound(err)
}

// AnalysesOf lists the analyses of a stream.
func (d *DB) AnalysesOf(ctx context.Context, streamID int64) ([]Analysis, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT `+analysisCols+analysisFrom+`WHERE a.stream_id = ? ORDER BY a.id`, streamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Analysis
	for rows.Next() {
		a, err := scanAnalysis(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// SaveRevision stores new settings for an analysis as its next revision.
func (d *DB) SaveRevision(ctx context.Context, id, by int64, spec json.RawMessage) (Analysis, error) {
	now := d.now()
	err := d.tx(ctx, func(tx *sql.Tx) error {
		var rev int
		var old string
		err := tx.QueryRowContext(ctx, `SELECT a.revision, r.spec`+analysisFrom+`WHERE a.id = ?`, id).Scan(&rev, &old)
		if err != nil {
			return notFound(err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO revisions (analysis_id, revision, spec, created, created_by) VALUES (?, ?, ?, ?, ?)`,
			id, rev+1, string(spec), stamp(now), by); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE analyses SET revision = ?, updated = ? WHERE id = ?`, rev+1, stamp(now), id); err != nil {
			return err
		}
		return audit(ctx, tx, now, by, "analysis", id, "change", old, string(spec))
	})
	if err != nil {
		return Analysis{}, err
	}
	return d.Analysis(ctx, id)
}

// Revisions lists an analysis's revisions, newest first.
func (d *DB) Revisions(ctx context.Context, id int64) ([]Revision, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT r.revision, r.spec, r.created, u.name FROM revisions r JOIN users u ON u.id = r.created_by
		WHERE r.analysis_id = ? ORDER BY r.revision DESC`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Revision
	for rows.Next() {
		var r Revision
		var spec, created string
		if err := rows.Scan(&r.Revision, &spec, &created, &r.CreatedBy); err != nil {
			return nil, err
		}
		r.Spec, r.Created = json.RawMessage(spec), parseStamp(created)
		out = append(out, r)
	}
	return out, rows.Err()
}
