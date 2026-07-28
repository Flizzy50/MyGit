// Package worktree materializes commits onto the filesystem.
//
// Every phase before this one moved data in one direction: read the working
// tree, hash it, store it. This package runs the pipeline backwards — objects
// out of the database and into files people can edit — and the reversal brings
// a class of problem that never arose while writing.
//
// Writing was always safe. Objects are immutable and content-addressed, so the
// worst a redundant write could do was recreate a byte-identical file. Checkout
// is the opposite: it overwrites and deletes real files, and a file the user
// edited but never committed exists in exactly one place on Earth. Losing it is
// unrecoverable, because it was never hashed and never stored.
//
// That asymmetry, not the tree walking, is what makes checkout difficult. The
// design here is therefore two-phase: compute the whole plan and validate it
// against the working tree first, then mutate only once nothing can be lost.
package worktree

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"mygit/internal/index"
	"mygit/internal/object"
)

// ObjectReader is the slice of the object store this package needs.
type ObjectReader interface {
	Get(oid object.OID) (object.Type, []byte, error)
}

// Entry is one file in a flattened tree.
type Entry struct {
	Mode object.Mode
	OID  object.OID
}

// Tree is a whole commit's file list, keyed by work-tree-relative slash path.
//
// This is the flat form again — the same shape as the index, and deliberately
// so. Trees are nested on disk because that gives structural sharing, but every
// operation that compares two commits wants to ask "what is at path p in each?"
// Flattening turns that into a map lookup instead of a walk, at the cost of
// giving up the O(1) subtree-equality shortcut. Both representations exist
// because each is right for a different question.
type Tree map[string]Entry

// Flatten expands a tree object and everything beneath it into a path map.
//
// This is the exact inverse of index.BuildTree: that folds a flat list into a
// hierarchy, this unfolds a hierarchy back into a flat list. Cost is O(entries)
// with recursion depth equal to directory nesting.
func Flatten(store ObjectReader, root object.OID) (Tree, error) {
	out := make(Tree)
	if err := flatten(store, root, "", out); err != nil {
		return nil, err
	}
	return out, nil
}

func flatten(store ObjectReader, oid object.OID, prefix string, out Tree) error {
	typ, payload, err := store.Get(oid)
	if err != nil {
		return fmt.Errorf("reading tree %s: %w", oid, err)
	}
	if typ != object.TypeTree {
		return fmt.Errorf("%s is a %s, not a tree", oid, typ)
	}
	entries, err := object.ParseTree(payload)
	if err != nil {
		return err
	}

	for _, e := range entries {
		path := e.Name
		if prefix != "" {
			path = prefix + "/" + e.Name
		}
		if e.IsTree() {
			if err := flatten(store, e.OID, path, out); err != nil {
				return err
			}
			continue
		}
		out[path] = Entry{Mode: e.Mode, OID: e.OID}
	}
	return nil
}

// FromIndex reads the staged state as a Tree, which is what the working tree is
// currently believed to contain.
func FromIndex(idx *index.Index) Tree {
	out := make(Tree, idx.Len())
	for _, e := range idx.Entries() {
		out[e.Path] = Entry{Mode: e.Mode, OID: e.OID}
	}
	return out
}

// Plan is the set of filesystem changes that will make the working tree match a
// target commit.
//
// Computing this before touching anything is the whole safety strategy. A plan
// can be validated, rejected, or reported without side effects, so the process
// can fail cleanly with the working tree untouched. Interleaving validation with
// mutation — the obvious way to write this — leaves a half-checked-out tree
// whenever the twentieth file turns out to be dirty.
type Plan struct {
	Write  []string // paths to create or overwrite, sorted
	Delete []string // paths to remove, sorted
}

// Empty reports whether the plan would change nothing.
func (p Plan) Empty() bool { return len(p.Write) == 0 && len(p.Delete) == 0 }

