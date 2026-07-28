package worktree

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"mygit/internal/index"
	"mygit/internal/object"
)

// memStore is an in-memory object database supporting both directions: storing
// blobs and trees, and reading them back for checkout. It satisfies both the
// ObjectReader this package needs and the ObjectWriter index.BuildTree needs,
// so tests can build a commit's trees and check them out with no filesystem
// object database involved.
type memStore struct {
	objects map[object.OID][]byte
	types   map[object.OID]object.Type
}

func newMemStore() *memStore {
	return &memStore{
		objects: make(map[object.OID][]byte),
		types:   make(map[object.OID]object.Type),
	}
}

func (m *memStore) Get(oid object.OID) (object.Type, []byte, error) {
	payload, ok := m.objects[oid]
	if !ok {
		return "", nil, fmt.Errorf("object not found: %s", oid)
	}
	return m.types[oid], payload, nil
}

func (m *memStore) Put(typ object.Type, payload []byte) (object.OID, error) {
	oid := object.HashPayload(typ, payload)
	m.objects[oid] = payload
	m.types[oid] = typ
	return oid, nil
}

func TestFlattenNestedTree(t *testing.T) {
	s := newMemStore()
	idx := index.New()
	for _, p := range []string{"README.md", "src/main.go", "src/util/helper.go"} {
		oid, _ := s.Put(object.TypeBlob, []byte("content of "+p))
		idx.Add(&index.Entry{Path: p, Mode: object.ModeRegular, OID: oid})
	}
	root, err := index.BuildTree(idx, s)
	if err != nil {
		t.Fatalf("BuildTree: %v", err)
	}

	flat, err := Flatten(s, root)
	if err != nil {
		t.Fatalf("Flatten: %v", err)
	}

	var got []string
	for p := range flat {
		got = append(got, p)
	}
	sort.Strings(got)
	want := []string{"README.md", "src/main.go", "src/util/helper.go"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("paths = %v, want %v", got, want)
	}
}

// TestFlattenIsInverseOfBuildTree pins the round trip: a flat index folded into
// nested trees and unfolded again must reproduce the original entries exactly.
func TestFlattenIsInverseOfBuildTree(t *testing.T) {
	s := newMemStore()
	idx := index.New()
	paths := []string{"a.txt", "d/e/f/deep.txt", "d/b.txt", "z.txt"}
	for _, p := range paths {
		oid, _ := s.Put(object.TypeBlob, []byte(p))
		idx.Add(&index.Entry{Path: p, Mode: object.ModeRegular, OID: oid})
	}

	root, err := index.BuildTree(idx, s)
	if err != nil {
		t.Fatalf("BuildTree: %v", err)
	}
	flat, err := Flatten(s, root)
	if err != nil {
		t.Fatalf("Flatten: %v", err)
	}

	if len(flat) != len(paths) {
		t.Fatalf("got %d paths, want %d", len(flat), len(paths))
	}
	for _, p := range paths {
		entry, ok := flat[p]
		if !ok {
			t.Errorf("missing %s", p)
			continue
		}
		staged, _ := idx.Get(p)
		if entry.OID != staged.OID {
			t.Errorf("%s: oid %s, want %s", p, entry.OID, staged.OID)
		}
	}
}

// TestBuildPlanSkipsUnchangedFiles is the content-addressing payoff: switching
// between commits touches only files whose IDs differ.
func TestBuildPlanSkipsUnchangedFiles(t *testing.T) {
	same := object.HashPayload(object.TypeBlob, []byte("unchanged"))
	oldOID := object.HashPayload(object.TypeBlob, []byte("old"))
	newOID := object.HashPayload(object.TypeBlob, []byte("new"))

	// Every path in current must exist on disk, or the staleness check below
	// would schedule extra writes.
	_, _, dir := setupWorkTree(t, map[string]string{
		"keep.txt":    "unchanged",
		"change.txt":  "old",
		"removed.txt": "old",
	})

	current := Tree{
		"keep.txt":    {Mode: object.ModeRegular, OID: same},
		"change.txt":  {Mode: object.ModeRegular, OID: oldOID},
		"removed.txt": {Mode: object.ModeRegular, OID: oldOID},
	}
	target := Tree{
		"keep.txt":   {Mode: object.ModeRegular, OID: same},
		"change.txt": {Mode: object.ModeRegular, OID: newOID},
		"added.txt":  {Mode: object.ModeRegular, OID: newOID},
	}

	plan := BuildPlan(dir, current, target)
	if fmt.Sprint(plan.Write) != fmt.Sprint([]string{"added.txt", "change.txt"}) {
		t.Errorf("Write = %v, want [added.txt change.txt]", plan.Write)
	}
	if fmt.Sprint(plan.Delete) != fmt.Sprint([]string{"removed.txt"}) {
		t.Errorf("Delete = %v, want [removed.txt]", plan.Delete)
	}
}

