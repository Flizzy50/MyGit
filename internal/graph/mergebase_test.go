package graph

import (
	"fmt"
	"sort"
	"testing"

	"mygit/internal/object"
)

// baseNames resolves merge bases to their commit messages, which makes failures
// readable.
func baseNames(t *testing.T, s *memStore, bases []object.OID) []string {
	t.Helper()
	var out []string
	for _, oid := range bases {
		_, payload, err := s.Get(oid)
		if err != nil {
			t.Fatal(err)
		}
		c, err := object.ParseCommit(payload)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, c.Summary())
	}
	sort.Strings(out)
	return out
}

// TestMergeBaseIsLowestNotAny is the defining property. Both B and A are common
// ancestors of C and D, but only B is *lowest*. Choosing A would present the
// changes made in B as new on both sides, producing spurious conflicts in every
// file B touched.
//
//	A ◀── B ◀── C   (ours)
//	      ▲
//	      └──── D   (theirs)
func TestMergeBaseIsLowestNotAny(t *testing.T) {
	s := newMemStore()
	a := s.commit(t, "A", at(1))
	b := s.commit(t, "B", at(2), a)
	c := s.commit(t, "C", at(3), b)
	d := s.commit(t, "D", at(4), b)

	bases, err := MergeBases(s, c, d)
	if err != nil {
		t.Fatalf("MergeBases: %v", err)
	}
	if got := baseNames(t, s, bases); fmt.Sprint(got) != "[B]" {
		t.Errorf("merge base = %v, want [B] — A is a common ancestor but not the lowest", got)
	}
}

func TestMergeBaseLinearHistory(t *testing.T) {
	s := newMemStore()
	a := s.commit(t, "A", at(1))
	b := s.commit(t, "B", at(2), a)
	c := s.commit(t, "C", at(3), b)

	// When one commit is an ancestor of the other, it is itself the base. That
	// is precisely the fast-forward condition.
	bases, err := MergeBases(s, a, c)
	if err != nil {
		t.Fatalf("MergeBases: %v", err)
	}
	if got := baseNames(t, s, bases); fmt.Sprint(got) != "[A]" {
		t.Errorf("merge base = %v, want [A]", got)
	}

	// A commit merged with itself.
	bases, _ = MergeBases(s, c, c)
	if len(bases) != 1 || bases[0] != c {
		t.Errorf("MergeBases(c, c) = %v, want [c]", bases)
	}
}

// TestMergeBaseCrissCross is the case that forces the result to be a set.
// Neither A nor B is an ancestor of the other, so both are genuinely lowest
// common ancestors and no single commit is the right answer.
//
//	A     B
//	│╲   ╱│
//	│ ╲ ╱ │
//	│  ╳  │
//	│ ╱ ╲ │
//	C     D
func TestMergeBaseCrissCross(t *testing.T) {
	s := newMemStore()
	root := s.commit(t, "root", at(0))
	a := s.commit(t, "A", at(1), root)
	b := s.commit(t, "B", at(2), root)
	c := s.commit(t, "C", at(3), a, b)
	d := s.commit(t, "D", at(4), a, b)

	bases, err := MergeBases(s, c, d)
	if err != nil {
		t.Fatalf("MergeBases: %v", err)
	}
	got := baseNames(t, s, bases)
	if fmt.Sprint(got) != "[A B]" {
		t.Errorf("merge bases = %v, want [A B] — a criss-cross has two lowest common ancestors", got)
	}
	// root is a common ancestor but is an ancestor of both A and B, so it must
	// be filtered out as redundant.
	for _, name := range got {
		if name == "root" {
			t.Error("root was returned; it is a proper ancestor of A and B")
		}
	}
}

// TestMergeBaseAfterMerge covers the everyday shape: a feature branch merged
// once, then both sides continue.
func TestMergeBaseAfterMerge(t *testing.T) {
	s := newMemStore()
	base := s.commit(t, "base", at(0))
	ours1 := s.commit(t, "ours1", at(1), base)
	theirs1 := s.commit(t, "theirs1", at(2), base)
	merged := s.commit(t, "merged", at(3), ours1, theirs1)

	ours2 := s.commit(t, "ours2", at(4), merged)
	theirs2 := s.commit(t, "theirs2", at(5), merged)

	bases, err := MergeBases(s, ours2, theirs2)
	if err != nil {
		t.Fatalf("MergeBases: %v", err)
	}
	if got := baseNames(t, s, bases); fmt.Sprint(got) != "[merged]" {
		t.Errorf("merge base = %v, want [merged]", got)
	}
}

