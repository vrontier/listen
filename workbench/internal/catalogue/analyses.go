package catalogue

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"time"
)

// Analysis is a member's analysis of 1 to n sources (design.md, "Analyses
// and sources"). Its settings are kept per source; one revision covers the
// sources and all their settings.
type Analysis struct {
	ID          int64
	OwnerID     int64
	OwnerName   string
	Name        string
	Description string
	State       string
	Revision    int
	Spec        Spec // of the current revision
	Sources     []AnalysisSource
	Created     time.Time
	Updated     time.Time
}

// Spec is what a revision holds: the settings of each source, as the engine
// takes them, keyed by the stream's catalogue identifier; and from two
// sources on, the group analyses that compare them.
type Spec struct {
	Sources map[string]json.RawMessage `json:"sources"`
	Compare []string                   `json:"compare,omitempty"`
}

// DefaultCompare is what an analysis with two or more sources compares
// until its member chooses otherwise: activity, which wall time allows.
var DefaultCompare = []string{"activity"}

// AnalysisSource is one source of an analysis, with what the analysis needs
// to know of the stream.
type AnalysisSource struct {
	Stream Stream
	Spec   json.RawMessage // this source's settings in the current revision
}

// EngineID is the identifier of the analysis in the engine. Each source of
// the analysis is one engine analysis on that source's stream, all under
// this identifier.
func (a Analysis) EngineID() string { return fmt.Sprintf("a-%04d", a.ID) }

// GroupID is the identifier of the engine group that compares the
// analysis's sources, from two sources on.
func (a Analysis) GroupID() string { return fmt.Sprintf("g-%04d", a.ID) }

// Label is what the engine returns with the analysis's events, so that a
// result can be traced to its revision.
func (a Analysis) Label() string { return fmt.Sprintf("rev-%d", a.Revision) }

// Source returns the analysis's source on a stream.
func (a Analysis) Source(streamID int64) (AnalysisSource, bool) {
	for _, s := range a.Sources {
		if s.Stream.ID == streamID {
			return s, true
		}
	}
	return AnalysisSource{}, false
}

// Revision is one saved version of an analysis's sources and settings.
type Revision struct {
	Revision  int
	Spec      json.RawMessage
	Created   time.Time
	CreatedBy string
}

func key(streamID int64) string { return strconv.FormatInt(streamID, 10) }

// SpecFor builds a revision's settings from settings per stream.
func SpecFor(settings map[int64]json.RawMessage) Spec {
	s := Spec{Sources: map[string]json.RawMessage{}}
	for id, raw := range settings {
		s.Sources[key(id)] = raw
	}
	return s
}

