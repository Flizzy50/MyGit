package graph

import "mygit/internal/object"

// IsAncestor reports whether ancestor is reachable from descendant by following
// parent pointers.
//
// Reachability is the single most important predicate in Git, and almost every
// user-facing question reduces to it:
//
//	"is this branch fully merged?"   -> is its tip an ancestor of the target?
//	"can this push fast-forward?"    -> is the remote tip an ancestor of mine?
//	"is this commit garbage?"        -> is it reachable from any ref at all?
//	"where did these branches split?" -> the merge base, in Phase 11
//
// The implementation is a plain traversal with early exit, reusing the Walker
// built for log. The date ordering is irrelevant here — any traversal order
// would be correct — but the Walker's visited set is not optional, for exactly
// the reason it was not optional for log: without it, a diamond-heavy history
// makes this exponential.
//
// Cost is O(V + E) worst case, when the answer is false and the entire history
// behind descendant must be examined. It is often far cheaper in practice,
// because a true answer stops the walk the moment the target is popped.
//
// A commit is considered its own ancestor, matching `git merge-base --is-
// ancestor`. That convention makes "is this branch merged?" answer correctly
// when the branch and the target point at the same commit.
func IsAncestor(store ObjectReader, ancestor, descendant object.OID) (bool, error) {
	if ancestor == descendant {
		return true, nil
	}

	walker, err := NewWalker(store, descendant)
	if err != nil {
		return false, err
	}
	for {
		commit, ok, err := walker.Next()
		if err != nil {
			return false, err
		}
		if !ok {
			return false, nil
		}
		if commit.OID == ancestor {
			return true, nil
		}
	}
}