// BuildPlan computes the difference between the current and target trees.
//
// Note what does not appear in Write: paths whose object ID is unchanged. Two
// files with the same content have the same ID, so an unchanged file is skipped
// by a 20-byte comparison rather than by reading it. Switching between branches
// that differ in three files touches three files, no matter how large the
// repository — the same content-addressing dividend that made commit cheap.
//
// The exception is why workTree is a parameter at all. "current" comes from the
// index, and the index is a cache of what the working tree is *believed* to
// hold. A user who deletes a tracked file behind Git's back makes that belief
// wrong, and a plan derived from the index alone would skip the file as already
// correct and leave it missing. Checking that each supposedly-correct file
// actually exists makes checkout self-healing, which is what real Git does:
// deleting a tracked file and checking out restores it. The cost is one stat
// per unchanged path — the same order as status, and far cheaper than the
// hashing it avoids.
func BuildPlan(workTree string, current, target Tree) Plan {
	var plan Plan

	for path, want := range target {
		have, ok := current[path]
		switch {
		case !ok || have.OID != want.OID || have.Mode != want.Mode:
			plan.Write = append(plan.Write, path)
		case !fileExists(workTree, path):
			plan.Write = append(plan.Write, path)
		}
	}
	for path := range current {
		if _, ok := target[path]; !ok {
			plan.Delete = append(plan.Delete, path)
		}
	}

	// Sorting makes the operation deterministic, which matters for tests, for
	// reproducible error messages, and for predictable directory pruning.
	sort.Strings(plan.Write)
	sort.Strings(plan.Delete)
	return plan
}

// fileExists reports whether a work-tree-relative path is present on disk.
func fileExists(workTree, path string) bool {
	_, err := os.Lstat(filepath.Join(workTree, filepath.FromSlash(path)))
	return err == nil
}

// Conflict describes a path that cannot be changed without losing work.
type Conflict struct {
	Path   string
	Reason string
}

// Validate reports paths where applying the plan would destroy data.
//
// Two distinct hazards are checked, and conflating them is the usual bug:
//
//   - A tracked file has been modified since it was staged. Overwriting it
//     discards edits that exist nowhere else.
//   - An untracked file sits where the target commit wants to put one. It was
//     never staged, so it has never been hashed into the object database, and
//     overwriting it is unrecoverable.
//
// The second case is the one people forget, and it is the more dangerous of the
// two: at least a modified tracked file has an older version in history.
func Validate(workTree string, plan Plan, idx *index.Index) []Conflict {
	var conflicts []Conflict

	check := func(path string) {
		abs := filepath.Join(workTree, filepath.FromSlash(path))
		info, err := os.Lstat(abs)
		if err != nil {
			return // absent on disk: nothing there to lose
		}
		if info.IsDir() {
			conflicts = append(conflicts, Conflict{path, "a directory is in the way"})
			return
		}

		entry, tracked := idx.Get(path)
		if !tracked {
			conflicts = append(conflicts, Conflict{path,
				"untracked file would be overwritten"})
			return
		}
		if dirty, err := isDirty(abs, entry, info); err != nil || dirty {
			conflicts = append(conflicts, Conflict{path,
				"local changes would be overwritten"})
		}
	}

	for _, path := range plan.Write {
		check(path)
	}
	for _, path := range plan.Delete {
		check(path)
	}

	sort.Slice(conflicts, func(i, j int) bool { return conflicts[i].Path < conflicts[j].Path })
	return conflicts
}

// isDirty reports whether a working file differs from what the index recorded.
//
// The stat cache from Phase 4 earns its keep here. When size and mtime match
// the staged values the file is assumed unchanged and never read, so validating
// a checkout in a repository with 50,000 files costs 50,000 stat calls rather
// than hashing every byte on disk. Only files that look suspicious are read and
// hashed.
func isDirty(abs string, entry *index.Entry, info os.FileInfo) (bool, error) {
	if entry.MatchesStat(info) {
		return false, nil
	}
	content, err := os.ReadFile(abs)
	if err != nil {
		return true, err
	}
	// The stat cache can report a false positive — touching a file changes its
	// mtime without changing content — so confirm with the hash before calling
	// it dirty. Content is the authority; stat is only a hint.
	return object.HashPayload(object.TypeBlob, content) != entry.OID, nil
}

