package merge

import (
	"strings"
	"testing"
)

func lines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func joined(ls []string) string { return strings.Join(ls, "\n") }

// TestMergeTextResolvesNonOverlappingEdits is the whole point of three-way
// merging: two people editing different parts of a file need no human help.
func TestMergeTextResolvesNonOverlappingEdits(t *testing.T) {
	base := lines("one\ntwo\nthree\nfour\nfive")
	ours := lines("ONE\ntwo\nthree\nfour\nfive")   // changed the first line
	theirs := lines("one\ntwo\nthree\nfour\nFIVE") // changed the last

	got, conflicts := MergeText(base, ours, theirs, "ours", "theirs")
	if conflicts != 0 {
		t.Fatalf("got %d conflicts, want none:\n%s", conflicts, joined(got))
	}
	if want := "ONE\ntwo\nthree\nfour\nFIVE"; joined(got) != want {
		t.Errorf("merged =\n%s\nwant\n%s", joined(got), want)
	}
}

// TestMergeTextOneSidedChanges covers the cases the base disambiguates. Without
// it, "ours has a line theirs lacks" could mean an addition or a deletion.
func TestMergeTextOneSidedChanges(t *testing.T) {
	cases := []struct {
		name               string
		base, ours, theirs string
		want               string
	}{
		{"only ours changed", "a\nb\nc", "a\nCHANGED\nc", "a\nb\nc", "a\nCHANGED\nc"},
		{"only theirs changed", "a\nb\nc", "a\nb\nc", "a\nCHANGED\nc", "a\nCHANGED\nc"},
		{"ours deleted a line", "a\nb\nc", "a\nc", "a\nb\nc", "a\nc"},
		{"theirs deleted a line", "a\nb\nc", "a\nb\nc", "a\nc", "a\nc"},
		{"ours appended", "a\nb", "a\nb\nc", "a\nb", "a\nb\nc"},
		{"theirs prepended", "a\nb", "a\nb", "z\na\nb", "z\na\nb"},
		{"neither changed", "a\nb", "a\nb", "a\nb", "a\nb"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, conflicts := MergeText(lines(tc.base), lines(tc.ours), lines(tc.theirs), "ours", "theirs")
			if conflicts != 0 {
				t.Fatalf("got %d conflicts, want none:\n%s", conflicts, joined(got))
			}
			if joined(got) != tc.want {
				t.Errorf("merged =\n%q\nwant\n%q", joined(got), tc.want)
			}
		})
	}
}

// TestIdenticalChangesDoNotConflict matters more than it looks: treating two
// people making the same edit as a conflict would make merges needlessly noisy,
// and it happens often when a fix is cherry-picked or independently rediscovered.
func TestIdenticalChangesDoNotConflict(t *testing.T) {
	base := lines("a\nold\nc")
	same := lines("a\nnew\nc")

	got, conflicts := MergeText(base, same, same, "ours", "theirs")
	if conflicts != 0 {
		t.Fatalf("identical edits produced %d conflicts:\n%s", conflicts, joined(got))
	}
	if joined(got) != "a\nnew\nc" {
		t.Errorf("merged = %q, want the change applied once", joined(got))
	}
}