// TestBuildPlanRestoresMissingFile covers the staleness case: the index claims
// a file is already correct, but it is gone from disk. Real Git restores such a
// file, and a plan built from the index alone would not.
func TestBuildPlanRestoresMissingFile(t *testing.T) {
	oid := object.HashPayload(object.TypeBlob, []byte("content"))
	dir := t.TempDir() // nothing on disk at all

	same := Tree{"vanished.txt": {Mode: object.ModeRegular, OID: oid}}

	plan := BuildPlan(dir, same, same)
	if fmt.Sprint(plan.Write) != fmt.Sprint([]string{"vanished.txt"}) {
		t.Errorf("Write = %v, want [vanished.txt] — a deleted file was not restored", plan.Write)
	}
}

// TestBuildPlanDetectsModeChange covers a file whose content is identical but
// whose executable bit flipped: same blob, still needs rewriting.
func TestBuildPlanDetectsModeChange(t *testing.T) {
	oid := object.HashPayload(object.TypeBlob, []byte("#!/bin/sh\n"))
	_, _, dir := setupWorkTree(t, map[string]string{"run.sh": "#!/bin/sh\n"})

	current := Tree{"run.sh": {Mode: object.ModeRegular, OID: oid}}
	target := Tree{"run.sh": {Mode: object.ModeExecutable, OID: oid}}

	if plan := BuildPlan(dir, current, target); len(plan.Write) != 1 {
		t.Errorf("Write = %v, want [run.sh]", plan.Write)
	}
}

// setupWorkTree creates a temp work tree with the given files staged and on
// disk, returning the store, index, and directory.
func setupWorkTree(t *testing.T, files map[string]string) (*memStore, *index.Index, string) {
	t.Helper()
	dir := t.TempDir()
	s := newMemStore()
	idx := index.New()

	for path, content := range files {
		abs := filepath.Join(dir, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		oid, _ := s.Put(object.TypeBlob, []byte(content))
		info, err := os.Lstat(abs)
		if err != nil {
			t.Fatal(err)
		}
		mt := info.ModTime()
		idx.Add(&index.Entry{
			Path: path, Mode: object.ModeRegular, OID: oid,
			MtimeSec: uint32(mt.Unix()), MtimeNsec: uint32(mt.Nanosecond()),
			Size: uint32(info.Size()),
		})
	}
	return s, idx, dir
}

// TestValidateRejectsModifiedTrackedFile is the primary safety property:
// uncommitted edits must never be silently discarded.
func TestValidateRejectsModifiedTrackedFile(t *testing.T) {
	_, idx, dir := setupWorkTree(t, map[string]string{"f.txt": "committed"})

	// Simulate an uncommitted edit.
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("edited, never staged"), 0o644); err != nil {
		t.Fatal(err)
	}

	plan := Plan{Write: []string{"f.txt"}}
	conflicts := Validate(dir, plan, idx)
	if len(conflicts) != 1 {
		t.Fatalf("conflicts = %v, want one for f.txt", conflicts)
	}
	if !strings.Contains(conflicts[0].Reason, "local changes") {
		t.Errorf("reason = %q, want it to mention local changes", conflicts[0].Reason)
	}
}

// TestValidateRejectsUntrackedOverwrite covers the more dangerous case: a file
// that was never staged has never been hashed, so overwriting it is
// unrecoverable.
func TestValidateRejectsUntrackedOverwrite(t *testing.T) {
	_, idx, dir := setupWorkTree(t, nil)

	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("never staged"), 0o644); err != nil {
		t.Fatal(err)
	}

	plan := Plan{Write: []string{"new.txt"}}
	conflicts := Validate(dir, plan, idx)
	if len(conflicts) != 1 {
		t.Fatalf("conflicts = %v, want one for new.txt", conflicts)
	}
	if !strings.Contains(conflicts[0].Reason, "untracked") {
		t.Errorf("reason = %q, want it to mention untracked", conflicts[0].Reason)
	}
}

// TestValidateAllowsCleanFiles confirms the check is not simply paranoid: an
// unmodified tracked file may be overwritten freely, since its content is
// safely in the object database.
func TestValidateAllowsCleanFiles(t *testing.T) {
	_, idx, dir := setupWorkTree(t, map[string]string{"a.txt": "clean", "b.txt": "also clean"})

	plan := Plan{Write: []string{"a.txt"}, Delete: []string{"b.txt"}}
	if conflicts := Validate(dir, plan, idx); len(conflicts) != 0 {
		t.Errorf("clean files reported conflicts: %v", conflicts)
	}
}

