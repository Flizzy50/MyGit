package graph

import (
	"fmt"
	"testing"
	"time"

	"mygit/internal/object"
)

// memStore is an in-memory object database that also counts reads, so tests can
// assert on how much work a traversal actually does — the thing this package
// exists to control.
type memStore struct {
	objects map[object.OID][]byte
	reads   int
}

func newMemStore() *memStore {
	return &memStore{objects: make(map[object.OID][]byte)}
}

func (m *memStore) Get(oid object.OID) (object.Type, []byte, error) {
	m.reads++
	payload, ok := m.objects[oid]
	if !ok {
		return "", nil, fmt.Errorf("object not found: %s", oid)
	}
	return object.TypeCommit, payload, nil
}

// commit stores a synthetic commit and returns its ID. The clock advances by
// one minute per commit so ordering is unambiguous.
func (m *memStore) commit(t *testing.T, msg string, when time.Time, parents ...object.OID) object.OID {
	t.Helper()
	sig := object.Signature{Name: "T", Email: "t@example.com", When: when}
	c := &object.Commit{
		Tree:      object.HashPayload(object.TypeTree, nil),
		Parents:   parents,
		Author:    sig,
		Committer: sig,
		Message:   msg,
	}
	payload := c.Serialize()
	oid := object.HashPayload(object.TypeCommit, payload)
	m.objects[oid] = payload
	return oid
}

func at(minute int) time.Time {
	return time.Unix(1700000000+int64(minute)*60, 0).In(time.UTC)
}

// walkAll drains a walker and returns the commit messages in order.
func walkAll(t *testing.T, w *Walker) []string {
	t.Helper()
	var got []string
	for {
		c, ok, err := w.Next()
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if !ok {
			return got
		}
		got = append(got, c.Summary())
	}
}

func TestLinearHistoryNewestFirst(t *testing.T) {
	s := newMemStore()
	a := s.commit(t, "A", at(1))
	b := s.commit(t, "B", at(2), a)
	c := s.commit(t, "C", at(3), b)

	w, err := NewWalker(s, c)
	if err != nil {
		t.Fatalf("NewWalker: %v", err)
	}

	got := walkAll(t, w)
	want := []string{"C", "B", "A"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("order = %v, want %v", got, want)
	}
}

// TestDiamondVisitsSharedAncestorOnce is the core correctness property. In
//
//	  A
//	 / \
//	B   C
//	 \ /
//	  D
//
// commit A is reachable by two paths, and must be emitted exactly once.
func TestDiamondVisitsSharedAncestorOnce(t *testing.T) {
	s := newMemStore()
	a := s.commit(t, "A", at(1))
	b := s.commit(t, "B", at(2), a)
	c := s.commit(t, "C", at(3), a)
	d := s.commit(t, "D", at(4), b, c)

	w, err := NewWalker(s, d)
	if err != nil {
		t.Fatalf("NewWalker: %v", err)
	}

	got := walkAll(t, w)
	if len(got) != 4 {
		t.Fatalf("emitted %d commits (%v), want 4 — a shared ancestor was duplicated", len(got), got)
	}
	// Newest first, and the merge base A comes last because it is oldest.
	want := []string{"D", "C", "B", "A"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("order = %v, want %v", got, want)
	}
}

