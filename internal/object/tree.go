package object

import (
	"bytes"
	"fmt"
	"sort"
)

// TreeEntry is one row of a directory listing: a name bound to a mode and an
// object ID.
//
// This is where filenames live. A blob holds only content and has no idea what
// it is called, so the binding of name to content exists exclusively here and
// in the index. Two consequences worth internalizing: identical files anywhere
// in the tree share one blob, and renaming a file writes no new blob at all —
// only a new tree with the name moved. Git's rename "detection" is exactly
// that, detection after the fact, because renames were never recorded.
type TreeEntry struct {
	Name string // a single path component, never containing '/'
	Mode Mode   // ModeTree for a subdirectory, otherwise a blob mode
	OID  OID    // the subtree or blob this name points at
}

// IsTree reports whether the entry points at a subdirectory.
func (e TreeEntry) IsTree() bool { return e.Mode.IsTree() }

// Type returns the object type this entry references, which is implied by the
// mode rather than stored separately.
func (e TreeEntry) Type() Type {
	if e.IsTree() {
		return TypeTree
	}
	return TypeBlob
}

// Tree is a directory: an ordered list of entries.
//
// A tree pointing at other trees is what turns the object database into a
// Merkle DAG. Because an entry stores a child's ID, and that ID is a hash of
// the child's full content, a parent's hash transitively covers everything
// beneath it. Change one byte of one file and its blob ID changes, which
// changes its directory's tree ID, which changes that directory's parent, all
// the way to the root — and to the commit. One root hash therefore fingerprints
// an entire source tree, which is precisely what makes comparing two commits,
// or two whole repositories, an O(1) hash comparison in the common case.
type Tree []TreeEntry

// sortKey returns the name Git actually orders by.
//
// This rule is subtle and is the usual reason a hand-built tree fails to match
// Git's hash. Entries are sorted by raw byte value, but a directory is compared
// as though its name ended in '/'. That is not the same as sorting the plain
// names, because '/' is 0x2F and several common filename characters sort below
// it — '.' is 0x2E, '-' is 0x2D.
//
// Concretely, given a file "src.txt" and a directory "src":
//
//	plain name sort:  "src" < "src.txt"      -> directory first
//	Git's tree sort:  "src/" vs "src.txt"    -> '/' (0x2F) vs '.' (0x2E)
//	                                         -> "src.txt" first
//
// Git orders trees this way so that a tree's entries appear in the same
// sequence as the corresponding flat, recursively expanded paths would — which
// keeps tree-to-tree diffing a linear merge of two sorted lists rather than a
// sort-and-compare. Get this wrong and every hash silently diverges from Git.
func (e TreeEntry) sortKey() string {
	if e.IsTree() {
		return e.Name + "/"
	}
	return e.Name
}

// Sort puts the tree into Git's canonical order.
//
// Canonical ordering is mandatory, not cosmetic: the serialized bytes are what
// gets hashed, so two trees with the same entries in different orders would
// otherwise receive different IDs and break deduplication outright.
func (t Tree) Sort() {
	sort.Slice(t, func(i, j int) bool { return t[i].sortKey() < t[j].sortKey() })
}

// Serialize renders the tree to its on-disk payload, sorting it first.
//
// Each entry is written as:
//
//	<octal mode> SP <name> NUL <20 raw bytes of object id>
//
// Two details differ from every other place mygit prints an ID. The mode has no
// leading zero — a directory is "40000", not "040000" — and the object ID is
// raw binary, not the 40-character hex used in output and file paths. Hex would
// double the size of every tree for no benefit, since trees are machine-read.
// The pretty-printed form that `cat-file -p` shows is a display convention
// layered on top; these bytes are the truth.
//
// Note there is no separator or count between entries: the NUL terminates the
// name and the ID is fixed-width, so the reader always knows exactly where the
// next entry begins.
func (t Tree) Serialize() []byte {
	t.Sort()

	var buf bytes.Buffer
	for _, e := range t {
		buf.WriteString(e.Mode.String())
		buf.WriteByte(' ')
		buf.WriteString(e.Name)
		buf.WriteByte(0)
		buf.Write(e.OID[:])
	}
	return buf.Bytes()
}

// ParseTree decodes a tree object's payload.
//
// The parse is a straight linear scan, O(payload size), because the format is
// self-delimiting: find the space, find the NUL, take the next twenty bytes,
// repeat.
func ParseTree(payload []byte) (Tree, error) {
	var tree Tree

	for pos := 0; pos < len(payload); {
		sp := bytes.IndexByte(payload[pos:], ' ')
		if sp < 0 {
			return nil, fmt.Errorf("malformed tree: no space after mode at offset %d", pos)
		}
		mode, err := ParseMode(string(payload[pos : pos+sp]))
		if err != nil {
			return nil, fmt.Errorf("malformed tree at offset %d: %w", pos, err)
		}
		pos += sp + 1

		nul := bytes.IndexByte(payload[pos:], 0)
		if nul < 0 {
			return nil, fmt.Errorf("malformed tree: name at offset %d is not NUL-terminated", pos)
		}
		name := string(payload[pos : pos+nul])
		pos += nul + 1

		if pos+OIDSize > len(payload) {
			return nil, fmt.Errorf("malformed tree: truncated object id for %q", name)
		}
		var oid OID
		copy(oid[:], payload[pos:pos+OIDSize])
		pos += OIDSize

		tree = append(tree, TreeEntry{Name: name, Mode: mode, OID: oid})
	}
	return tree, nil
}

// PrettyPrint renders a tree the way `git cat-file -p` does:
//
//	<mode padded to 6> <type> <hex id>\t<name>
//
// The padding is display-only. On disk a directory's mode is the five
// characters "40000"; Git pads it to "040000" here purely so the column lines
// up with the six-character "100644". Reproducing that quirk keeps mygit's
// output diffable against Git's.
func (t Tree) PrettyPrint() string {
	var buf bytes.Buffer
	for _, e := range t {
		fmt.Fprintf(&buf, "%06o %s %s\t%s\n", uint32(e.Mode), e.Type(), e.OID, e.Name)
	}
	return buf.String()
}
