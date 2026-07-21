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
	entries map[string]*Entry
}

// New returns an empty index, the state of a repository with nothing staged.
func New() *Index {
	return &Index{entries: make(map[string]*Entry)}
}

// Add stages an entry, replacing any existing entry for the same path.
//
// Re-staging a path is how an edit supersedes an earlier version: the map keeps
// exactly one entry per path, so the newest content wins. Because blobs are
// content-addressed, staging identical content twice writes no new object.
func (idx *Index) Add(e *Entry) { idx.entries[e.Path] = e }

// Remove unstages a path, reporting whether it was present. This is what a
// future `rm` builds on, and what `add` of a deleted directory would call.
func (idx *Index) Remove(path string) bool {
	_, ok := idx.entries[path]
	delete(idx.entries, path)
	return ok
}

// Get returns the entry for a path, if staged.
func (idx *Index) Get(path string) (*Entry, bool) {
	e, ok := idx.entries[path]
	return e, ok
}

// Len reports the number of staged entries.
func (idx *Index) Len() int { return len(idx.entries) }

// Entries returns all staged entries sorted by path, byte for byte.
//
// The sort order is not cosmetic. Git requires index and tree entries to be
// sorted by raw byte value of the path, because that ordering is part of what
// gets hashed into a tree object: two indexes with the same entries in
// different orders must produce the same tree, so a canonical order is
// mandatory. Sorting on plain Go string comparison gives byte order for the
// ASCII/UTF-8 paths mygit handles.
func (idx *Index) Entries() []*Entry {
	out := make([]*Entry, 0, len(idx.entries))
	for _, e := range idx.entries {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}
