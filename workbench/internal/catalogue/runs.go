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
	ID           int64
	AnalysisID   int64
	AnalysisName string
	Number       int // counted per analysis, from 1
	Started      time.Time
	StartedBy    string
	Ends         time.Time // planned end; zero: until stopped
	Stopped      time.Time // zero while it runs
	StoppedBy    string    // empty when it ended at its planned end
	Revision     int       // the analysis's revision at the start
	// OfflineStop is how many minutes a source may send nothing before the
	// run stops by itself; 0: it does not (design.md, Runs).
	OfflineStop int
	// EndReason is why the run ended by itself: EndPlanned, EndOffline,
	// EndSilent or EndDisk; empty while it runs or when its member stopped
	// it. EndNote says it in words.
	EndReason string
	EndNote   string

	// The data set the run produced (design.md, Runs): recording while it
	// runs, freezing at its end, then ready (or failed); deleted by its
	// member; none for runs from before data sets.
	Dataset      string
	DatasetBytes int64
	DatasetNote  string
	Summary      json.RawMessage
}

// Data set states.
const (
	DatasetRecording = "recording"
	DatasetFreezing  = "freezing"
	DatasetReady     = "ready"
	DatasetFailed    = "failed"
	DatasetDeleted   = "deleted"
	DatasetNone      = "none"
)

// Why a run ended by itself.
const (
	EndPlanned = "planned" // it reached its planned end
	EndOffline = "offline" // a source sent nothing for its OfflineStop
	EndSilent  = "silent"  // no source sent anything for SilentStop
	EndDisk    = "disk"    // its member's data went over the disk quota
)

// DefaultOfflineStop is the run setup's default for OfflineStop, in
// minutes; SilentStop ends any run whose sources all sent nothing for that
// long (design.md, Runs).
const (
	DefaultOfflineStop = 15
	SilentStop         = 15 * time.Minute
)

// Active reports whether the run is going on at t.
func (r Run) Active(t time.Time) bool {
	return r.Stopped.IsZero() && (r.Ends.IsZero() || t.Before(r.Ends))
}

// ErrRunActive is returned when an analysis that already runs is started.
var ErrRunActive = errors.New("the analysis is already running")

// ErrNoRun is returned when an analysis that does not run is stopped.
var ErrNoRun = errors.New("the analysis is not running")

const runSelect = `SELECT r.id, r.analysis_id, an.name,
	(SELECT count(*) FROM runs x WHERE x.analysis_id = r.analysis_id AND x.id <= r.id),
	r.started, su.name, coalesce(r.ends, ''), coalesce(r.stopped, ''), coalesce(tu.name, ''), r.revision,
	r.dataset, r.dataset_bytes, r.dataset_note, r.summary, r.offline_stop, r.end_reason, r.end_note
	FROM runs r JOIN analyses an ON an.id = r.analysis_id JOIN users su ON su.id = r.started_by LEFT JOIN users tu ON tu.id = r.stopped_by `

func scanRun(row interface{ Scan(...any) error }) (Run, error) {
	var r Run
	var started, ends, stopped, summary string
	if err := row.Scan(&r.ID, &r.AnalysisID, &r.AnalysisName, &r.Number, &started, &r.StartedBy, &ends, &stopped, &r.StoppedBy, &r.Revision,
		&r.Dataset, &r.DatasetBytes, &r.DatasetNote, &summary, &r.OfflineStop, &r.EndReason, &r.EndNote); err != nil {
		return r, notFound(err)
	}
	r.Summary = json.RawMessage(summary)
	r.Started, r.Ends, r.Stopped = parseStamp(started), parseStamp(ends), parseStamp(stopped)
	return r, nil
}

// activeWhere selects the runs going on now; its one argument is now.
const activeWhere = `r.stopped IS NULL AND (r.ends IS NULL OR r.ends > ?)`

// Runs lists an analysis's runs, newest first.
func (d *DB) Runs(ctx context.Context, analysisID int64) ([]Run, error) {
	return d.queryRuns(ctx, `WHERE r.analysis_id = ? AND r.removed IS NULL ORDER BY r.id DESC`, analysisID)
}

