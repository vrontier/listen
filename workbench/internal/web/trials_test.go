package web

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/vrontier/listen-workbench/internal/catalogue"
	"github.com/vrontier/listen-workbench/internal/engine"
)

// A trial runs changed settings next to a run without touching it; keeping
// it ends the run and saves them as the next revision.
func TestTrial(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	u, _ := r.db.CreateUser(ctx, "m@example.org", "M", false)
	st, _ := r.db.CreateStream(ctx, catalogue.NewStream{OwnerID: u.ID, Input: "https://s.example.org/a", SampleRate: 22050,
		Texts: catalogue.StreamTexts{Name: "A"}, Probe: []byte(`{}`)})
	a, _ := r.db.CreateAnalysis(ctx, u.ID, "x", "", []int64{st.ID}, nil)
	member := r.loginAs("m@example.org")
	aURL := "/analyses/" + strconv.FormatInt(a.ID, 10)
	_, page, _ := r.get(member, aURL)
	csrf := csrfOf(t, page)
	p := prefixOf(st.ID)
	form := url.Values{"csrf": {csrf}, p + "memory_mode": {"disk"}, p + "rule_count": {"1"}, p + "rule_kind_0": {"comb"}, p + "rule_hz_0": {"50"}}
	cl := engine.NewClient(&http.Client{}, r.engSrv.URL)
	tid := "t-" + a.EngineID()[2:]

	// No trial outside a run.
	r.post(member, aURL+"/trial", form)
	if _, err := cl.Analysis(ctx, st.EngineID(), tid); !engine.IsCode(err, "not_found") {
		t.Fatalf("trial outside a run: %v", err)
	}

	r.post(member, aURL+"/run", url.Values{"csrf": {csrf}, "length": {"1h"}})
	bad := url.Values{"csrf": {csrf}, p + "memory_mode": {"disk"}, p + "rule_count": {"1"}, p + "rule_kind_0": {"line"}, p + "rule_hz_0": {"1000"}, p + "rule_odd_0": {"on"}}
	if code, page, _ := r.post(member, aURL+"/trial", bad); code != 422 || !strings.Contains(page, "odd harmonics") {
		t.Fatalf("refused trial: %d", code)
	}
	if code, _, _ := r.post(member, aURL+"/trial", form); code != http.StatusSeeOther {
		t.Fatalf("trial: %d", code)
	}
	ea, err := cl.Analysis(ctx, st.EngineID(), tid)
	if err != nil || ea.Memory.Mode != "ram" || len(ea.Rules) != 1 {
		t.Fatalf("trial in the engine: %v %+v", err, ea)
	}
	if run, _ := cl.Analysis(ctx, st.EngineID(), a.EngineID()); len(run.Rules) != 0 || run.Label != "rev-1" {
		t.Fatalf("the run was touched: %+v", run)
	}
	_, page, _ = r.get(member, aURL)
	if !strings.Contains(page, "Trial on A") || !strings.Contains(page, "Keep: stop the run, save as revision 2") || !strings.Contains(page, `value="50"`) {
		t.Fatal("analysis page during a trial")
	}
	if code, _, _ := r.get(member, aURL+"/trial/data/"+strconv.FormatInt(st.ID, 10)+"/state"); code != 200 {
		t.Fatalf("trial state: %d", code)
	}

	// Discard, then try again and keep.
	r.post(member, aURL+"/trial/end", url.Values{"csrf": {csrf}})
	if _, err := cl.Analysis(ctx, st.EngineID(), tid); !engine.IsCode(err, "not_found") {
		t.Fatalf("discarded trial in the engine: %v", err)
	}
	r.post(member, aURL+"/trial", form)
	if code, _, _ := r.post(member, aURL+"/trial/keep", url.Values{"csrf": {csrf}}); code != http.StatusSeeOther {
		t.Fatalf("keep: %d", code)
	}
	r.srv.data.busy.Wait()
	if _, inRun, _ := r.db.ActiveRun(ctx, a.ID); inRun {
		t.Fatal("run goes on after keep")
	}
	if a, _ = r.db.Analysis(ctx, a.ID); a.Revision != 2 || !strings.Contains(string(a.Sources[0].Spec), `"hz":50`) {
		t.Fatalf("kept settings: rev %d %s", a.Revision, a.Sources[0].Spec)
	}
	if _, err := cl.Analysis(ctx, st.EngineID(), tid); !engine.IsCode(err, "not_found") {
		t.Fatalf("kept trial still in the engine: %v", err)
	}
	entries, _ := r.db.AuditOf(ctx, "analysis", a.ID)
	var actions []string
	for _, e := range entries {
		actions = append(actions, e.Action)
	}
	if got := strings.Join(actions, ","); !strings.Contains(got, "trial keep") || !strings.Contains(got, "trial discard") {
		t.Fatalf("audit: %s", got)
	}
}
