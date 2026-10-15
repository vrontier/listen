package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/vrontier/listen-workbench/internal/catalogue"
)

// A set put in the library by one member is copied by another into their
// own analysis, as its next revision; deleting the set leaves the copy.
func TestLibrary(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	ann, _ := r.db.CreateUser(ctx, "ann@example.org", "Ann", false)
	bob, _ := r.db.CreateUser(ctx, "bob@example.org", "Bob", false)
	sa, _ := r.db.CreateStream(ctx, catalogue.NewStream{OwnerID: ann.ID, Input: "https://s.example.org/a", SampleRate: 22050,
		Texts: catalogue.StreamTexts{Name: "A"}, Probe: []byte(`{}`)})
	sb, _ := r.db.CreateStream(ctx, catalogue.NewStream{OwnerID: bob.ID, Input: "https://s.example.org/b", SampleRate: 32000,
		Texts: catalogue.StreamTexts{Name: "B"}, Probe: []byte(`{}`)})
	rules := `{"rules":[{"kind":"comb","hz":50},{"kind":"line","hz":1000,"tol_hz":5}],"memory":{"mode":"disk"}}`
	aa, _ := r.db.CreateAnalysis(ctx, ann.ID, "Ann's", "", []int64{sa.ID}, map[int64]json.RawMessage{sa.ID: json.RawMessage(rules)})
	ab, _ := r.db.CreateAnalysis(ctx, bob.ID, "Bob's", "", []int64{sb.ID}, nil)

	annC := r.loginAs("ann@example.org")
	_, page, _ := r.get(annC, "/analyses/"+strconv.FormatInt(aa.ID, 10))
	if !strings.Contains(page, "Put these rules in the library") {
		t.Fatal("no way to put the rules in")
	}
	csrf := csrfOf(t, page)
	if code, _, _ := r.post(annC, "/library", url.Values{"csrf": {csrf}, "analysis": {fmt.Sprint(aa.ID)}, "source": {fmt.Sprint(sa.ID)},
		"name": {"Mains"}, "note": {"hum and a test tone"}}); code != http.StatusSeeOther {
		t.Fatalf("put in: %d", code)
	}
	// Bob cannot put in what is not his.
	bobC := r.loginAs("bob@example.org")
	_, page, _ = r.get(bobC, "/library")
	bcsrf := csrfOf(t, page)
	if code, _, _ := r.post(bobC, "/library", url.Values{"csrf": {bcsrf}, "analysis": {fmt.Sprint(aa.ID)}, "source": {fmt.Sprint(sa.ID)}, "name": {"x y"}}); code != http.StatusNotFound {
		t.Fatalf("put in someone else's: %d", code)
	}
	if !strings.Contains(page, "Mains") || !strings.Contains(page, "50 Hz and its harmonics") || !strings.Contains(page, "a line at 1000 Hz, within 5 Hz") {
		t.Fatal("set not listed in words")
	}
	sets, _ := r.db.LibrarySets(ctx)
	if len(sets) != 1 {
		t.Fatalf("sets: %d", len(sets))
	}
	set := strconv.FormatInt(sets[0].ID, 10)
	// Bob cannot delete it.
	r.post(bobC, "/library/"+set+"/delete", url.Values{"csrf": {bcsrf}})
	if sets, _ := r.db.LibrarySets(ctx); len(sets) != 1 {
		t.Fatal("deleted by someone else")
	}
	// Bob takes a copy into his analysis.
	_, _, hdr := r.post(bobC, "/library/"+set+"/take", url.Values{"csrf": {bcsrf}, "target": {fmt.Sprintf("%d:%d", ab.ID, sb.ID)}, "mode": {"add"}})
	got, _ := r.db.Analysis(ctx, ab.ID)
	if got.Revision != 2 || len(rulesOf(got.Sources[0].Spec)) != 2 {
		t.Fatalf("not taken: rev %d, %s", got.Revision, got.Sources[0].Spec)
	}
	if _, page, _ = r.get(bobC, hdr.Get("Location")); !strings.Contains(page, "made at 22050 Hz and this source runs at 32000 Hz") {
		t.Fatal("rate difference not mentioned")
	}
	// Taking it again adds nothing.
	r.post(bobC, "/library/"+set+"/take", url.Values{"csrf": {bcsrf}, "target": {fmt.Sprintf("%d:%d", ab.ID, sb.ID)}, "mode": {"add"}})
	if got, _ := r.db.Analysis(ctx, ab.ID); got.Revision != 2 {
		t.Fatalf("taken twice: rev %d", got.Revision)
	}
	// Not into Ann's analysis.
	if code, _, _ := r.post(bobC, "/library/"+set+"/take", url.Values{"csrf": {bcsrf}, "target": {fmt.Sprintf("%d:%d", aa.ID, sa.ID)}}); code != http.StatusNotFound {
		t.Fatalf("taken into someone else's: %d", code)
	}
	// Ann takes it out; Bob's copy stays.
	r.post(annC, "/library/"+set+"/delete", url.Values{"csrf": {csrf}})
	if sets, _ := r.db.LibrarySets(ctx); len(sets) != 0 {
		t.Fatal("not taken out")
	}
	if got, _ := r.db.Analysis(ctx, ab.ID); len(rulesOf(got.Sources[0].Spec)) != 2 {
		t.Fatal("copy lost")
	}
}
