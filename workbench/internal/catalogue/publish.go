package catalogue

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// Publishing (members-and-streams.md, Of an analysis): an analysis is
// published at once, without review, at /research/<slug>, and withdrawn at
// once. Using a source someone else owns needs that owner's consent.

// Analysis states.
const (
	AnalysisPrivate   = "private"
	AnalysisPublished = "published"
	AnalysisWithdrawn = "withdrawn"
)

func freeAnalysisSlug(ctx context.Context, tx *sql.Tx, name string) (string, error) {
	base := Slugify(name)
	if base == "" {
		base = "research"
	}
	for i := 1; ; i++ {
		slug := base
		if i > 1 {
			slug = fmt.Sprintf("%s-%d", base, i)
		}
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM analyses WHERE slug = ?`, slug).Scan(&n); err != nil {
			return "", err
		}
		if n == 0 {
			return slug, nil
		}
	}
}

// PublishAnalysis makes an analysis public. Its address is made from its
// title the first time and kept afterwards. attested is when the member
// confirmed the rights once more.
func (d *DB) PublishAnalysis(ctx context.Context, id, by int64, withData bool, attested time.Time) (string, error) {
	now := d.now()
	var slug string
	err := d.tx(ctx, func(tx *sql.Tx) error {
		var name, state string
		var old sql.NullString
		if err := tx.QueryRowContext(ctx, `SELECT name, state, slug FROM analyses WHERE id = ?`, id).Scan(&name, &state, &old); err != nil {
			return notFound(err)
		}
		slug = old.String
		if slug == "" {
			var err error
			if slug, err = freeAnalysisSlug(ctx, tx, name); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE analyses SET state = ?, slug = ?, published = ?, withdrawn = NULL, publish_data = ? WHERE id = ?`,
			AnalysisPublished, slug, stamp(now), withData, id); err != nil {
			return err
		}
		after, _ := json.Marshal(map[string]any{"slug": slug, "data_sets": withData, "attested": stamp(attested)})
		return audit(ctx, tx, now, by, "analysis", id, "publish", state, string(after))
	})
	return slug, err
}

// WithdrawAnalysis takes an analysis off its public page, by its member or
// the master admin. The page then says it was withdrawn.
func (d *DB) WithdrawAnalysis(ctx context.Context, id, by int64, reason string) error {
	now := d.now()
	return d.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE analyses SET state = ?, withdrawn = ? WHERE id = ? AND state = ?`,
			AnalysisWithdrawn, stamp(now), id, AnalysisPublished)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		after, _ := json.Marshal(map[string]any{"reason": reason})
		return audit(ctx, tx, now, by, "analysis", id, "withdraw", AnalysisPublished, string(after))
	})
}

// AnalysisBySlug finds an analysis by its public address.
func (d *DB) AnalysisBySlug(ctx context.Context, slug string) (Analysis, error) {
	as, err := d.queryAnalyses(ctx, `WHERE a.slug = ?`, slug)
	if err != nil {
		return Analysis{}, err
	}
	if len(as) == 0 {
		return Analysis{}, ErrNotFound
	}
	return as[0], nil
}

// PublishedAnalyses lists what is public now.
func (d *DB) PublishedAnalyses(ctx context.Context) ([]Analysis, error) {
	return d.queryAnalyses(ctx, `WHERE a.state = ?`, AnalysisPublished)
}

// AnyPublished tells whether anything is public now.
func (d *DB) AnyPublished(ctx context.Context) (bool, error) {
	var n int
	err := d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM analyses WHERE state = ?`, AnalysisPublished).Scan(&n)
	return n > 0, err
}

// Report is a visitor's report about a public page.
type Report struct {
	ID         int64
	AnalysisID int64 // 0 once the analysis is deleted
	Title      string
	Message    string
	Contact    string
	Created    time.Time
}

// AddReport records a report about a published analysis.
func (d *DB) AddReport(ctx context.Context, analysisID int64, title, message, contact string) (int64, error) {
	res, err := d.db.ExecContext(ctx, `INSERT INTO reports (analysis_id, title, message, contact, created) VALUES (?, ?, ?, ?, ?)`,
		analysisID, title, message, contact, stamp(d.now()))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// OpenReports lists the reports not yet closed, oldest first.
func (d *DB) OpenReports(ctx context.Context) ([]Report, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT id, coalesce(analysis_id, 0), title, message, contact, created FROM reports WHERE closed IS NULL ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Report
	for rows.Next() {
		var r Report
		var created string
		if err := rows.Scan(&r.ID, &r.AnalysisID, &r.Title, &r.Message, &r.Contact, &created); err != nil {
			return nil, err
		}
		r.Created = parseStamp(created)
		out = append(out, r)
	}
	return out, rows.Err()
}

// CloseReport marks a report as dealt with.
func (d *DB) CloseReport(ctx context.Context, id, by int64) error {
	now := d.now()
	return d.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE reports SET closed = ?, closed_by = ? WHERE id = ? AND closed IS NULL`, stamp(now), by, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return audit(ctx, tx, now, by, "report", id, "close", "", "")
	})
}
