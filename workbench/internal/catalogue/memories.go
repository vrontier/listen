package catalogue

import (
	"context"
	"database/sql"
	"time"
)

// The engine keeps an analysis's memories on disk and reports their sizes
// only while the analysis is in the engine, which is during runs. The
// Workbench keeps the last sizes it was told, so that they count against
// the member's disk quota also between runs (members-and-streams.md,
// Resources and caps).

// MemorySize is one memory of one source of an analysis, as last reported.
type MemorySize struct {
	Key   string
	Bytes int64
}

// SetMemorySizes replaces what is known of the memories of one source of
// an analysis.
func (d *DB) SetMemorySizes(ctx context.Context, analysisID, streamID int64, sizes []MemorySize) error {
	now := stamp(d.now())
	return d.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM memory_sizes WHERE analysis_id = ? AND stream_id = ?`, analysisID, streamID); err != nil {
			return err
		}
		for _, m := range sizes {
			if _, err := tx.ExecContext(ctx, `INSERT INTO memory_sizes (analysis_id, stream_id, key, bytes, updated) VALUES (?, ?, ?, ?, ?)`,
				analysisID, streamID, m.Key, m.Bytes, now); err != nil {
				return err
			}
		}
		return nil
	})
}

// MemoryBytesOf adds up the memories of a member's analyses as last
// reported.
func (d *DB) MemoryBytesOf(ctx context.Context, userID int64) (int64, error) {
	var n int64
	err := d.db.QueryRowContext(ctx, `SELECT coalesce(sum(m.bytes), 0) FROM memory_sizes m JOIN analyses an ON an.id = m.analysis_id WHERE an.owner_id = ?`, userID).Scan(&n)
	return n, err
}

// Disk is what counts against a member's disk quota.
type Disk struct {
	Datasets int64
	Memories int64
}

// Total is the two together.
func (k Disk) Total() int64 { return k.Datasets + k.Memories }

// DiskOf returns what a member's data sets and memories take on disk.
func (d *DB) DiskOf(ctx context.Context, userID int64) (Disk, error) {
	var k Disk
	var err error
	if k.Datasets, err = d.DatasetBytesOf(ctx, userID); err != nil {
		return k, err
	}
	k.Memories, err = d.MemoryBytesOf(ctx, userID)
	return k, err
}

// MemoryUse is what the memories of one source of a member's analysis take,
// as last reported.
type MemoryUse struct {
	AnalysisID   int64
	AnalysisName string
	SourceName   string
	Count        int
	Bytes        int64
	Updated      time.Time
}

// MemoriesOf lists the memories of a member's analyses by analysis and
// source, largest first.
func (d *DB) MemoriesOf(ctx context.Context, userID int64) ([]MemoryUse, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT an.id, an.name, s.name, count(*), sum(m.bytes), max(m.updated)
		FROM memory_sizes m JOIN analyses an ON an.id = m.analysis_id JOIN streams s ON s.id = m.stream_id
		WHERE an.owner_id = ? GROUP BY m.analysis_id, m.stream_id ORDER BY sum(m.bytes) DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MemoryUse
	for rows.Next() {
		var m MemoryUse
		var updated string
		if err := rows.Scan(&m.AnalysisID, &m.AnalysisName, &m.SourceName, &m.Count, &m.Bytes, &updated); err != nil {
			return nil, err
		}
		m.Updated = parseStamp(updated)
		out = append(out, m)
	}
	return out, rows.Err()
}
