package catalogue

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// A run is a period in which an analysis runs whether or not anyone
// watches: its sources stay connected from the start until it is stopped
// or reaches its planned end (design.md, "Runs").
type Run struct {
	ID         int64
	AnalysisID int64
	Number     int // counted per analysis, from 1
	Started    time.Time
	StartedBy  string
	Ends       time.Time // planned end; zero: until stopped
	Stopped    time.Time // zero while it runs
	StoppedBy  string    // empty when it ended at its planned end
	Revision   int       // the analysis's revision at the start
}

// Active reports whether the run is going on at t.
func (r Run) Active(t time.Time) bool {
	return r.Stopped.IsZero() && (r.Ends.IsZero() || t.Before(r.Ends))
}

// ErrRunActive is returned when an analysis that already runs is started.
var ErrRunActive = errors.New("the analysis is already running")

// ErrNoRun is returned when an analysis that does not run is stopped.
var ErrNoRun = errors.New("the analysis is not running")

const runSelect = `SELECT r.id, r.analysis_id,
	(SELECT count(*) FROM runs x WHERE x.analysis_id = r.analysis_id AND x.id <= r.id),
	r.started, su.name, coalesce(r.ends, ''), coalesce(r.stopped, ''), coalesce(tu.name, ''), r.revision
	FROM runs r JOIN users su ON su.id = r.started_by LEFT JOIN users tu ON tu.id = r.stopped_by `

func scanRun(row interface{ Scan(...any) error }) (Run, error) {
	var r Run
	var started, ends, stopped string
	if err := row.Scan(&r.ID, &r.AnalysisID, &r.Number, &started, &r.StartedBy, &ends, &stopped, &r.StoppedBy, &r.Revision); err != nil {
		return r, notFound(err)
	}
	r.Started, r.Ends, r.Stopped = parseStamp(started), parseStamp(ends), parseStamp(stopped)
	return r, nil
}

// activeWhere selects the runs going on now; its one argument is now.
const activeWhere = `r.stopped IS NULL AND (r.ends IS NULL OR r.ends > ?)`

// Runs lists an analysis's runs, newest first.
func (d *DB) Runs(ctx context.Context, analysisID int64) ([]Run, error) {
	rows, err := d.db.QueryContext(ctx, runSelect+`WHERE r.analysis_id = ? ORDER BY r.id DESC`, analysisID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ActiveRun returns the run an analysis is in, if any.
func (d *DB) ActiveRun(ctx context.Context, analysisID int64) (Run, bool, error) {
	r, err := scanRun(d.db.QueryRowContext(ctx, runSelect+`WHERE r.analysis_id = ? AND `+activeWhere, analysisID, stamp(d.now())))
	if errors.Is(err, ErrNotFound) {
		return Run{}, false, nil
	}
	return r, err == nil, err
}

// StreamInRun reports whether an analysis using the stream is in a run, so
// that the stream has to stay connected.
func (d *DB) StreamInRun(ctx context.Context, streamID int64) (bool, error) {
	var n int
	err := d.db.QueryRowContext(ctx, `SELECT count(*) FROM runs r JOIN analysis_sources x ON x.analysis_id = r.analysis_id
		WHERE x.stream_id = ? AND `+activeWhere, streamID, stamp(d.now())).Scan(&n)
	return n > 0, err
}

// StartRun starts a run of an analysis; a zero ends runs until stopped.
func (d *DB) StartRun(ctx context.Context, analysisID, by int64, ends time.Time) (Run, error) {
	now := d.now()
	var id int64
	err := d.tx(ctx, func(tx *sql.Tx) error {
		if err := closeEnded(ctx, tx, now); err != nil {
			return err
		}
		var n, rev int
		if err := tx.QueryRowContext(ctx, `SELECT revision FROM analyses WHERE id = ?`, analysisID).Scan(&rev); err != nil {
			return notFound(err)
		}
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM runs WHERE analysis_id = ? AND stopped IS NULL`, analysisID).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return ErrRunActive
		}
		var endsVal any
		if !ends.IsZero() {
			endsVal = stamp(ends)
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO runs (analysis_id, started, started_by, ends, revision) VALUES (?, ?, ?, ?, ?)`,
			analysisID, stamp(now), by, endsVal, rev)
		if err != nil {
			return err
		}
		id, _ = res.LastInsertId()
		after, _ := json.Marshal(map[string]any{"run": id, "ends": endsVal, "revision": rev})
		return audit(ctx, tx, now, by, "analysis", analysisID, "run start", "", string(after))
	})
	if err != nil {
		return Run{}, err
	}
	return scanRun(d.db.QueryRowContext(ctx, runSelect+`WHERE r.id = ?`, id))
}

// StopRun stops an analysis's run now.
func (d *DB) StopRun(ctx context.Context, analysisID, by int64) error {
	now := d.now()
	return d.tx(ctx, func(tx *sql.Tx) error {
		if err := closeEnded(ctx, tx, now); err != nil {
			return err
		}
		var id int64
		err := tx.QueryRowContext(ctx, `SELECT id FROM runs WHERE analysis_id = ? AND stopped IS NULL`, analysisID).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNoRun
		}
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE runs SET stopped = ?, stopped_by = ? WHERE id = ?`, stamp(now), by, id); err != nil {
			return err
		}
		after, _ := json.Marshal(map[string]any{"run": id})
		return audit(ctx, tx, now, by, "analysis", analysisID, "run stop", "", string(after))
	})
}

// EndRuns closes the runs that have reached their planned end and returns
// the analyses they belong to, so that their sources can be let go.
func (d *DB) EndRuns(ctx context.Context) ([]int64, error) {
	now := d.now()
	var ids []int64
	err := d.tx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT DISTINCT analysis_id FROM runs WHERE stopped IS NULL AND ends IS NOT NULL AND ends <= ?`, stamp(now))
		if err != nil {
			return err
		}
		for rows.Next() {
			var id int64
			rows.Scan(&id)
			ids = append(ids, id)
		}
		rows.Close()
		return closeEnded(ctx, tx, now)
	})
	return ids, err
}

// closeEnded marks runs past their planned end as stopped at that end, with
// an entry in the audit trail by no one.
func closeEnded(ctx context.Context, tx *sql.Tx, now time.Time) error {
	rows, err := tx.QueryContext(ctx, `SELECT id, analysis_id, ends FROM runs WHERE stopped IS NULL AND ends IS NOT NULL AND ends <= ?`, stamp(now))
	if err != nil {
		return err
	}
	type ended struct {
		id, analysis int64
		ends         string
	}
	var es []ended
	for rows.Next() {
		var e ended
		rows.Scan(&e.id, &e.analysis, &e.ends)
		es = append(es, e)
	}
	rows.Close()
	for _, e := range es {
		if _, err := tx.ExecContext(ctx, `UPDATE runs SET stopped = ends WHERE id = ?`, e.id); err != nil {
			return err
		}
		after, _ := json.Marshal(map[string]any{"run": e.id, "at": e.ends})
		if err := audit(ctx, tx, parseStamp(e.ends), 0, "analysis", e.analysis, "run end", "", string(after)); err != nil {
			return err
		}
	}
	return nil
}
