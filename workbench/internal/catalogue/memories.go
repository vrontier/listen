package catalogue

import (
	"context"
	"database/sql"
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
