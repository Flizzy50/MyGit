package merge

import (
	"bytes"
	"fmt"
	"sort"
	"strings"

	"mygit/internal/object"
	"mygit/internal/worktree"
)

// ObjectStore is the slice of the object database this package needs: reading
// blobs to merge them, and writing the merged results back.
type ObjectStore interface {
	Get(oid object.OID) (object.Type, []byte, error)
	Put(typ object.Type, payload []byte) (object.OID, error)
}

// Conflict records a path the merge could not resolve on its own.
//
// All three versions are retained rather than just the fact of the conflict,
// because that is what allows resolution to happen later, by a person or a
// different tool, without recomputing anything.
type Conflict struct {
	Path   string
	Reason string
	Base   *worktree.Entry // nil when the path did not exist in the base
	Ours   *worktree.Entry // nil when deleted on our side
	Theirs *worktree.Entry // nil when deleted on their side
}

// Result is the outcome of merging two trees.
type Result struct {
	Tree      worktree.Tree // the merged file set, including conflict markers
	Conflicts []Conflict    // sorted by path; empty means a clean merge
}

// Clean reports whether the merge resolved completely.
func (r Result) Clean() bool { return len(r.Conflicts) == 0 }

// Trees performs a three-way merge of two trees against their common base.
//
// The work is per path, over the union of paths appearing in any of the three
// trees. For each one, the base answers the question a two-way comparison
// cannot: which side actually changed?
//
//	base   ours   theirs   outcome
//	────   ────   ──────   ───────
//	 X      X       X      unchanged, keep X
//	 X      Y       X      only ours changed, take ours
//	 X      X       Y      only theirs changed, take theirs
//	 X      Y       Y      both made the same change, take it once
//	 X      Y       Z      both changed differently -> merge content, or conflict
//	 -      Y       Y      both added the same file, no conflict
//	 -      Y       Z      both added different files -> conflict
//	 X      -       X      ours deleted, theirs untouched -> delete
//	 X      -       Z      ours deleted, theirs modified -> conflict
//
// The last row is the case people find surprising. Deletion versus
// modification cannot be resolved automatically, because the two sides express
// incompatible intentions about whether the file should exist at all, and no
// amount of content analysis settles it.
//
// Note how cheap the equality tests are: every comparison is between object
// IDs, so "did this file change?" costs 20 bytes regardless of file size, and
// an unchanged subtree of ten thousand files is dismissed by one comparison.
// The content merge only runs where IDs genuinely differ on both sides.
func Trees(store ObjectStore, base, ours, theirs worktree.Tree, ourLabel, theirLabel string) (Result, error) {
	result := Result{Tree: make(worktree.Tree)}

	for _, path := range unionPaths(base, ours, theirs) {
		b, hasBase := base[path]
		o, hasOurs := ours[path]
		t, hasTheirs := theirs[path]

		switch {
		// Both sides agree, including both having deleted the path.
		case hasOurs == hasTheirs && (!hasOurs || o == t):
			if hasOurs {
				result.Tree[path] = o
			}

		// The path did not exist in the base and only one side introduced it.
		// A new file on one branch is not a conflict with the other branch's
		// silence — the other side never had an opinion about it.
		case !hasBase && hasOurs && !hasTheirs:
			result.Tree[path] = o
		case !hasBase && !hasOurs && hasTheirs:
			result.Tree[path] = t

		// Only one side changed relative to the base: take the changed side.
		// This covers modification and deletion uniformly, which is the payoff
		// of having a base to compare against.
		case hasBase && hasOurs && b == o:
			if hasTheirs {
				result.Tree[path] = t
			}
		case hasBase && hasTheirs && b == t:
			if hasOurs {
				result.Tree[path] = o
			}

		// One side deleted while the other modified.
		case hasBase && !hasOurs:
			result.Conflicts = append(result.Conflicts, Conflict{
				Path: path, Reason: "deleted by us, modified by them",
				Base: entryPtr(base, path), Theirs: entryPtr(theirs, path),
			})
		case hasBase && !hasTheirs:
			result.Conflicts = append(result.Conflicts, Conflict{
				Path: path, Reason: "modified by us, deleted by them",
				Base: entryPtr(base, path), Ours: entryPtr(ours, path),
			})

		// Both sides changed the same path differently. Content merging is the
		// last chance to resolve without human help.
		default:
			merged, conflict, err := mergeContent(store, path, base, ours, theirs, ourLabel, theirLabel)
			if err != nil {
				return Result{}, err
			}
			result.Tree[path] = merged
			if conflict != nil {
				result.Conflicts = append(result.Conflicts, *conflict)
			}
		}
	}

	sort.Slice(result.Conflicts, func(i, j int) bool {
		return result.Conflicts[i].Path < result.Conflicts[j].Path
	})
	return result, nil
}

