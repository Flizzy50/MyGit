// Package index implements mygit's staging area: the index.
//
// The index is the single most misunderstood object in Git, usually described
// as "the list of files you're about to commit." That description is not wrong,
// but it hides the two facts that actually matter:
//
//  1. The index is a full, flat snapshot of a proposed next tree — every staged
//     path with its mode and blob ID — not a list of changes. Committing does
//     not diff anything; it serializes the index into tree objects verbatim.
//     This is where Git's snapshot model becomes concrete.
//
//  2. Each entry caches the working file's stat data (size and mtime). That
//     cache is what makes `status` cost O(number of files) stat calls instead
//     of O(total bytes) of hashing: if a file's size and mtime are unchanged,
//     Git trusts the cached blob ID without reading the file at all.
//
// Git keeps three trees in play at once, and the index is the middle one:
//
//	working tree            index (staging)          HEAD (last commit)
//	────────────            ───────────────          ──────────────────
//	what you edit     →     proposed next tree   →   the committed tree
//	  many versions           one version              one version
//	  as files on disk        cached + content-addr    as objects
//
//	add:    working tree ─▶ index      (hash the file, record the entry)
//	commit: index        ─▶ new commit (serialize entries into trees)
//	status: compare all three
//
// Without a staging area, commit would have to guess intent by diffing the
// working tree against HEAD every time. The index makes "what will be committed"
// an explicit, inspectable, materialized thing you compose deliberately.
package index

import (
	"os"
	"sort"

	"mygit/internal/object"
)

// Stage marks which side of an unresolved merge an entry belongs to.
//
// Ordinarily every entry is StageNormal and the index holds exactly one entry
// per path. During a conflicted merge that invariant is deliberately relaxed:
// the same path carries up to three entries at once, one per version, and the
// index becomes the record of an in-progress merge rather than a clean
// snapshot.
//
//	StageNormal (0)  the usual, unconflicted state
//	StageBase   (1)  the merge base: what both sides started from
//	StageOurs   (2)  the current branch's version
//	StageTheirs (3)  the incoming branch's version
//
// Storing all three is what lets a conflict be resolved later without redoing
// any work: the information needed to re-run the merge, or to run a different
// merge tool, is preserved. It is also how Git knows a merge is unfinished — a
// commit is refused while any entry sits at a nonzero stage.
type Stage uint16

const (
	StageNormal Stage = 0
	StageBase   Stage = 1
	StageOurs   Stage = 2
	StageTheirs Stage = 3
)

// Entry is one staged file.
//
// The stat fields are the cache. mygit stores only mtime and size, the two
// fields available portably through os.FileInfo. Real Git additionally records
// ctime, device, inode, uid, and gid — but on Windows those are all zero (you
// can see it in a raw `.git/index` dump), so they add nothing on this platform,
// and reproducing them elsewhere requires per-OS syscall code. The mtime+size
// pair already delivers the optimization the cache exists for; the extra fields
// only harden it against adversarial edge cases like a file being swapped for a
// different inode within the same timestamp.
type Entry struct {
	Path      string      // slash-separated, relative to the work tree root
	Stage     Stage       // StageNormal unless part of an unresolved merge
	Mode      object.Mode // ModeRegular, ModeExecutable, or ModeSymlink
	OID       object.OID  // blob ID of the staged content
	MtimeSec  uint32      // modification time, whole seconds
	MtimeNsec uint32      // modification time, nanosecond remainder
	Size      uint32      // file size, truncated to 32 bits as Git does
}

// MatchesStat reports whether fi looks identical to what was staged, so the
// cached OID can be trusted without re-hashing the file. This is the fast path
// that status walks for every tracked file.
//
// It is deliberately a stat comparison, never a content comparison — reading
// the content would defeat the entire purpose. The known weakness is "racy
// Git": a file modified within the same second it was staged can share the
// recorded mtime and slip through. Real Git closes that gap by also comparing
// against the index file's own mtime and re-hashing anything suspicious; mygit
// notes the hazard and leaves the hardening for later.
func (e *Entry) MatchesStat(fi os.FileInfo) bool {
	mt := fi.ModTime()
	return e.Size == uint32(fi.Size()) &&
		e.MtimeSec == uint32(mt.Unix()) &&
		e.MtimeNsec == uint32(mt.Nanosecond())
}