// TestMergeTextConflict checks the markers and that all three versions survive
// into the output, since seeing the base is usually what makes the resolution
// obvious.
func TestMergeTextConflict(t *testing.T) {
	base := lines("a\noriginal\nc")
	ours := lines("a\nour version\nc")
	theirs := lines("a\ntheir version\nc")

	got, conflicts := MergeText(base, ours, theirs, "main", "feature")
	if conflicts != 1 {
		t.Fatalf("got %d conflicts, want 1:\n%s", conflicts, joined(got))
	}

	out := joined(got)
	for _, want := range []string{
		MarkerOurs + " main",
		"our version",
		MarkerBase + " base",
		"original",
		MarkerMiddle,
		"their version",
		MarkerTheirs + " feature",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	// The unchanged surrounding lines must survive outside the conflict.
	if !strings.HasPrefix(out, "a\n") || !strings.HasSuffix(out, "\nc") {
		t.Errorf("context lines were lost:\n%s", out)
	}
}

// TestConflictRegionsAreMinimal confirms only the genuinely contested region is
// marked, not the whole file.
func TestConflictRegionsAreMinimal(t *testing.T) {
	base := lines("1\n2\n3\n4\n5\n6\n7")
	ours := lines("1\n2\nOURS\n4\n5\n6\n7")
	theirs := lines("1\n2\nTHEIRS\n4\n5\n6\n7")

	got, conflicts := MergeText(base, ours, theirs, "ours", "theirs")
	if conflicts != 1 {
		t.Fatalf("got %d conflicts, want 1", conflicts)
	}

	out := joined(got)
	// Lines 4 through 7 are untouched by both sides and must appear once, after
	// the conflict block ends.
	if strings.Count(out, "\n4") != 1 || strings.Count(out, "\n7") != 1 {
		t.Errorf("unchanged lines were swept into the conflict:\n%s", out)
	}
	if !strings.HasSuffix(out, MarkerTheirs+" theirs\n4\n5\n6\n7") {
		t.Errorf("conflict region extends too far:\n%s", out)
	}
}

func TestMultipleSeparateConflicts(t *testing.T) {
	base := lines("a\nb\nc\nd\ne")
	ours := lines("A1\nb\nc\nd\nE1")
	theirs := lines("A2\nb\nc\nd\nE2")

	got, conflicts := MergeText(base, ours, theirs, "ours", "theirs")
	if conflicts != 2 {
		t.Fatalf("got %d conflicts, want 2:\n%s", conflicts, joined(got))
	}
	if n := strings.Count(joined(got), MarkerOurs); n != 2 {
		t.Errorf("found %d conflict blocks, want 2", n)
	}
}

func TestMergeTextEdgeCases(t *testing.T) {
	t.Run("empty base, both add the same", func(t *testing.T) {
		got, conflicts := MergeText(nil, lines("x"), lines("x"), "o", "t")
		if conflicts != 0 || joined(got) != "x" {
			t.Errorf("got %q with %d conflicts, want \"x\" and none", joined(got), conflicts)
		}
	})

	t.Run("empty base, both add differently", func(t *testing.T) {
		_, conflicts := MergeText(nil, lines("x"), lines("y"), "o", "t")
		if conflicts != 1 {
			t.Errorf("got %d conflicts, want 1", conflicts)
		}
	})

	t.Run("both delete everything", func(t *testing.T) {
		got, conflicts := MergeText(lines("a\nb"), nil, nil, "o", "t")
		if conflicts != 0 || len(got) != 0 {
			t.Errorf("got %v with %d conflicts, want empty and none", got, conflicts)
		}
	})

	t.Run("ours empties the file, theirs edits it", func(t *testing.T) {
		_, conflicts := MergeText(lines("a\nb"), nil, lines("a\nB"), "o", "t")
		if conflicts != 1 {
			t.Errorf("got %d conflicts, want 1", conflicts)
		}
	})

	t.Run("all empty", func(t *testing.T) {
		got, conflicts := MergeText(nil, nil, nil, "o", "t")
		if conflicts != 0 || len(got) != 0 {
			t.Errorf("got %v with %d conflicts", got, conflicts)
		}
	})
}

func TestLCSMatch(t *testing.T) {
	a := []string{"a", "b", "c", "d"}
	b := []string{"a", "x", "c", "d"}

	matches := lcsMatch(a, b)
	// a, c, and d are common; b/x are the changed pair.
	for _, i := range []int{0, 2, 3} {
		if _, ok := matches[i]; !ok {
			t.Errorf("index %d (%q) should have matched", i, a[i])
		}
	}
	if _, ok := matches[1]; ok {
		t.Errorf("index 1 (%q) should not have matched", a[1])
	}
}

func TestLCSMatchIsMonotonic(t *testing.T) {
	a := []string{"1", "2", "3", "4", "5", "6"}
	b := []string{"2", "9", "4", "6", "8"}

	matches := lcsMatch(a, b)
	prev := -1
	for i := 0; i < len(a); i++ {
		if j, ok := matches[i]; ok {
			if j <= prev {
				t.Fatalf("matches are not increasing: index %d maps to %d after %d", i, j, prev)
			}
			prev = j
			if a[i] != b[j] {
				t.Errorf("matched %q to %q", a[i], b[j])
			}
		}
	}
}