// TestNoExponentialBlowup is the headline test of Phase 7.
//
// It builds a chain of n diamonds, a shape that arises naturally whenever a
// branch is repeatedly merged back. The number of distinct root-to-tip paths is
// 2^n, so a traversal without a visited set performs O(2^n) work. With one, the
// cost is O(V + E).
//
// With 30 diamonds there are over a billion paths but only 91 commits. If this
// test ever hangs instead of failing, the visited set has been removed.
func TestNoExponentialBlowup(t *testing.T) {
	const diamonds = 30

	s := newMemStore()
	tip := s.commit(t, "root", at(0))
	for i := 0; i < diamonds; i++ {
		left := s.commit(t, fmt.Sprintf("L%d", i), at(3*i+1), tip)
		right := s.commit(t, fmt.Sprintf("R%d", i), at(3*i+2), tip)
		tip = s.commit(t, fmt.Sprintf("M%d", i), at(3*i+3), left, right)
	}

	wantCommits := 3*diamonds + 1

	w, err := NewWalker(s, tip)
	if err != nil {
		t.Fatalf("NewWalker: %v", err)
	}
	got := walkAll(t, w)

	if len(got) != wantCommits {
		t.Fatalf("emitted %d commits, want %d", len(got), wantCommits)
	}
	// The decisive assertion: one object read per commit, not one per path.
	if s.reads != wantCommits {
		t.Errorf("performed %d object reads for %d commits; the visited set is not working", s.reads, wantCommits)
	}
	if w.Visited() != wantCommits {
		t.Errorf("Visited() = %d, want %d", w.Visited(), wantCommits)
	}
}

// TestOrderingIsByDateNotGraphShape shows why a priority queue is used rather
// than DFS. The long branch is old; the short branch is recent. Depth-first
// from the merge would bury the recent commits beneath the entire old branch.
func TestOrderingIsByDateNotGraphShape(t *testing.T) {
	s := newMemStore()
	base := s.commit(t, "base", at(0))

	// A long, old branch.
	old := base
	for i := 1; i <= 5; i++ {
		old = s.commit(t, fmt.Sprintf("old%d", i), at(i), old)
	}
	// A short, recent branch.
	recent := s.commit(t, "recent1", at(100), base)
	recent = s.commit(t, "recent2", at(101), recent)

	merge := s.commit(t, "merge", at(200), old, recent)

	w, err := NewWalker(s, merge)
	if err != nil {
		t.Fatalf("NewWalker: %v", err)
	}
	got := walkAll(t, w)

	want := []string{
		"merge", "recent2", "recent1",
		"old5", "old4", "old3", "old2", "old1",
		"base",
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("order = %v\nwant       %v", got, want)
	}
}

// TestMultipleStarts covers walking from several tips at once, which is what
// merge-base and fetch negotiation will need.
func TestMultipleStarts(t *testing.T) {
	s := newMemStore()
	base := s.commit(t, "base", at(1))
	mainTip := s.commit(t, "main", at(2), base)
	featTip := s.commit(t, "feature", at(3), base)

	w, err := NewWalker(s, mainTip, featTip)
	if err != nil {
		t.Fatalf("NewWalker: %v", err)
	}

	got := walkAll(t, w)
	want := []string{"feature", "main", "base"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("order = %v, want %v", got, want)
	}
	// base is shared by both starts and must still appear once.
	if s.reads != 3 {
		t.Errorf("read %d objects, want 3", s.reads)
	}
}

// TestLazyExpansion proves the walker does no work beyond what is consumed,
// which is what makes `log -n 5` cheap on a huge repository.
func TestLazyExpansion(t *testing.T) {
	s := newMemStore()
	tip := s.commit(t, "c0", at(0))
	for i := 1; i < 1000; i++ {
		tip = s.commit(t, fmt.Sprintf("c%d", i), at(i), tip)
	}

	before := s.reads
	w, err := NewWalker(s, tip)
	if err != nil {
		t.Fatalf("NewWalker: %v", err)
	}
	for i := 0; i < 5; i++ {
		if _, ok, err := w.Next(); err != nil || !ok {
			t.Fatalf("Next %d: ok=%v err=%v", i, ok, err)
		}
	}

	// Five emitted commits plus the one parent each queued: at most six loads,
	// not a thousand.
	if reads := s.reads - before; reads > 6 {
		t.Errorf("read %d objects to emit 5 commits; expansion is not lazy", reads)
	}
}