// Apply performs the plan and updates the index to match the target.
//
// Deletions run before writes, and the ordering is load-bearing. Consider a
// commit where the directory "a/" is replaced by a file named "a". Writing
// first fails, because a directory already occupies that name. Deleting first
// empties and prunes the directory, leaving the name free.
//
// The index is rewritten as part of the same operation because checkout moves
// all three trees at once. Leaving the index describing the old commit would
// make every staged file look modified, and the next commit would silently
// resurrect the previous snapshot.
func Apply(store ObjectReader, workTree string, plan Plan, target Tree, idx *index.Index) error {
	for _, path := range plan.Delete {
		abs := filepath.Join(workTree, filepath.FromSlash(path))
		if err := os.Remove(abs); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("removing %s: %w", path, err)
		}
		idx.Remove(path)
		pruneEmptyDirs(workTree, filepath.Dir(abs))
	}

	for _, path := range plan.Write {
		if err := writeFile(store, workTree, path, target[path], idx); err != nil {
			return err
		}
	}

	// Entries that were already correct on disk still need index entries, since
	// the index is being rebuilt to describe the target commit exactly.
	for path, entry := range target {
		if _, staged := idx.Get(path); !staged {
			if err := stageExisting(workTree, path, entry, idx); err != nil {
				return err
			}
		}
	}
	return nil
}

// writeFile materializes one blob and records it in the index.
func writeFile(store ObjectReader, workTree, path string, entry Entry, idx *index.Index) error {
	typ, content, err := store.Get(entry.OID)
	if err != nil {
		return fmt.Errorf("reading blob for %s: %w", path, err)
	}
	if typ != object.TypeBlob {
		return fmt.Errorf("%s is a %s, not a blob", path, typ)
	}

	abs := filepath.Join(workTree, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return fmt.Errorf("creating directory for %s: %w", path, err)
	}

	// Permissions come from the recorded mode, collapsed to executable or not.
	// On Windows the executable bit is not represented, so this is effectively
	// 0644 there — the same behavior real Git has on the platform.
	perm := os.FileMode(0o644)
	if entry.Mode == object.ModeExecutable {
		perm = 0o755
	}

	// A previously checked-out file may be read-only or otherwise unwritable,
	// so remove before writing rather than trying to truncate in place.
	if err := os.Remove(abs); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("replacing %s: %w", path, err)
	}
	if err := os.WriteFile(abs, content, perm); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}

	return stageExisting(workTree, path, entry, idx)
}

// stageExisting records a file in the index with fresh stat data.
//
// The stat values must be read after the write, not before, or the cache would
// describe a file that no longer exists in that form and every subsequent
// status check would treat it as modified.
func stageExisting(workTree, path string, entry Entry, idx *index.Index) error {
	abs := filepath.Join(workTree, filepath.FromSlash(path))
	info, err := os.Lstat(abs)
	if err != nil {
		return fmt.Errorf("stat %s: %w", path, err)
	}
	mt := info.ModTime()
	idx.Add(&index.Entry{
		Path:      path,
		Mode:      entry.Mode,
		OID:       entry.OID,
		MtimeSec:  uint32(mt.Unix()),
		MtimeNsec: uint32(mt.Nanosecond()),
		Size:      uint32(info.Size()),
	})
	return nil
}

// pruneEmptyDirs removes directories left empty by deletions, walking upward
// until it reaches a non-empty directory or the work tree root.
//
// This exists because Git has no concept of an empty directory: trees only ever
// contain blobs and other trees, so a directory whose last file was deleted
// simply has no representation in the target commit. Leaving the empty shell
// behind would make the working tree differ from the commit in a way status
// could not even describe.
func pruneEmptyDirs(workTree, dir string) {
	root := filepath.Clean(workTree)
	for {
		dir = filepath.Clean(dir)
		if dir == root || !strings.HasPrefix(dir, root) {
			return
		}
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) > 0 {
			return
		}
		if err := os.Remove(dir); err != nil {
			return
		}
		dir = filepath.Dir(dir)
	}
}

// FormatConflicts renders conflicts the way Git reports them.
func FormatConflicts(conflicts []Conflict) string {
	var b strings.Builder
	b.WriteString("your local changes would be overwritten by checkout:\n")
	for _, c := range conflicts {
		fmt.Fprintf(&b, "\t%s (%s)\n", c.Path, c.Reason)
	}
	b.WriteString("please commit your changes before switching")
	return b.String()
}
