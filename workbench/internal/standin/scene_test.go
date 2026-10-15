package standin

import "testing"

// Sources registered alike must still sound different.
func TestScenesDiffer(t *testing.T) {
	a := newStream(nil, "s-0001", "https://a.example/one", 44100).scene
	b := newStream(nil, "s-0002", "https://b.example/two", 44100).scene
	if a.wanderHz == b.wanderHz && a.whistleHz == b.whistleHz && a.levelDB == b.levelDB {
		t.Fatalf("two streams got the same scene: %+v", a)
	}
}
