package catalogue

import (
	"context"
	"errors"
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