// CreateAnalysis makes an analysis with its sources and their first
// settings, in source order.
func (d *DB) CreateAnalysis(ctx context.Context, owner int64, name, description string, streams []int64, settings map[int64]json.RawMessage) (Analysis, error) {
	now := d.now()
	var id int64
	err := d.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `INSERT INTO analyses (owner_id, name, description, created, updated) VALUES (?, ?, ?, ?, ?)`,
			owner, name, description, stamp(now), stamp(now))
		if err != nil {
			return err
		}
		id, _ = res.LastInsertId()
		spec, err := setSources(ctx, tx, id, streams, settings, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO revisions (analysis_id, revision, spec, created, created_by) VALUES (?, 1, ?, ?, ?)`,
			id, spec, stamp(now), owner); err != nil {
			return err
		}
		after, _ := json.Marshal(map[string]any{"name": name, "description": description, "spec": json.RawMessage(spec)})
		return audit(ctx, tx, now, owner, "analysis", id, "create", "", string(after))
	})
	if err != nil {
		return Analysis{}, err
	}
	return d.Analysis(ctx, id)
}

// setSources replaces an analysis's sources and returns the revision's
// settings for them. With two or more sources and no choice of group
// analyses, DefaultCompare is taken; with one, none.
func setSources(ctx context.Context, tx *sql.Tx, id int64, streams []int64, settings map[int64]json.RawMessage, compare []string) (string, error) {
	if _, err := tx.ExecContext(ctx, `DELETE FROM analysis_sources WHERE analysis_id = ?`, id); err != nil {
		return "", err
	}
	spec := Spec{Sources: map[string]json.RawMessage{}}
	for i, sid := range streams {
		if _, err := tx.ExecContext(ctx, `INSERT INTO analysis_sources (analysis_id, stream_id, position) VALUES (?, ?, ?)`, id, sid, i); err != nil {
			return "", err
		}
		raw := settings[sid]
		if len(raw) == 0 {
			raw = json.RawMessage(`{}`)
		}
		spec.Sources[key(sid)] = raw
	}
	if len(streams) >= 2 {
		spec.Compare = compare
		if spec.Compare == nil {
			spec.Compare = DefaultCompare
		}
	}
	b, err := json.Marshal(spec)
	return string(b), err
}

func (d *DB) loadAnalysis(ctx context.Context, row interface{ Scan(...any) error }) (Analysis, error) {
	var a Analysis
	var spec, created, updated string
	if err := row.Scan(&a.ID, &a.OwnerID, &a.OwnerName, &a.Name, &a.Description, &a.State, &a.Revision, &spec, &created, &updated); err != nil {
		return a, notFound(err)
	}
	a.Created, a.Updated = parseStamp(created), parseStamp(updated)
	json.Unmarshal([]byte(spec), &a.Spec)
	if a.Spec.Sources == nil {
		a.Spec.Sources = map[string]json.RawMessage{}
	}
	return a, nil
}

const analysisSelect = `SELECT a.id, a.owner_id, u.name, a.name, a.description, a.state, a.revision, r.spec, a.created, a.updated
	FROM analyses a JOIN users u ON u.id = a.owner_id JOIN revisions r ON r.analysis_id = a.id AND r.revision = a.revision `

// withSources fills in the sources of analyses.
func (d *DB) withSources(ctx context.Context, as []Analysis) error {
	for i := range as {
		rows, err := d.db.QueryContext(ctx, `SELECT `+streamCols+` FROM analysis_sources x
			JOIN streams s ON s.id = x.stream_id JOIN users u ON u.id = s.owner_id
			WHERE x.analysis_id = ? ORDER BY x.position, s.id`, as[i].ID)
		if err != nil {
			return err
		}
		for rows.Next() {
			st, err := scanStream(rows)
			if err != nil {
				rows.Close()
				return err
			}
			raw := as[i].Spec.Sources[key(st.ID)]
			if len(raw) == 0 {
				raw = json.RawMessage(`{}`)
			}
			as[i].Sources = append(as[i].Sources, AnalysisSource{Stream: st, Spec: raw})
		}
		rows.Close()
	}
	return nil
}

func (d *DB) queryAnalyses(ctx context.Context, where string, args ...any) ([]Analysis, error) {
	rows, err := d.db.QueryContext(ctx, analysisSelect+where+` ORDER BY a.name, a.id`, args...)
	if err != nil {
		return nil, err
	}
	var out []Analysis
	for rows.Next() {
		a, err := d.loadAnalysis(ctx, rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, a)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, d.withSources(ctx, out)
}

// Analysis returns one analysis with its sources.
func (d *DB) Analysis(ctx context.Context, id int64) (Analysis, error) {
	as, err := d.queryAnalyses(ctx, `WHERE a.id = ?`, id)
	if err != nil {
		return Analysis{}, err
	}
	if len(as) == 0 {
		return Analysis{}, ErrNotFound
	}
	return as[0], nil
}

// AllAnalyses lists every analysis, for putting the engine's groups in step.
func (d *DB) AllAnalyses(ctx context.Context) ([]Analysis, error) {
	return d.queryAnalyses(ctx, ``)
}

// AnalysesOf lists a member's analyses.
func (d *DB) AnalysesOf(ctx context.Context, userID int64) ([]Analysis, error) {
	return d.queryAnalyses(ctx, `WHERE a.owner_id = ?`, userID)
}

// AnalysesUsing lists the analyses that have a stream among their sources:
// what the engine runs on it, and who is connected to it.
func (d *DB) AnalysesUsing(ctx context.Context, streamID int64) ([]Analysis, error) {
	return d.queryAnalyses(ctx, `WHERE a.id IN (SELECT analysis_id FROM analysis_sources WHERE stream_id = ?)`, streamID)
}

// UpdateAnalysisTexts changes name and description; they are not part of a
// revision.
func (d *DB) UpdateAnalysisTexts(ctx context.Context, id, by int64, name, description string) error {
	now := d.now()
	return d.tx(ctx, func(tx *sql.Tx) error {
		var oldName, oldDesc string
		if err := tx.QueryRowContext(ctx, `SELECT name, description FROM analyses WHERE id = ?`, id).Scan(&oldName, &oldDesc); err != nil {
			return notFound(err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE analyses SET name = ?, description = ?, updated = ? WHERE id = ?`, name, description, stamp(now), id); err != nil {
			return err
		}
		before, _ := json.Marshal(map[string]string{"name": oldName, "description": oldDesc})
		after, _ := json.Marshal(map[string]string{"name": name, "description": description})
		return audit(ctx, tx, now, by, "analysis", id, "change", string(before), string(after))
	})
}

// SaveRevision stores new sources and settings, and the group analyses
// that compare the sources, as the analysis's next revision.
func (d *DB) SaveRevision(ctx context.Context, id, by int64, streams []int64, settings map[int64]json.RawMessage, compare []string) (Analysis, error) {
	now := d.now()
	err := d.tx(ctx, func(tx *sql.Tx) error {
		var rev int
		var old string
		if err := tx.QueryRowContext(ctx, `SELECT a.revision, r.spec FROM analyses a JOIN revisions r ON r.analysis_id = a.id AND r.revision = a.revision
			WHERE a.id = ?`, id).Scan(&rev, &old); err != nil {
			return notFound(err)
		}
		spec, err := setSources(ctx, tx, id, streams, settings, compare)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO revisions (analysis_id, revision, spec, created, created_by) VALUES (?, ?, ?, ?, ?)`,
			id, rev+1, spec, stamp(now), by); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE analyses SET revision = ?, updated = ? WHERE id = ?`, rev+1, stamp(now), id); err != nil {
			return err
		}
		return audit(ctx, tx, now, by, "analysis", id, "change", old, spec)
	})
	if err != nil {
		return Analysis{}, err
	}
	return d.Analysis(ctx, id)
}

// DeleteAnalysis removes an analysis. The audit trail keeps what it was.
func (d *DB) DeleteAnalysis(ctx context.Context, id, by int64) error {
	now := d.now()
	return d.tx(ctx, func(tx *sql.Tx) error {
		var name, spec string
		if err := tx.QueryRowContext(ctx, `SELECT a.name, r.spec FROM analyses a JOIN revisions r ON r.analysis_id = a.id AND r.revision = a.revision
			WHERE a.id = ?`, id).Scan(&name, &spec); err != nil {
			return notFound(err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM analyses WHERE id = ?`, id); err != nil {
			return err
		}
		before, _ := json.Marshal(map[string]any{"name": name, "spec": json.RawMessage(spec)})
		return audit(ctx, tx, now, by, "analysis", id, "delete", string(before), "")
	})
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

// SortedKeys returns a spec's stream identifiers in order, for display.
func (s Spec) SortedKeys() []string {
	out := make([]string, 0, len(s.Sources))
	for k := range s.Sources {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
