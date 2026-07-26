package index

import (
	"fmt"
	"strings"

	"mygit/internal/object"
)

// ObjectWriter is the slice of the object store that tree building needs.
//
// Declaring the interface here, at the point of use, rather than importing
// store directly, keeps the dependency arrow pointing the right way: index
// depends on an idea it defines, not on a concrete database. Tests can build
// trees against a map with no filesystem involved, and a future packfile writer
// satisfies this without index changing at all. This is the standard Go idiom —
// accept interfaces, and keep them one method wide.
type ObjectWriter interface {
	Put(typ object.Type, payload []byte) (object.OID, error)
}

// BuildTree converts the flat index into a hierarchy of tree objects and
// returns the ID of the root tree.
//
// This is the structural heart of Git. The index is deliberately flat — a list
// of full paths like "src/util/helper.go" — because that makes staging and stat
// comparison simple. But a commit points at a *tree*, and trees are nested. So
// somewhere the flat list has to be reassembled into a hierarchy, and this is
// that somewhere:
//
//	index (flat)                    trees (nested)
//	────────────                    ──────────────
//	README.md                       root
//	src/main.go            ───▶     ├── blob README.md
//	src/util/helper.go              └── tree src
//	                                    ├── blob main.go
//	                                    └── tree util
//	                                        └── blob helper.go
//
// The work happens in two passes. First, insert every path into an in-memory
// trie keyed by path component, which reconstructs the directory structure in
// O(total path components). Second, walk that trie in post-order — children
// before parents — because a parent tree must embed its children's object IDs,
// and those IDs do not exist until the children have been serialized and
// stored. That ordering constraint is not an implementation detail; it is
// forced by content-addressing itself. You cannot name a parent until every
// descendant is named, which is exactly why a Merkle DAG is always built from
// the leaves up.
func BuildTree(idx *Index, w ObjectWriter) (object.OID, error) {
	root := newNode()
	for _, e := range idx.Entries() {
		if err := root.insert(strings.Split(e.Path, "/"), e); err != nil {
			return object.OID{}, err
		}
	}
	return root.write(w)
}

// node is one directory while the hierarchy is being reconstructed.
//
// Files and subdirectories are kept apart because they become tree entries in
// different ways: a file's OID is already known from the index, while a
// subdirectory's OID only exists once it has been written.
type node struct {
	files   map[string]*Entry
	subdirs map[string]*node
}

func newNode() *node {
	return &node{files: make(map[string]*Entry), subdirs: make(map[string]*node)}
}

// insert threads one index entry down into the trie, creating directories as
// needed. parts is the entry's path split on '/'.
func (n *node) insert(parts []string, e *Entry) error {
	name := parts[0]

	if len(parts) == 1 {
		// A leaf. A name cannot be both a file and a directory in one tree, so
		// reject the collision rather than silently dropping one of them. Real
		// Git enforces the same rule; on a case-insensitive filesystem this is
		// also where "README" versus "readme" trouble would surface.
		if _, clash := n.subdirs[name]; clash {
			return fmt.Errorf("cannot build tree: %q is both a file and a directory", e.Path)
		}
		n.files[name] = e
		return nil
	}

	if _, clash := n.files[name]; clash {
		return fmt.Errorf("cannot build tree: %q is both a file and a directory", name)
	}
	sub, ok := n.subdirs[name]
	if !ok {
		sub = newNode()
		n.subdirs[name] = sub
	}
	return sub.insert(parts[1:], e)
}

// write serializes this directory and everything under it, returning the ID of
// the tree object it produced.
//
// The recursion is post-order by necessity, as described on BuildTree: each
// subdirectory is written first so that its ID is available to embed here.
func (n *node) write(w ObjectWriter) (object.OID, error) {
	tree := make(object.Tree, 0, len(n.files)+len(n.subdirs))

	for name, e := range n.files {
		tree = append(tree, object.TreeEntry{Name: name, Mode: e.Mode, OID: e.OID})
	}

	for name, sub := range n.subdirs {
		oid, err := sub.write(w) // children first
		if err != nil {
			return object.OID{}, err
		}
		tree = append(tree, object.TreeEntry{Name: name, Mode: object.ModeTree, OID: oid})
	}

	// Serialize sorts into Git's canonical order, so the map iteration above
	// being randomized is harmless — the bytes, and therefore the ID, are
	// deterministic.
	//
	// Storing here is what makes an unchanged subdirectory free: its bytes are
	// identical to last time, so its ID is identical, so Put is a no-op and the
	// parent simply points at the object that already exists. A commit touching
	// one file in a deep repository writes only the trees along that one path.
	return w.Put(object.TypeTree, tree.Serialize())
}