func TestMergeBaseUnrelatedHistories(t *testing.T) {
	s := newMemStore()
	rootA := s.commit(t, "rootA", at(1))
	rootB := s.commit(t, "rootB", at(2))

	bases, err := MergeBases(s, rootA, rootB)
	if err != nil {
		t.Fatalf("MergeBases: %v", err)
	}
	if len(bases) != 0 {
		t.Errorf("MergeBases on unrelated histories = %v, want none", baseNames(t, s, bases))
	}
}

// TestMergeBaseDeterministic guards against map iteration order leaking into
// the result, which matters because callers use bases[0].
func TestMergeBaseDeterministic(t *testing.T) {
	build := func() []string {
		s := newMemStore()
		root := s.commit(t, "root", at(0))
		a := s.commit(t, "A", at(1), root)
		b := s.commit(t, "B", at(2), root)
		c := s.commit(t, "C", at(3), a, b)
		d := s.commit(t, "D", at(4), a, b)
		bases, err := MergeBases(s, c, d)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, oid := range bases {
			out = append(out, oid.String())
		}
		return out
	}

	first := build()
	for i := 0; i < 20; i++ {
		if got := build(); fmt.Sprint(got) != fmt.Sprint(first) {
			t.Fatalf("run %d returned %v, first returned %v", i, got, first)
		}
	}
}

// TestMergeBaseIsLinear confirms the algorithm stays O(V+E) rather than
// degenerating into a traversal per common ancestor.
func TestMergeBaseIsLinear(t *testing.T) {
	const shared = 300

	s := newMemStore()
	tip := s.commit(t, "c0", at(0))
	for i := 1; i < shared; i++ {
		tip = s.commit(t, fmt.Sprintf("c%d", i), at(i), tip)
	}
	ours := s.commit(t, "ours", at(shared+1), tip)
	theirs := s.commit(t, "theirs", at(shared+2), tip)

	before := s.reads
	bases, err := MergeBases(s, ours, theirs)
	if err != nil {
		t.Fatalf("MergeBases: %v", err)
	}
	if len(bases) != 1 || bases[0] != tip {
		t.Fatalf("merge base = %v, want the shared tip", baseNames(t, s, bases))
	}

	// Two walks over roughly 300 shared commits, and the redundancy sweep adds
	// none because it reuses the cached parent edges. A quadratic
	// implementation — one traversal per common ancestor — would perform on the
	// order of 90,000 reads.
	if reads := s.reads - before; reads > 2*shared+4 {
		t.Errorf("performed %d reads for %d commits, want about %d; the sweep is re-reading objects",
			reads, shared, 2*shared)
	}
}

// TestMergeBaseWithDiamondHistory checks the algorithm survives the shape that
// breaks naive path-following implementations.
func TestMergeBaseWithDiamondHistory(t *testing.T) {
	const diamonds = 20

	s := newMemStore()
	tip := s.commit(t, "root", at(0))
	for i := 0; i < diamonds; i++ {
		l := s.commit(t, fmt.Sprintf("L%d", i), at(3*i+1), tip)
		r := s.commit(t, fmt.Sprintf("R%d", i), at(3*i+2), tip)
		tip = s.commit(t, fmt.Sprintf("M%d", i), at(3*i+3), l, r)
	}
	ours := s.commit(t, "ours", at(1000), tip)
	theirs := s.commit(t, "theirs", at(1001), tip)

	bases, err := MergeBases(s, ours, theirs)
	if err != nil {
		t.Fatalf("MergeBases: %v", err)
	}
	if len(bases) != 1 || bases[0] != tip {
		t.Errorf("merge base = %v, want the final merge commit", baseNames(t, s, bases))
	}
}