// TestDeterministicTieBreak guards against unstable output when commits share a
// timestamp, which happens constantly in scripted history and test suites.
func TestDeterministicTieBreak(t *testing.T) {
	build := func() []string {
		s := newMemStore()
		base := s.commit(t, "base", at(0))
		// Three siblings, all with the identical committer date.
		x := s.commit(t, "x", at(5), base)
		y := s.commit(t, "y", at(5), base)
		z := s.commit(t, "z", at(5), base)
		merge := s.commit(t, "merge", at(9), x, y, z)

		w, err := NewWalker(s, merge)
		if err != nil {
			t.Fatalf("NewWalker: %v", err)
		}
		return walkAll(t, w)
	}

	first := build()
	for i := 0; i < 20; i++ {
		if got := build(); fmt.Sprint(got) != fmt.Sprint(first) {
			t.Fatalf("run %d gave %v, first run gave %v — ordering is unstable", i, got, first)
		}
	}
}

// TestOctopusMerge checks that more than two parents traverse correctly.
func TestOctopusMerge(t *testing.T) {
	s := newMemStore()
	base := s.commit(t, "base", at(0))
	p1 := s.commit(t, "p1", at(1), base)
	p2 := s.commit(t, "p2", at(2), base)
	p3 := s.commit(t, "p3", at(3), base)
	octopus := s.commit(t, "octopus", at(4), p1, p2, p3)

	w, err := NewWalker(s, octopus)
	if err != nil {
		t.Fatalf("NewWalker: %v", err)
	}
	got := walkAll(t, w)
	want := []string{"octopus", "p3", "p2", "p1", "base"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("order = %v, want %v", got, want)
	}
}

// TestMultipleRoots covers histories grafted from unrelated projects, which
// have more than one parentless commit.
func TestMultipleRoots(t *testing.T) {
	s := newMemStore()
	rootA := s.commit(t, "rootA", at(1))
	rootB := s.commit(t, "rootB", at(2))
	merge := s.commit(t, "merge", at(3), rootA, rootB)

	w, err := NewWalker(s, merge)
	if err != nil {
		t.Fatalf("NewWalker: %v", err)
	}
	if got := walkAll(t, w); len(got) != 3 {
		t.Errorf("emitted %v, want all three commits", got)
	}
}

func TestWalkerErrors(t *testing.T) {
	s := newMemStore()

	t.Run("missing start", func(t *testing.T) {
		missing := object.HashPayload(object.TypeCommit, []byte("nope"))
		if _, err := NewWalker(s, missing); err == nil {
			t.Error("NewWalker accepted a missing commit")
		}
	})

	t.Run("missing parent", func(t *testing.T) {
		missing := object.HashPayload(object.TypeCommit, []byte("ghost"))
		orphan := s.commit(t, "orphan", at(1), missing)

		w, err := NewWalker(s, orphan)
		if err != nil {
			t.Fatalf("NewWalker: %v", err)
		}
		if _, _, err := w.Next(); err == nil {
			t.Error("Next accepted a commit whose parent is absent")
		}
	})

	t.Run("not a commit", func(t *testing.T) {
		blobStore := &wrongTypeStore{}
		oid := object.HashPayload(object.TypeBlob, []byte("x"))
		if _, err := NewWalker(blobStore, oid); err == nil {
			t.Error("NewWalker accepted a blob as a starting commit")
		}
	})
}

// wrongTypeStore always returns a blob, to exercise the type check.
type wrongTypeStore struct{}

func (wrongTypeStore) Get(object.OID) (object.Type, []byte, error) {
	return object.TypeBlob, []byte("not a commit"), nil
}

func TestEmptyWalk(t *testing.T) {
	w, err := NewWalker(newMemStore())
	if err != nil {
		t.Fatalf("NewWalker: %v", err)
	}
	if _, ok, err := w.Next(); ok || err != nil {
		t.Errorf("Next on an empty walk: ok=%v err=%v, want false and nil", ok, err)
	}
}