// Index is an in-memory staging area: a set of entries keyed by path.
//
// The backing store is a map for O(1) staging and unstaging, but every path
// that leaves this package — serialization, tree building, status output — goes
// through Entries, which returns them sorted. Git instead keeps a permanently
// sorted array and binary-searches inserts (O(n) to shift on each add); a map
// plus sort-on-read trades that for simpler mutation at the cost of an
// O(n log n) sort per read, which is negligible next to the file I/O around it.
type Index struct {
	entries map[key]*Entry
}

// key identifies an entry. Including the stage is what permits three entries
// for one path during a conflicted merge while still keeping O(1) lookup.
type key struct {
	path  string
	stage Stage
}

// New returns an empty index, the state of a repository with nothing staged.
func New() *Index {
	return &Index{entries: make(map[key]*Entry)}
}

// Add stages an entry, replacing any existing entry for the same path and
// stage.
//
// Re-staging a path is how an edit supersedes an earlier version: the map keeps
// exactly one entry per (path, stage), so the newest content wins. Because
// blobs are content-addressed, staging identical content twice writes no new
// object.
func (idx *Index) Add(e *Entry) { idx.entries[key{e.Path, e.Stage}] = e }

// Remove unstages a path at every stage, reporting whether anything was
// present.
//
// Clearing all stages together is what "resolving" a conflict means: the three
// competing versions are replaced by a single stage-zero entry, and leaving any
// of them behind would keep the merge looking unfinished.
func (idx *Index) Remove(path string) bool {
	found := false
	for _, stage := range []Stage{StageNormal, StageBase, StageOurs, StageTheirs} {
		if _, ok := idx.entries[key{path, stage}]; ok {
			delete(idx.entries, key{path, stage})
			found = true
		}
	}
	return found
}

// Get returns the ordinary, unconflicted entry for a path.
func (idx *Index) Get(path string) (*Entry, bool) {
	e, ok := idx.entries[key{path, StageNormal}]
	return e, ok
}

// GetStage returns the entry for a path at a specific merge stage.
func (idx *Index) GetStage(path string, stage Stage) (*Entry, bool) {
	e, ok := idx.entries[key{path, stage}]
	return e, ok
}

// Len reports the number of entries, counting each stage separately.
func (idx *Index) Len() int { return len(idx.entries) }

// HasConflicts reports whether any entry sits at a nonzero stage, meaning a
// merge is still unresolved. Commit consults this and refuses.
func (idx *Index) HasConflicts() bool {
	for k := range idx.entries {
		if k.stage != StageNormal {
			return true
		}
	}
	return false
}

// ConflictedPaths returns the sorted paths that still have unresolved stages.
func (idx *Index) ConflictedPaths() []string {
	seen := make(map[string]bool)
	for k := range idx.entries {
		if k.stage != StageNormal {
			seen[k.path] = true
		}
	}
	out := make([]string, 0, len(seen))
	for path := range seen {
		out = append(out, path)
	}
	sort.Strings(out)
	return out
}

// Entries returns all entries sorted by path, then by stage.
//
// The sort order is not cosmetic. Git requires index and tree entries to be
// sorted by raw byte value of the path, because that ordering is part of what
// gets hashed into a tree object: two indexes with the same entries in
// different orders must produce the same tree, so a canonical order is
// mandatory. Sorting on plain Go string comparison gives byte order for the
// ASCII/UTF-8 paths mygit handles. Stage breaks ties so conflicted entries
// appear base, ours, theirs — the order Git prints them in.
func (idx *Index) Entries() []*Entry {
	out := make([]*Entry, 0, len(idx.entries))
	for _, e := range idx.entries {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].Stage < out[j].Stage
	})
	return out
}
