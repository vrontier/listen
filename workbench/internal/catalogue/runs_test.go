package catalogue

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestRuns(t *testing.T) {
	d, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ctx := context.Background()
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	d.now = func() time.Time { return now }
	u, _ := d.CreateUser(ctx, "m@example.org", "M", false)
	st, err := d.CreateStream(ctx, NewStream{OwnerID: u.ID, Input: "https://s.example.org/a", SampleRate: 22050, Texts: StreamTexts{Name: "A"}, Probe: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	a, err := d.CreateAnalysis(ctx, u.ID, "x", "", []int64{st.ID}, nil)
	if err != nil {
		t.Fatal(err)
	}

	if in, _ := d.StreamInRun(ctx, st.ID); in {
		t.Fatal("stream in a run before any started")
	}
	if _, err := d.StartRun(ctx, a.ID, u.ID, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := d.StartRun(ctx, a.ID, u.ID, time.Time{}); !errors.Is(err, ErrRunActive) {
		t.Fatalf("second start: %v", err)
	}
	if in, _ := d.StreamInRun(ctx, st.ID); !in {
		t.Fatal("stream not in the run")
	}

	// Past its planned end the run is over, and EndRuns closes it.
	now = now.Add(2 * time.Hour)
	if in, _ := d.StreamInRun(ctx, st.ID); in {
		t.Fatal("stream still in a run after its end")
	}
	ids, err := d.EndRuns(ctx)
	if err != nil || len(ids) != 1 || ids[0] != a.ID {
		t.Fatalf("end runs: %v %v", ids, err)
	}
	runs, _ := d.Runs(ctx, a.ID)
	if len(runs) != 1 || runs[0].Stopped != runs[0].Ends || runs[0].StoppedBy != "" || runs[0].Number != 1 {
		t.Fatalf("ended run: %+v", runs)
	}

	// A run until stopped, stopped by hand.
	if _, err := d.StartRun(ctx, a.ID, u.ID, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if r, ok, _ := d.ActiveRun(ctx, a.ID); !ok || r.Number != 2 || r.Revision != 1 {
		t.Fatalf("active run: %v %+v", ok, r)
	}
	now = now.Add(time.Minute)
	if err := d.StopRun(ctx, a.ID, u.ID); err != nil {
		t.Fatal(err)
	}
	if err := d.StopRun(ctx, a.ID, u.ID); !errors.Is(err, ErrNoRun) {
		t.Fatalf("second stop: %v", err)
	}
	runs, _ = d.Runs(ctx, a.ID)
	if len(runs) != 2 || runs[0].StoppedBy != "M" || !runs[0].Stopped.Equal(now) {
		t.Fatalf("stopped run: %+v", runs[0])
	}
	entries, _ := d.AuditOf(ctx, "analysis", a.ID)
	n := 0
	for _, e := range entries {
		if e.Action == "run start" || e.Action == "run stop" || e.Action == "run end" {
			n++
		}
	}
	if n != 4 {
		t.Fatalf("run entries in the audit trail: %d", n)
	}
}

// Runs made before data sets get the columns; those already ended have no
// data set, one going on records.
func TestMigrateRuns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runs.db")
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	u, _ := d.CreateUser(ctx, "m@example.org", "M", false)
	a, _ := d.CreateAnalysis(ctx, u.ID, "x", "", nil, nil)
	b, _ := d.CreateAnalysis(ctx, u.ID, "y", "", nil, nil)
	d.StartRun(ctx, a.ID, u.ID, time.Time{})
	d.StopRun(ctx, a.ID, u.ID)
	d.StartRun(ctx, b.ID, u.ID, time.Time{})
	for _, c := range []string{"dataset", "dataset_bytes", "dataset_note", "summary"} {
		if _, err := d.db.Exec(`ALTER TABLE runs DROP COLUMN ` + c); err != nil {
			t.Fatal(err)
		}
	}
	d.Close()
	if d, err = Open(path); err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ra, _ := d.Runs(ctx, a.ID)
	rb, _ := d.Runs(ctx, b.ID)
	if ra[0].Dataset != DatasetNone || rb[0].Dataset != DatasetRecording {
		t.Fatalf("after the migration: %q %q", ra[0].Dataset, rb[0].Dataset)
	}
}

func TestDeleteRun(t *testing.T) {
	d, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ctx := context.Background()
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	d.now = func() time.Time { return now }
	u, _ := d.CreateUser(ctx, "m@example.org", "M", false)
	st, _ := d.CreateStream(ctx, NewStream{OwnerID: u.ID, Input: "https://s.example.org/a", SampleRate: 22050, Texts: StreamTexts{Name: "A"}, Probe: []byte(`{}`)})
	a, _ := d.CreateAnalysis(ctx, u.ID, "x", "", []int64{st.ID}, nil)
	var ids []int64
	for i := 0; i < 3; i++ {
		r, err := d.StartRun(ctx, a.ID, u.ID, time.Time{})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, r.ID)
		now = now.Add(time.Minute)
		if i < 2 {
			d.StopRun(ctx, a.ID, u.ID)
			d.SetDataset(ctx, r.ID, DatasetReady, 1000, "", nil)
		}
	}
	if err := d.DeleteRun(ctx, ids[2], u.ID); !errors.Is(err, ErrRunNotFinished) {
		t.Fatalf("deleted a running run: %v", err)
	}
	if err := d.DeleteRun(ctx, ids[0], u.ID); err != nil {
		t.Fatal(err)
	}
	runs, _ := d.Runs(ctx, a.ID)
	if len(runs) != 2 || runs[0].Number != 3 || runs[1].Number != 2 {
		t.Fatalf("after deleting run 1: %+v", runs)
	}
	if _, err := d.Run(ctx, ids[0]); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted run still found: %v", err)
	}
	if n, _ := d.DatasetBytesOf(ctx, u.ID); n != 1000 {
		t.Fatalf("data sets count %d bytes", n)
	}
	if err := d.DeleteRun(ctx, ids[0], u.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted twice: %v", err)
	}
}
