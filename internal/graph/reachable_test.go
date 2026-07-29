package graph

import "testing"

func TestIsAncestorLinear(t *testing.T) {
	s := newMemStore()
	a := s.commit(t, "A", at(1))
	b := s.commit(t, "B", at(2), a)
	c := s.commit(t, "C", at(3), b)

	cases := []struct {
		name                 string
		ancestor, descendant [20]byte
		want                 bool
	}{
		{"grandparent", a, c, true},
		{"parent", b, c, true},
		{"self", c, c, true},
		{"reversed", c, a, false},
		{"child of self", c, b, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := IsAncestor(s, tc.ancestor, tc.descendant)
			if err != nil {
				t.Fatalf("IsAncestor: %v", err)
			}
			if got != tc.want {
				t.Errorf("IsAncestor = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestIsAncestorAcrossBranches is the question `branch -d` actually asks: are
// this branch's commits already reachable from where I am?
func TestIsAncestorAcrossBranches(t *testing.T) {
	s := newMemStore()
	base := s.commit(t, "base", at(1))
	mainTip := s.commit(t, "main work", at(2), base)
	featTip := s.commit(t, "feature work", at(3), base)

	// Siblings are ancestors of neither direction.
	if got, _ := IsAncestor(s, featTip, mainTip); got {
		t.Error("an unmerged feature tip was reported as an ancestor of main")
	}
	if got, _ := IsAncestor(s, mainTip, featTip); got {
		t.Error("main was reported as an ancestor of an unmerged feature")
	}
	// The shared base is an ancestor of both.
	for _, tip := range [][20]byte{mainTip, featTip} {
		if got, _ := IsAncestor(s, base, tip); !got {
			t.Error("the shared base should be an ancestor of both tips")
		}
	}

	// After merging, the feature tip becomes reachable and the branch is safe
	// to delete.
	merged := s.commit(t, "merge", at(4), mainTip, featTip)
	if got, _ := IsAncestor(s, featTip, merged); !got {
		t.Error("after merging, the feature tip should be an ancestor of the merge")
	}
}

// TestIsAncestorUnrelatedHistories covers grafted projects, where neither root
// reaches the other.
func TestIsAncestorUnrelatedHistories(t *testing.T) {
	s := newMemStore()
	rootA := s.commit(t, "rootA", at(1))
	rootB := s.commit(t, "rootB", at(2))

	if got, _ := IsAncestor(s, rootA, rootB); got {
		t.Error("unrelated roots should not be ancestors of one another")
	}
}

// TestIsAncestorNoExponentialBlowup confirms the reachability check inherits
// the Walker's visited set. A false answer must walk the whole history once,
// not once per path.
func TestIsAncestorNoExponentialBlowup(t *testing.T) {
	const diamonds = 25

	s := newMemStore()
	tip := s.commit(t, "root", at(0))
	for i := 0; i < diamonds; i++ {
		left := s.commit(t, "L", at(3*i+1), tip)
		right := s.commit(t, "R", at(3*i+2), tip)
		tip = s.commit(t, "M", at(3*i+3), left, right)
	}
	unrelated := s.commit(t, "unrelated", at(1))

	before := s.reads
	got, err := IsAncestor(s, unrelated, tip)
	if err != nil {
		t.Fatalf("IsAncestor: %v", err)
	}
	if got {
		t.Error("an unrelated commit was reported as an ancestor")
	}
	// 3*25+1 commits behind the tip; anything near that is linear, and a
	// path-following implementation would not finish this decade.
	if reads := s.reads - before; reads > 3*diamonds+2 {
		t.Errorf("performed %d reads, want at most %d", reads, 3*diamonds+2)
	}
}

// TestIsAncestorStopsEarly shows the common case is cheap: finding the target
// near the tip ends the walk immediately.
func TestIsAncestorStopsEarly(t *testing.T) {
	s := newMemStore()
	tip := s.commit(t, "c0", at(0))
	for i := 1; i < 500; i++ {
		tip = s.commit(t, "c", at(i), tip)
	}
	nearTip := tip
	tip = s.commit(t, "final", at(500), tip)

	before := s.reads
	if got, err := IsAncestor(s, nearTip, tip); err != nil || !got {
		t.Fatalf("IsAncestor = %v, %v", got, err)
	}
	if reads := s.reads - before; reads > 3 {
		t.Errorf("read %d objects to find an immediate parent; the walk did not stop early", reads)
	}
}
