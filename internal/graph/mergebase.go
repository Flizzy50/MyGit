package graph

import (
	"sort"

	"mygit/internal/object"
)

// MergeBases returns the best common ancestors of two commits.
//
// This is the question every merge begins with: given two divergent branches,
// what state were they both derived from? Without it, a merge could only
// compare two versions and would have no way to tell "you added this line" from
// "they deleted it" — the two look identical when all you see is the endpoints.
// The base supplies the missing third point of reference, which is precisely
// why the operation is called a three-way merge.
//
//	A ◀── B ◀── C        (main)
//	      ▲
//	      └─── D         (feature)
//
//	MergeBases(C, D) = {B}
//
// Note that B is the answer, not A. Both are common ancestors, but A is an
// ancestor *of* B, so using it would present changes made in B as if they were
// new on both sides. The wanted answer is the set of *lowest* common ancestors:
// common ancestors that are not themselves ancestors of any other common
// ancestor.
//
// # Why this is not the textbook LCA problem
//
// Classical lowest-common-ancestor algorithms assume a tree, where every node
// has one parent and the answer is unique. A commit graph is a DAG: merge
// commits have several parents, so two commits can have several incomparable
// lowest common ancestors. The criss-cross case is the standard example, where
// two branches were each merged into the other:
//
//	A     B          MergeBases(C, D) = {A, B}
//	│╲   ╱│          Neither A nor B is an ancestor of the other, so
//	│ ╲ ╱ │          neither can be discarded, and the merge genuinely
//	│  ╳  │          has two equally valid bases.
//	│ ╱ ╲ │
//	C     D
//
// Returning a set rather than a single commit is therefore forced by the data
// model, not a design preference. Real Git handles multiple bases by merging
// the bases together recursively to synthesize a virtual one — the "recursive"
// merge strategy that is Git's default. mygit reports the ambiguity and uses
// the first base, which is honest about the limitation rather than hiding it.
//
// # Algorithm
//
// Two traversals of the object database, O(V + E) overall:
//
//  1. Walk from each side, recording every commit reached and its parents.
//  2. Intersect the two sets: those are the common ancestors.
//  3. Discard any common ancestor that is a *proper* ancestor of another one.
//
// Step 3 is the subtle one. Doing it naively — testing every common ancestor
// against every other — costs O(|C|²) traversals. The trick is that a commit is
// non-minimal exactly when it is reachable from some common ancestor by at
// least one edge, so a single sweep seeded with the *parents* of every common
// ancestor marks all of them at once.
//
// That sweep needs no further reads: step 1 already loaded every commit
// involved, so the parent edges are cached and step 3 runs purely in memory.
// Caching the edges rather than re-reading them is what keeps the constant
// factor at two passes instead of four.
//
// Real Git instead paints flags downward from both tips and stops as soon as
// every commit still queued is known to be redundant, avoiding the full history
// walk entirely. That is faster on deep histories; this version is easier to
// verify correct, and both compute the same answer.
func MergeBases(store ObjectReader, a, b object.OID) ([]object.OID, error) {
	if a == b {
		return []object.OID{a}, nil
	}

	parentsOf := make(map[object.OID][]object.OID)

	reachableFromA, err := reachableSet(store, a, parentsOf)
	if err != nil {
		return nil, err
	}
	reachableFromB, err := reachableSet(store, b, parentsOf)
	if err != nil {
		return nil, err
	}

	common := make(map[object.OID]bool)
	for oid := range reachableFromA {
		if reachableFromB[oid] {
			common[oid] = true
		}
	}
	if len(common) == 0 {
		return nil, nil // unrelated histories
	}

	redundant := properAncestorsOf(common, parentsOf)

	var bases []object.OID
	for oid := range common {
		if !redundant[oid] {
			bases = append(bases, oid)
		}
	}

	// Sorting keeps the result deterministic despite map iteration order, which
	// matters because callers pick bases[0] when several exist.
	sort.Slice(bases, func(i, j int) bool { return bases[i].String() < bases[j].String() })
	return bases, nil
}

// reachableSet returns every commit reachable from start, inclusive, recording
// each commit's parents into the shared cache as it goes.
func reachableSet(store ObjectReader, start object.OID, parentsOf map[object.OID][]object.OID) (map[object.OID]bool, error) {
	walker, err := NewWalker(store, start)
	if err != nil {
		return nil, err
	}
	seen := make(map[object.OID]bool)
	for {
		commit, ok, err := walker.Next()
		if err != nil {
			return nil, err
		}
		if !ok {
			return seen, nil
		}
		seen[commit.OID] = true
		parentsOf[commit.OID] = commit.Parents
	}
}

// properAncestorsOf marks every commit reachable from the given set by at least
// one parent edge, using only cached edges.
//
// Seeding the sweep with parents rather than with the commits themselves is
// what makes the ancestry *proper*: a commit is not its own proper ancestor, so
// a lone common ancestor must not mark itself redundant.
func properAncestorsOf(commits map[object.OID]bool, parentsOf map[object.OID][]object.OID) map[object.OID]bool {
	var frontier []object.OID
	for oid := range commits {
		frontier = append(frontier, parentsOf[oid]...)
	}

	redundant := make(map[object.OID]bool)
	visited := make(map[object.OID]bool)

	for len(frontier) > 0 {
		oid := frontier[len(frontier)-1]
		frontier = frontier[:len(frontier)-1]

		if visited[oid] {
			continue
		}
		visited[oid] = true

		if commits[oid] {
			redundant[oid] = true
		}
		frontier = append(frontier, parentsOf[oid]...)
	}
	return redundant
}
