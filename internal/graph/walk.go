// Package graph traverses commit history.
//
// Every phase so far has *built* structure. This one reads it, and that turns
// out to be where the real algorithmic content of a version control system
// lives. Merge bases, "what is reachable from here", fetch negotiation, and
// log output are all the same traversal wearing different hats.
//
// The shape being walked is a directed acyclic graph. Edges point from a commit
// to its parents, so traversal always runs backwards through time:
//
//	A ◀── B ◀── C ◀── F        (main)
//	      ▲            │
//	      └── D ◀── E ─┘       (feature, merged into F)
//
// Acyclicity is guaranteed by construction rather than enforced by a check: a
// commit's ID is a hash of its parent IDs, so a cycle would require a commit to
// know its own hash before computing it. Content-addressing makes cycles
// unrepresentable, which is why nothing here needs cycle detection.
//
// Two properties of the graph drive the whole design of this package:
//
//   - It is a DAG, not a tree. A commit can be reached by many distinct paths,
//     so a naive recursive walk revisits shared history exponentially often.
//   - It has no global ordering. Parent edges give a partial order; "which
//     commit is newer" across two branches is a question the graph alone
//     cannot answer.
package graph

import (
	"container/heap"
	"fmt"

	"mygit/internal/object"
)

// ObjectReader is the slice of the object store this package needs. Declaring
// it here, one method wide, keeps graph independent of the concrete database —
// the same dependency-inversion move used for tree building.
type ObjectReader interface {
	Get(oid object.OID) (object.Type, []byte, error)
}

// Commit pairs a commit with its object ID.
//
// The ID is carried alongside rather than stored inside object.Commit because a
// content-addressed object cannot contain its own hash: adding the ID would
// change the bytes, which would change the ID. An object never knows its own
// name; the caller that looked it up does.
type Commit struct {
	OID object.OID
	*object.Commit
}

// Walker performs a single traversal of commit history.
//
// The algorithm is a best-first search over the DAG, driven by a priority queue
// ordered by committer date, newest first. Two design decisions matter, and
// both are worth understanding before reading the code.
//
// # Why a visited set is mandatory, not an optimization
//
// Consider history where a branch is repeatedly created and merged back:
//
//	A ◀── B ◀── D ◀── F
//	  ◀── C ◀───┘ ◀── E ◀──┘
//
// Each diamond doubles the number of distinct paths from the tip to the root.
// A recursive walk with no memory follows every path, so n diamonds cost
// O(2^n) commit loads. This is not a contrived case — it is exactly what a
// repository with a long-lived release branch looks like. Marking commits when
// they are *enqueued* collapses that to O(V + E): every commit is loaded once
// and every edge is traversed once.
//
// # Why a priority queue rather than a stack or a plain queue
//
// Depth-first search would walk one branch entirely to the root before showing
// anything from the other, so a merge would make recent work from one side
// appear far below ancient history from the other. Breadth-first fares no
// better, interleaving by graph distance, which has nothing to do with time.
//
// Ordering by committer date instead makes the traversal a k-way merge of
// already-sorted streams: each branch is internally newest-first, and the heap
// picks whichever branch currently holds the newest unvisited commit. That is
// what produces the reverse-chronological output people expect from log, and it
// costs O(V log V + E).
//
// The honest caveat is that this trusts commit timestamps, which are wall-clock
// readings from whatever machine authored the commit. Clock skew, deliberate
// rewriting, and rebases can all produce a child that appears older than its
// parent, and then date order violates topological order. Real Git offers
// --topo-order for callers that need the stronger guarantee; mygit does not,
// but the limitation is a property of the data, not of this implementation.
type Walker struct {
	store   ObjectReader
	pending commitQueue
	seen    map[object.OID]bool
}

// NewWalker begins a traversal from one or more starting commits.
//
// Multiple starts are supported because several later operations need them:
// finding a merge base walks from two tips at once, and fetch negotiation walks
// from every ref. The heap merges the streams correctly with no extra work.
func NewWalker(store ObjectReader, starts ...object.OID) (*Walker, error) {
	w := &Walker{store: store, seen: make(map[object.OID]bool)}
	for _, oid := range starts {
		if err := w.enqueue(oid); err != nil {
			return nil, err
		}
	}
	return w, nil
}

// Next returns the next commit in reverse-chronological order.
//
// The bool reports whether a commit was produced; false means the traversal is
// complete. Errors are surfaced separately from exhaustion so that a corrupt
// object is never silently reported as the end of history.
func (w *Walker) Next() (Commit, bool, error) {
	if w.pending.Len() == 0 {
		return Commit{}, false, nil
	}

	c := heap.Pop(&w.pending).(Commit)

	// Expand lazily: a commit's parents enter the queue only when the commit
	// itself is emitted. Combined with a caller that stops early — log -n 10 on
	// a repository with a million commits — this keeps work proportional to
	// output rather than to history size.
	for _, parent := range c.Parents {
		if err := w.enqueue(parent); err != nil {
			return Commit{}, false, fmt.Errorf("walking parents of %s: %w", c.OID, err)
		}
	}
	return c, true, nil
}

// enqueue loads a commit and adds it to the frontier, unless it has been seen.
//
// The seen check happens on enqueue rather than on emit, and the distinction is
// the entire defense against exponential blowup. Marking on emit would still
// let the same commit be pushed once per incoming edge, so a diamond-heavy
// history would fill the heap with duplicates even though each is only printed
// once.
func (w *Walker) enqueue(oid object.OID) error {
	if w.seen[oid] {
		return nil
	}
	w.seen[oid] = true

	typ, payload, err := w.store.Get(oid)
	if err != nil {
		return fmt.Errorf("reading commit %s: %w", oid, err)
	}
	if typ != object.TypeCommit {
		return fmt.Errorf("%s is a %s, not a commit", oid, typ)
	}
	commit, err := object.ParseCommit(payload)
	if err != nil {
		return fmt.Errorf("parsing commit %s: %w", oid, err)
	}

	heap.Push(&w.pending, Commit{OID: oid, Commit: commit})
	return nil
}

// Visited reports how many distinct commits the walk has loaded. Tests use it
// to assert that shared history is traversed once rather than once per path.
func (w *Walker) Visited() int { return len(w.seen) }

// commitQueue is a max-heap on committer date, implementing heap.Interface.
//
// Ties are broken by object ID so the traversal is fully deterministic. Without
// a tiebreak, commits sharing a timestamp — common in scripted history and in
// test suites — would emerge in whatever order the heap happened to arrange
// them, making output unstable across runs for no good reason.
type commitQueue []Commit

func (q commitQueue) Len() int { return len(q) }

func (q commitQueue) Less(i, j int) bool {
	ti, tj := q[i].Committer.When, q[j].Committer.When
	if !ti.Equal(tj) {
		return ti.After(tj) // newest first
	}
	return q[i].OID.String() > q[j].OID.String()
}

func (q commitQueue) Swap(i, j int) { q[i], q[j] = q[j], q[i] }

func (q *commitQueue) Push(x any) { *q = append(*q, x.(Commit)) }

func (q *commitQueue) Pop() any {
	old := *q
	n := len(old)
	item := old[n-1]
	*q = old[:n-1]
	return item
}