// mergeContent attempts a line-level merge of a path both sides changed.
//
// When the merge conflicts, the blob written to the tree is the *marked-up*
// text, not one of the two sides. That is deliberate: the working tree then
// contains a file showing both versions, which is what makes a conflict
// something a person can edit and resolve in place rather than an opaque
// failure.
func mergeContent(store ObjectStore, path string, base, ours, theirs worktree.Tree, ourLabel, theirLabel string) (worktree.Entry, *Conflict, error) {
	ourEntry, hasOurs := ours[path]
	theirEntry, hasTheirs := theirs[path]

	// Both sides added the same path with different content; there is no base
	// text to align against, so diff3 has nothing to work with.
	if !hasOurs || !hasTheirs {
		return worktree.Entry{}, nil, fmt.Errorf("internal error: mergeContent on absent path %q", path)
	}

	baseLines, err := blobLines(store, base, path)
	if err != nil {
		return worktree.Entry{}, nil, err
	}
	ourLines, err := blobLinesOf(store, ourEntry.OID)
	if err != nil {
		return worktree.Entry{}, nil, err
	}
	theirLines, err := blobLinesOf(store, theirEntry.OID)
	if err != nil {
		return worktree.Entry{}, nil, err
	}

	// Binary files cannot be merged line by line. Detecting them by scanning
	// for a NUL byte is the same heuristic Git uses: crude, but it never
	// mangles a binary file into unusable text.
	if isBinary(store, ourEntry.OID) || isBinary(store, theirEntry.OID) {
		return ourEntry, &Conflict{
			Path: path, Reason: "binary files differ",
			Base: entryPtr(base, path), Ours: &ourEntry, Theirs: &theirEntry,
		}, nil
	}

	mergedLines, conflicts := MergeText(baseLines, ourLines, theirLines, ourLabel, theirLabel)
	oid, err := store.Put(object.TypeBlob, []byte(strings.Join(mergedLines, "\n")))
	if err != nil {
		return worktree.Entry{}, nil, err
	}

	// A mode conflict is possible independently of content, when one side makes
	// a file executable and the other does not.
	mode := ourEntry.Mode
	if ourEntry.Mode != theirEntry.Mode {
		if b, ok := base[path]; ok && ourEntry.Mode == b.Mode {
			mode = theirEntry.Mode
		}
	}

	entry := worktree.Entry{Mode: mode, OID: oid}
	if conflicts == 0 {
		return entry, nil, nil
	}
	return entry, &Conflict{
		Path:   path,
		Reason: fmt.Sprintf("content conflict in %d region(s)", conflicts),
		Base:   entryPtr(base, path), Ours: &ourEntry, Theirs: &theirEntry,
	}, nil
}

// blobLines returns the lines of a path in a tree, or nothing if absent. An
// absent base is normal for an add/add conflict, and empty base lines make
// diff3 treat every line as added by both sides — which is exactly right.
func blobLines(store ObjectStore, tree worktree.Tree, path string) ([]string, error) {
	entry, ok := tree[path]
	if !ok {
		return nil, nil
	}
	return blobLinesOf(store, entry.OID)
}

func blobLinesOf(store ObjectStore, oid object.OID) ([]string, error) {
	typ, payload, err := store.Get(oid)
	if err != nil {
		return nil, err
	}
	if typ != object.TypeBlob {
		return nil, fmt.Errorf("%s is a %s, not a blob", oid, typ)
	}
	if len(payload) == 0 {
		return nil, nil
	}
	return strings.Split(string(payload), "\n"), nil
}

func isBinary(store ObjectStore, oid object.OID) bool {
	_, payload, err := store.Get(oid)
	if err != nil {
		return false
	}
	// Git only inspects the first 8000 bytes, on the reasoning that a file with
	// no NUL in its first 8 KB is text for practical purposes.
	if len(payload) > 8000 {
		payload = payload[:8000]
	}
	return bytes.IndexByte(payload, 0) >= 0
}

func entryPtr(tree worktree.Tree, path string) *worktree.Entry {
	if e, ok := tree[path]; ok {
		return &e
	}
	return nil
}

// unionPaths returns every path mentioned by any of the three trees, sorted so
// the merge is deterministic.
func unionPaths(trees ...worktree.Tree) []string {
	seen := make(map[string]bool)
	for _, tree := range trees {
		for path := range tree {
			seen[path] = true
		}
	}
	out := make([]string, 0, len(seen))
	for path := range seen {
		out = append(out, path)
	}
	sort.Strings(out)
	return out
}
