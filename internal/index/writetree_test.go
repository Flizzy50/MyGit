package index

import (
	"testing"

	"mygit/internal/object"
)

// memStore is an in-memory ObjectWriter. Tree building needs somewhere to put
// objects but has no interest in files, so the one-method interface lets these
// tests run with no filesystem at all — the practical payoff of declaring the
// interface at the point of use.
type memStore struct {
	objects map[object.OID][]byte
	writes  int // counts every Put call, including no-op rewrites
}

func newMemStore() *memStore {
	return &memStore{objects: make(map[object.OID][]byte)}
}

func (m *memStore) Put(typ object.Type, payload []byte) (object.OID, error) {
	m.writes++
	oid := object.HashPayload(typ, payload)
	m.objects[oid] = payload
	return oid, nil
}

func (m *memStore) tree(t *testing.T, oid object.OID) object.Tree {
	t.Helper()
	payload, ok := m.objects[oid]
	if !ok {
		t.Fatalf("object %s was never written", oid)
	}
	tree, err := object.ParseTree(payload)
	if err != nil {
		t.Fatalf("parsing tree %s: %v", oid, err)
	}
	return tree
}

func stage(idx *Index, path string) {
	idx.Add(&Entry{
		Path: path,
		Mode: object.ModeRegular,
		OID:  object.HashPayload(object.TypeBlob, []byte(path)),
	})
}

// TestBuildTreeNestsDirectories is the core of Phase 5: a flat index of paths
// becomes a hierarchy of tree objects.
func TestBuildTreeNestsDirectories(t *testing.T) {
	idx := New()
	stage(idx, "README.md")
	stage(idx, "src/main.go")
	stage(idx, "src/util/helper.go")

	store := newMemStore()
	root, err := BuildTree(idx, store)
	if err != nil {
		t.Fatalf("BuildTree: %v", err)
	}

	// Root holds one blob and one subtree.
	rootTree := store.tree(t, root)
	if len(rootTree) != 2 {
		t.Fatalf("root has %d entries, want 2", len(rootTree))
	}
	if rootTree[0].Name != "README.md" || rootTree[0].IsTree() {
		t.Errorf("root[0] = %+v, want blob README.md", rootTree[0])
	}
	if rootTree[1].Name != "src" || !rootTree[1].IsTree() {
		t.Errorf("root[1] = %+v, want tree src", rootTree[1])
	}

	// src holds main.go and the util subtree.
	srcTree := store.tree(t, rootTree[1].OID)
	if len(srcTree) != 2 {
		t.Fatalf("src has %d entries, want 2", len(srcTree))
	}
	if srcTree[0].Name != "main.go" || srcTree[0].IsTree() {
		t.Errorf("src[0] = %+v, want blob main.go", srcTree[0])
	}
	if srcTree[1].Name != "util" || !srcTree[1].IsTree() {
		t.Errorf("src[1] = %+v, want tree util", srcTree[1])
	}

	// util holds the leaf, stored under its bare name, not its full path.
	utilTree := store.tree(t, srcTree[1].OID)
	if len(utilTree) != 1 || utilTree[0].Name != "helper.go" {
		t.Fatalf("util = %+v, want a single entry named helper.go", utilTree)
	}
}

// TestTreeEntriesStoreBareNames pins the reason trees exist: a tree entry holds
// one path component, and the full path is recovered by walking down from the
// root. Nothing in the object database ever stores "src/util/helper.go".
func TestTreeEntriesStoreBareNames(t *testing.T) {
	idx := New()
	stage(idx, "src/util/helper.go")

	store := newMemStore()
	if _, err := BuildTree(idx, store); err != nil {
		t.Fatalf("BuildTree: %v", err)
	}

	for oid, payload := range store.objects {
		if tree, err := object.ParseTree(payload); err == nil {
			for _, e := range tree {
				if len(e.Name) == 0 || containsSlash(e.Name) {
					t.Errorf("tree %s has entry %q; names must be single components", oid, e.Name)
				}
			}
		}
	}
}

func containsSlash(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == '/' {
			return true
		}
	}
	return false
}

// TestBuildTreeIsDeterministic proves the same index always yields the same
// root ID, despite the builder iterating Go maps in randomized order.
func TestBuildTreeIsDeterministic(t *testing.T) {
	build := func() object.OID {
		idx := New()
		for _, p := range []string{"z.txt", "a/b.txt", "a/c.txt", "m/n/o.txt", "b.txt"} {
			stage(idx, p)
		}
		root, err := BuildTree(idx, newMemStore())
		if err != nil {
			t.Fatalf("BuildTree: %v", err)
		}
		return root
	}

	first := build()
	for i := 0; i < 10; i++ {
		if got := build(); got != first {
			t.Fatalf("run %d produced %s, want %s — map iteration order leaked into the hash", i, got, first)
		}
	}
}