// RunsOf lists the runs of a member's analyses, newest first.
func (d *DB) RunsOf(ctx context.Context, userID int64) ([]Run, error) {
	return d.queryRuns(ctx, `WHERE an.owner_id = ? AND r.removed IS NULL ORDER BY r.id DESC LIMIT 500`, userID)
}

func (d *DB) queryRuns(ctx context.Context, where string, args ...any) ([]Run, error) {
	rows, err := d.db.QueryContext(ctx, runSelect+where, args...)
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

// StartRun starts a run of an analysis that stops when a source sends
// nothing for DefaultOfflineStop minutes; a zero ends runs until stopped.
func (d *DB) StartRun(ctx context.Context, analysisID, by int64, ends time.Time) (Run, error) {
	return d.StartRunWith(ctx, analysisID, by, ends, DefaultOfflineStop)
}

// StartRunWith starts a run; offlineStop is in minutes, 0 for no stop when
// a source sends nothing.
func (d *DB) StartRunWith(ctx context.Context, analysisID, by int64, ends time.Time, offlineStop int) (Run, error) {
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
		res, err := tx.ExecContext(ctx, `INSERT INTO runs (analysis_id, started, started_by, ends, revision, offline_stop) VALUES (?, ?, ?, ?, ?, ?)`,
			analysisID, stamp(now), by, endsVal, rev, offlineStop)
		if err != nil {
			return err
		}
		id, _ = res.LastInsertId()
		after, _ := json.Marshal(map[string]any{"run": id, "ends": endsVal, "revision": rev, "offline_stop_min": offlineStop})
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
		// Stopped by its member: nobody needs telling.
		if _, err := tx.ExecContext(ctx, `UPDATE runs SET stopped = ?, stopped_by = ?, mailed = ? WHERE id = ?`, stamp(now), by, stamp(now), id); err != nil {
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
		if _, err := tx.ExecContext(ctx, `UPDATE runs SET stopped = ends, end_reason = ?, end_note = 'it reached its planned end' WHERE id = ?`, EndPlanned, e.id); err != nil {
			return err
		}
		after, _ := json.Marshal(map[string]any{"run": e.id, "at": e.ends})
		if err := audit(ctx, tx, parseStamp(e.ends), 0, "analysis", e.analysis, "run end", "", string(after)); err != nil {
			return err
		}
	}
	return nil
}

// Run returns one run; a deleted run is not found.
func (d *DB) Run(ctx context.Context, id int64) (Run, error) {
	return scanRun(d.db.QueryRowContext(ctx, runSelect+`WHERE r.id = ? AND r.removed IS NULL`, id))
}

// ErrRunNotFinished is returned when a run that goes on, or whose data set
// is still being put together, is deleted.
var ErrRunNotFinished = errors.New("the run has not finished")

// DeleteRun deletes a finished run: it goes from every list, its data set
// is marked deleted (the caller removes the files), and the audit trail
// keeps what it was. The other runs of the analysis keep their numbers.
func (d *DB) DeleteRun(ctx context.Context, runID, by int64) error {
	now := d.now()
	return d.tx(ctx, func(tx *sql.Tx) error {
		r, err := scanRun(tx.QueryRowContext(ctx, runSelect+`WHERE r.id = ? AND r.removed IS NULL`, runID))
		if err != nil {
			return err
		}
		if r.Active(now) || r.Dataset == DatasetRecording || r.Dataset == DatasetFreezing {
			return ErrRunNotFinished
		}
		if _, err := tx.ExecContext(ctx, `UPDATE runs SET removed = ?, dataset = 'deleted', dataset_bytes = 0, mailed = coalesce(mailed, ?) WHERE id = ?`,
			stamp(now), stamp(now), runID); err != nil {
			return err
		}
		before, _ := json.Marshal(map[string]any{"run": runID, "number": r.Number, "started": stamp(r.Started), "revision": r.Revision,
			"dataset": r.Dataset, "bytes": r.DatasetBytes})
		return audit(ctx, tx, now, by, "analysis", r.AnalysisID, "run delete", string(before), "")
	})
}

// ActiveRuns lists the runs going on now, of every member.
func (d *DB) ActiveRuns(ctx context.Context) ([]Run, error) {
	return d.queryRuns(ctx, `WHERE `+activeWhere+` ORDER BY r.id`, stamp(d.now()))
}

// UnfrozenRuns lists ended runs whose data set is not finished: they ended
// while the Workbench was down, or it stopped while freezing.
func (d *DB) UnfrozenRuns(ctx context.Context) ([]Run, error) {
	return d.queryRuns(ctx, `WHERE r.dataset IN ('recording', 'freezing') AND NOT (`+activeWhere+`) ORDER BY r.id`, stamp(d.now()))
}

// SetDataset records the state of a run's data set.
func (d *DB) SetDataset(ctx context.Context, runID int64, state string, bytes int64, note string, summary json.RawMessage) error {
	if summary == nil {
		summary = json.RawMessage(`{}`)
	}
	_, err := d.db.ExecContext(ctx, `UPDATE runs SET dataset = ?, dataset_bytes = ?, dataset_note = ?, summary = ? WHERE id = ?`,
		state, bytes, note, string(summary), runID)
	return err
}

// DeleteDataset marks a run's data set deleted; the caller removes its
// files. The run stays in the list.
func (d *DB) DeleteDataset(ctx context.Context, runID, by int64) error {
	now := d.now()
	return d.tx(ctx, func(tx *sql.Tx) error {
		var analysis, bytes int64
		if err := tx.QueryRowContext(ctx, `SELECT analysis_id, dataset_bytes FROM runs WHERE id = ?`, runID).Scan(&analysis, &bytes); err != nil {
			return notFound(err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE runs SET dataset = 'deleted', dataset_bytes = 0 WHERE id = ?`, runID); err != nil {
			return err
		}
		before, _ := json.Marshal(map[string]any{"run": runID, "bytes": bytes})
		return audit(ctx, tx, now, by, "analysis", analysis, "data set delete", string(before), "")
	})
}

// DatasetBytesOf adds up the data sets of a member's analyses, which count
// against their disk quota.
func (d *DB) DatasetBytesOf(ctx context.Context, userID int64) (int64, error) {
	var n int64
	err := d.db.QueryRowContext(ctx, `SELECT coalesce(sum(r.dataset_bytes), 0) FROM runs r JOIN analyses an ON an.id = r.analysis_id WHERE an.owner_id = ?`, userID).Scan(&n)
	return n, err
}

// EndRun ends a run by itself, for a reason other than its planned end,
// before that end. It returns false when the run had ended already.
func (d *DB) EndRun(ctx context.Context, runID int64, reason, note string) (bool, error) {
	now := d.now()
	ended := false
	err := d.tx(ctx, func(tx *sql.Tx) error {
		var analysis int64
		err := tx.QueryRowContext(ctx, `SELECT analysis_id FROM runs WHERE id = ? AND stopped IS NULL AND (ends IS NULL OR ends > ?)`, runID, stamp(now)).Scan(&analysis)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE runs SET stopped = ?, end_reason = ?, end_note = ? WHERE id = ?`, stamp(now), reason, note, runID); err != nil {
			return err
		}
		ended = true
		after, _ := json.Marshal(map[string]any{"run": runID, "reason": reason, "note": note})
		return audit(ctx, tx, now, 0, "analysis", analysis, "run end", "", string(after))
	})
	return ended, err
}

// UntoldRuns lists the runs that ended by themselves and whose member has
// not been told yet.
func (d *DB) UntoldRuns(ctx context.Context) ([]Run, error) {
	return d.queryRuns(ctx, `WHERE r.mailed IS NULL AND r.stopped IS NOT NULL ORDER BY r.id`)
}

// NoteRunTold records that a run's member was told it ended.
func (d *DB) NoteRunTold(ctx context.Context, runID int64) error {
	_, err := d.db.ExecContext(ctx, `UPDATE runs SET mailed = ? WHERE id = ?`, stamp(d.now()), runID)
	return err
}
