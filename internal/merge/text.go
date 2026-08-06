// Package merge implements three-way merging, at both the file and tree level.
//
// The central insight is that comparing two versions is not enough. Given only
// "ours" and "theirs", a line present in one and absent in the other is
// ambiguous: one side added it, or the other side deleted it, and those demand
// opposite resolutions. A two-way merge cannot tell them apart and must ask a
// human about every difference.
//
// The merge base breaks the ambiguity by supplying a third point of reference —
// the state both sides started from. Then each difference has a direction:
//
//	base has line, ours has it, theirs lacks it   -> theirs deleted it
//	base lacks line, ours has it, theirs lacks it -> ours added it
//
// With that, a merge can resolve automatically everywhere exactly one side
// changed, and needs human judgment only where both sides changed the same
// region differently. That is the entire idea, and everything below is
// mechanism.
package merge

// lcsMatch pairs up lines that are common to both inputs, using a longest
// common subsequence.
//
// The result maps an index in a to the index in b it corresponds to, for lines
// the LCS considers unchanged. Everything not in the map is part of an
// insertion or deletion.
//
// This is the textbook dynamic program: O(n*m) time and space. Real diff tools
// use Myers' algorithm, which runs in O((n+m)·d) where d is the size of the
// edit script — dramatically better for the common case of two nearly identical
// files, because the work scales with how *different* the inputs are rather
// than with how large they are. The DP is used here because it is transparent
// and its correctness is obvious; the asymptotics are the honest cost.
func lcsMatch(a, b []string) map[int]int {
	n, m := len(a), len(b)

	// table[i][j] is the LCS length of a[i:] and b[j:]. Filling from the end
	// makes the backtrack below a simple forward walk.
	table := make([][]int, n+1)
	for i := range table {
		table[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				table[i][j] = table[i+1][j+1] + 1
			} else if table[i+1][j] >= table[i][j+1] {
				table[i][j] = table[i+1][j]
			} else {
				table[i][j] = table[i][j+1]
			}
		}
	}

	matches := make(map[int]int)
	for i, j := 0, 0; i < n && j < m; {
		switch {
		case a[i] == b[j]:
			matches[i] = j
			i++
			j++
		case table[i+1][j] >= table[i][j+1]:
			i++
		default:
			j++
		}
	}
	return matches
}

// Conflict markers, matching Git's so the output is familiar and so existing
// tooling can parse it.
const (
	MarkerOurs   = "<<<<<<<"
	MarkerBase   = "|||||||"
	MarkerMiddle = "======="
	MarkerTheirs = ">>>>>>>"
)

// MergeText performs a three-way merge of line slices and reports how many
// regions could not be resolved automatically.
//
// The algorithm is diff3. Both sides are aligned against the base, and the
// lines the base shares with *both* sides become synchronization points —
// anchors where all three versions provably agree. Between consecutive anchors
// lies a region that each side may have changed independently, and each region
// is resolved on its own:
//
//	ours == base    -> only theirs changed, take theirs
//	theirs == base  -> only ours changed, take ours
//	ours == theirs  -> both made the same change, take it once
//	otherwise       -> both changed it differently, emit a conflict
//
// The third case is worth noting: two people making the *identical* edit is not
// a conflict, and treating it as one would make merges far noisier than they
// need to be.
func MergeText(base, ours, theirs []string, ourLabel, theirLabel string) ([]string, int) {
	matchOurs := lcsMatch(base, ours)
	matchTheirs := lcsMatch(base, theirs)

	// Anchors are base lines that survived unchanged into both sides.
	var anchors []int
	for i := range base {
		if _, inOurs := matchOurs[i]; inOurs {
			if _, inTheirs := matchTheirs[i]; inTheirs {
				anchors = append(anchors, i)
			}
		}
	}

	var (
		out       []string
		conflicts int
		bPos      int
		oPos      int
		tPos      int
	)

	// A sentinel past the final anchor flushes the trailing region.
	for k := 0; k <= len(anchors); k++ {
		bEnd, oEnd, tEnd := len(base), len(ours), len(theirs)
		if k < len(anchors) {
			anchor := anchors[k]
			bEnd, oEnd, tEnd = anchor, matchOurs[anchor], matchTheirs[anchor]
		}

		region, conflicted := resolveRegion(
			base[bPos:bEnd], ours[oPos:oEnd], theirs[tPos:tEnd], ourLabel, theirLabel)
		out = append(out, region...)
		if conflicted {
			conflicts++
		}

		if k < len(anchors) {
			// The anchor line itself is identical in all three versions.
			out = append(out, base[anchors[k]])
			bPos, oPos, tPos = anchors[k]+1, matchOurs[anchors[k]]+1, matchTheirs[anchors[k]]+1
		}
	}
	return out, conflicts
}

// resolveRegion merges one span between anchors.
func resolveRegion(base, ours, theirs []string, ourLabel, theirLabel string) ([]string, bool) {
	switch {
	case equalLines(ours, theirs):
		// Includes the case where neither side changed anything.
		return ours, false
	case equalLines(ours, base):
		return theirs, false
	case equalLines(theirs, base):
		return ours, false
	}

	// Both sides changed the same region differently. The base is included
	// between ||||||| and ======= — Git calls this diff3 style — because seeing
	// what the text *was* is usually what makes the right resolution obvious.
	out := make([]string, 0, len(ours)+len(base)+len(theirs)+4)
	out = append(out, MarkerOurs+" "+ourLabel)
	out = append(out, ours...)
	out = append(out, MarkerBase+" base")
	out = append(out, base...)
	out = append(out, MarkerMiddle)
	out = append(out, theirs...)
	out = append(out, MarkerTheirs+" "+theirLabel)
	return out, true
}

func equalLines(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