// TestUnchangedSubtreesAreReused is the Merkle DAG's structural-sharing
// property. Changing one file changes the trees along its path to the root and
// nothing else, so an untouched directory keeps its exact ID.
func TestUnchangedSubtreesAreReused(t *testing.T) {
	build := func(mutate func(*Index)) (object.OID, *memStore) {
		idx := New()
		stage(idx, "untouched/a.txt")
		stage(idx, "untouched/b.txt")
		stage(idx, "changing/c.txt")
		mutate(idx)

		store := newMemStore()
		root, err := BuildTree(idx, store)
		if err != nil {
			t.Fatalf("BuildTree: %v", err)
		}
		return root, store
	}

	rootA, storeA := build(func(*Index) {})
	rootB, storeB := build(func(idx *Index) {
		idx.Add(&Entry{
			Path: "changing/c.txt",
			Mode: object.ModeRegular,
			OID:  object.HashPayload(object.TypeBlob, []byte("edited content")),
		})
	})

	if rootA == rootB {
		t.Fatal("editing a file did not change the root tree")
	}

	subtreeID := func(store *memStore, root object.OID, name string) object.OID {
		for _, e := range store.tree(t, root) {
			if e.Name == name {
				return e.OID
			}
		}
		t.Fatalf("no subtree named %q", name)
		return object.OID{}
	}

	if a, b := subtreeID(storeA, rootA, "untouched"), subtreeID(storeB, rootB, "untouched"); a != b {
		t.Errorf("untouched subtree changed: %s then %s", a, b)
	}
	if a, b := subtreeID(storeA, rootA, "changing"), subtreeID(storeB, rootB, "changing"); a == b {
		t.Error("edited subtree kept the same id")
	}
}

// TestIdenticalDirectoriesShareOneTree shows deduplication working on
// structure, not just content: two directories with identical contents are one
// object.
func TestIdenticalDirectoriesShareOneTree(t *testing.T) {
	idx := New()
	idx.Add(&Entry{Path: "x/f.txt", Mode: object.ModeRegular, OID: object.HashPayload(object.TypeBlob, []byte("same"))})
	idx.Add(&Entry{Path: "y/f.txt", Mode: object.ModeRegular, OID: object.HashPayload(object.TypeBlob, []byte("same"))})

	store := newMemStore()
	root, err := BuildTree(idx, store)
	if err != nil {
		t.Fatalf("BuildTree: %v", err)
	}

	rootTree := store.tree(t, root)
	if len(rootTree) != 2 {
		t.Fatalf("root has %d entries, want 2", len(rootTree))
	}
	if rootTree[0].OID != rootTree[1].OID {
		t.Error("directories with identical contents produced different tree ids")
	}
	// Two Puts happened, but they collapsed to one stored object.
	if got := len(store.objects); got != 2 { // the shared subtree plus the root
		t.Errorf("stored %d distinct objects, want 2 (one shared subtree, one root)", got)
	}
}

func TestBuildEmptyIndex(t *testing.T) {
	root, err := BuildTree(New(), newMemStore())
	if err != nil {
		t.Fatalf("BuildTree: %v", err)
	}
	const emptyTree = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"
	if root.String() != emptyTree {
		t.Errorf("empty index produced %s, want the empty tree %s", root, emptyTree)
	}
}

// TestFileDirectoryConflictRejected covers the case where one name would have
// to be both a blob and a tree in the same directory.
func TestFileDirectoryConflictRejected(t *testing.T) {
	for _, order := range [][]string{
		{"src", "src/main.go"},
		{"src/main.go", "src"},
	} {
		idx := New()
		for _, p := range order {
			stage(idx, p)
		}
		if _, err := BuildTree(idx, newMemStore()); err == nil {
			t.Errorf("BuildTree(%v) succeeded, want a file/directory conflict error", order)
		}
	}
}

// TestDeepNesting checks the recursion holds up and produces one tree per
// directory level.
func TestDeepNesting(t *testing.T) {
	idx := New()
	stage(idx, "a/b/c/d/e/f/g/deep.txt")

	store := newMemStore()
	root, err := BuildTree(idx, store)
	if err != nil {
		t.Fatalf("BuildTree: %v", err)
	}

	depth := 0
	current := root
	for {
		tree := store.tree(t, current)
		if len(tree) != 1 {
			t.Fatalf("level %d has %d entries, want 1", depth, len(tree))
		}
		if !tree[0].IsTree() {
			if tree[0].Name != "deep.txt" {
				t.Errorf("leaf = %q, want deep.txt", tree[0].Name)
			}
			break
		}
		current = tree[0].OID
		depth++
	}
	if depth != 7 { // a,b,c,d,e,f,g
		t.Errorf("walked %d directory levels, want 7", depth)
	}
}
