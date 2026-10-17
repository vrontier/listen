package catalogue

import (
	"context"
	"database/sql"
	"encoding/json"
	"strconv"
	"time"
)

// Previews and trials are kept here as well as in the Workbench's memory,
// so that a restart of the Workbench (a deploy) does not end them
// (2026-10-16). Runs are in the runs table already.

// Trial is a trial of changed settings next to a run: per stream, the
// settings on trial.
type Trial struct {
	Started  time.Time
	Until    time.Time
	Settings map[int64]json.RawMessage
}

func migrateLive(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS previews (
	analysis_id INTEGER PRIMARY KEY REFERENCES analyses(id) ON DELETE CASCADE,
	until       TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS trials (
	analysis_id INTEGER PRIMARY KEY REFERENCES analyses(id) ON DELETE CASCADE,
	started     TEXT NOT NULL,
	until       TEXT NOT NULL,
	settings    TEXT NOT NULL
)`)
	return err
}

// SavePreview notes that an analysis's preview runs until a time.
func (d *DB) SavePreview(ctx context.Context, analysisID int64, until time.Time) error {
	_, err := d.db.ExecContext(ctx, `INSERT INTO previews (analysis_id, until) VALUES (?, ?)
		ON CONFLICT (analysis_id) DO UPDATE SET until = excluded.until`, analysisID, stamp(until))
	return err
}

func (d *DB) DeletePreview(ctx context.Context, analysisID int64) error {
	_, err := d.db.ExecContext(ctx, `DELETE FROM previews WHERE analysis_id = ?`, analysisID)
	return err
}

// Previews returns the previews kept, by analysis.
func (d *DB) Previews(ctx context.Context) (map[int64]time.Time, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT analysis_id, until FROM previews`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]time.Time{}
	for rows.Next() {
		var id int64
		var until string
		if err := rows.Scan(&id, &until); err != nil {
			return nil, err
		}
		out[id] = parseStamp(until)
	}
	return out, rows.Err()
}

// SaveTrial notes an analysis's trial.
func (d *DB) SaveTrial(ctx context.Context, analysisID int64, t Trial) error {
	settings := map[string]json.RawMessage{}
	for id, raw := range t.Settings {
		settings[strconv.FormatInt(id, 10)] = raw
	}
	b, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	_, err = d.db.ExecContext(ctx, `INSERT INTO trials (analysis_id, started, until, settings) VALUES (?, ?, ?, ?)
		ON CONFLICT (analysis_id) DO UPDATE SET started = excluded.started, until = excluded.until, settings = excluded.settings`,
		analysisID, stamp(t.Started), stamp(t.Until), string(b))
	return err
}

func (d *DB) DeleteTrial(ctx context.Context, analysisID int64) error {
	_, err := d.db.ExecContext(ctx, `DELETE FROM trials WHERE analysis_id = ?`, analysisID)
	return err
}

// Trials returns the trials kept, by analysis.
func (d *DB) Trials(ctx context.Context) (map[int64]Trial, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT analysis_id, started, until, settings FROM trials`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]Trial{}
	for rows.Next() {
		var id int64
		var started, until, raw string
		if err := rows.Scan(&id, &started, &until, &raw); err != nil {
			return nil, err
		}
		var settings map[string]json.RawMessage
		if err := json.Unmarshal([]byte(raw), &settings); err != nil {
			return nil, err
		}
		t := Trial{Started: parseStamp(started), Until: parseStamp(until), Settings: map[int64]json.RawMessage{}}
		for k, v := range settings {
			if n, err := strconv.ParseInt(k, 10, 64); err == nil {
				t.Settings[n] = v
			}
		}
		out[id] = t
	}
	return out, rows.Err()
}