// TestValidateToleratesTouchedFile shows why the hash confirms the stat cache:
// touching a file changes its mtime without changing content, and that must not
// be reported as a conflict.
func TestValidateToleratesTouchedFile(t *testing.T) {
	_, idx, dir := setupWorkTree(t, map[string]string{"f.txt": "same content"})

	// Rewrite identical content, which updates mtime and invalidates the cache.
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("same content"), 0o644); err != nil {
		t.Fatal(err)
	}

	if conflicts := Validate(dir, Plan{Write: []string{"f.txt"}}, idx); len(conflicts) != 0 {
		t.Errorf("a touched but unmodified file was reported as a conflict: %v", conflicts)
	}
}

// TestApplyWritesDeletesAndPrunes exercises the full mutation path, including
// the directory pruning Git needs because it cannot represent empty directories.
func TestApplyWritesDeletesAndPrunes(t *testing.T) {
	s, idx, dir := setupWorkTree(t, map[string]string{
		"keep.txt":       "keep",
		"old/gone.txt":   "delete me",
		"old/deep/x.txt": "delete me too",
	})

	newOID, _ := s.Put(object.TypeBlob, []byte("brand new"))
	keepOID, _ := idx.Get("keep.txt")
	target := Tree{
		"keep.txt":     {Mode: object.ModeRegular, OID: keepOID.OID},
		"new/file.txt": {Mode: object.ModeRegular, OID: newOID},
	}

	plan := BuildPlan(dir, FromIndex(idx), target)
	if err := Apply(s, dir, plan, target, idx); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if content, err := os.ReadFile(filepath.Join(dir, "new", "file.txt")); err != nil {
		t.Errorf("new file not written: %v", err)
	} else if string(content) != "brand new" {
		t.Errorf("new file = %q, want %q", content, "brand new")
	}

	// The emptied directory tree must be gone entirely, not left as shells.
	if _, err := os.Stat(filepath.Join(dir, "old")); !os.IsNotExist(err) {
		t.Error("empty directory 'old' was not pruned")
	}
	// The work tree root itself must survive pruning.
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("work tree root was removed: %v", err)
	}

	// The index must now describe the target exactly.
	if idx.Len() != 2 {
		t.Errorf("index has %d entries, want 2", idx.Len())
	}
	if _, ok := idx.Get("old/gone.txt"); ok {
		t.Error("deleted path is still in the index")
	}
	if _, ok := idx.Get("new/file.txt"); !ok {
		t.Error("written path is missing from the index")
	}
}

// TestApplyDeletesBeforeWriting covers the directory-to-file transition, which
// fails outright if writes run first.
func TestApplyDeletesBeforeWriting(t *testing.T) {
	s, idx, dir := setupWorkTree(t, map[string]string{"a/b.txt": "inside a directory"})

	fileOID, _ := s.Put(object.TypeBlob, []byte("now a file"))
	target := Tree{"a": {Mode: object.ModeRegular, OID: fileOID}}

	plan := BuildPlan(dir, FromIndex(idx), target)
	if err := Apply(s, dir, plan, target, idx); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	info, err := os.Lstat(filepath.Join(dir, "a"))
	if err != nil {
		t.Fatalf("path 'a' missing: %v", err)
	}
	if info.IsDir() {
		t.Error("'a' is still a directory; deletions did not run before writes")
	}
}

// TestApplyRefreshesStatCache confirms the index records post-write stat data,
// so a checked-out file does not immediately look modified.
func TestApplyRefreshesStatCache(t *testing.T) {
	s, idx, dir := setupWorkTree(t, nil)
	oid, _ := s.Put(object.TypeBlob, []byte("content"))
	target := Tree{"f.txt": {Mode: object.ModeRegular, OID: oid}}

	plan := BuildPlan(dir, FromIndex(idx), target)
	if err := Apply(s, dir, plan, target, idx); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	entry, ok := idx.Get("f.txt")
	if !ok {
		t.Fatal("f.txt missing from the index")
	}
	info, err := os.Lstat(filepath.Join(dir, "f.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !entry.MatchesStat(info) {
		t.Error("the freshly written file does not match its own index entry")
	}
}

func TestPruneStopsAtNonEmptyDirectory(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "a", "b", "c")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	// A sibling file keeps "a" alive.
	if err := os.WriteFile(filepath.Join(dir, "a", "sibling.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	pruneEmptyDirs(dir, nested)

	if _, err := os.Stat(filepath.Join(dir, "a", "b")); !os.IsNotExist(err) {
		t.Error("empty directory 'a/b' was not pruned")
	}
	if _, err := os.Stat(filepath.Join(dir, "a")); err != nil {
		t.Error("non-empty directory 'a' was wrongly pruned")
	}
}
