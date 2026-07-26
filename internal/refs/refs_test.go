package refs

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"mygit/internal/object"
)

func newStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "refs", "heads"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, HeadFile), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatalf("writing HEAD: %v", err)
	}
	return New(dir), dir
}

func oid(s string) object.OID { return object.HashPayload(object.TypeCommit, []byte(s)) }

// TestUnbornHead covers the state `init` leaves behind: HEAD names a branch
// that does not exist. It must resolve without error, because "no commits yet"
// is normal, not broken.
func TestUnbornHead(t *testing.T) {
	s, _ := newStore(t)

	head, err := s.Head()
	if err != nil {
		t.Fatalf("Head on a fresh repository: %v", err)
	}
	if head.Detached() {
		t.Error("fresh HEAD should be attached to a branch")
	}
	if !head.Unborn() {
		t.Error("fresh HEAD should be unborn")
	}
	if head.Ref != "refs/heads/main" {
		t.Errorf("Ref = %q, want refs/heads/main", head.Ref)
	}
	if head.ShortRef() != "main" {
		t.Errorf("ShortRef = %q, want main", head.ShortRef())
	}
}

// TestFirstCommitCreatesBranch is the resolution of the unborn state: updating
// HEAD when no branch file exists creates it, with no special case needed.
func TestFirstCommitCreatesBranch(t *testing.T) {
	s, dir := newStore(t)
	want := oid("first commit")

	if err := s.UpdateHead(want); err != nil {
		t.Fatalf("UpdateHead: %v", err)
	}

	branch := filepath.Join(dir, "refs", "heads", "main")
	raw, err := os.ReadFile(branch)
	if err != nil {
		t.Fatalf("branch file was not created: %v", err)
	}
	// Ref files hold 40 hex characters plus a newline, as Git writes them.
	if string(raw) != want.String()+"\n" {
		t.Errorf("branch file = %q, want %q", raw, want.String()+"\n")
	}

	head, err := s.Head()
	if err != nil {
		t.Fatalf("Head: %v", err)
	}
	if head.Unborn() || head.OID != want {
		t.Errorf("HEAD = %+v, want it resolved to %s", head, want)
	}
}

// TestUpdateHeadMovesBranchNotHead is the payoff of HEAD's indirection: on a
// branch, committing advances the branch and leaves HEAD's own bytes untouched.
func TestUpdateHeadMovesBranchNotHead(t *testing.T) {
	s, dir := newStore(t)
	headPath := filepath.Join(dir, HeadFile)

	before, err := os.ReadFile(headPath)
	if err != nil {
		t.Fatal(err)
	}

	for i, c := range []object.OID{oid("c1"), oid("c2"), oid("c3")} {
		if err := s.UpdateHead(c); err != nil {
			t.Fatalf("UpdateHead %d: %v", i, err)
		}
		after, err := os.ReadFile(headPath)
		if err != nil {
			t.Fatal(err)
		}
		if string(after) != string(before) {
			t.Fatalf("HEAD was rewritten: %q became %q", before, after)
		}
		got, err := s.Resolve("refs/heads/main")
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if got != c {
			t.Errorf("branch = %s, want %s", got, c)
		}
	}
}

// TestDetachedHead covers the other mode: HEAD holds an ID directly, so there
// is no branch to advance and UpdateHead must rewrite HEAD itself.
func TestDetachedHead(t *testing.T) {
	s, _ := newStore(t)
	first := oid("c1")
	if err := s.UpdateHead(first); err != nil {
		t.Fatal(err)
	}

	if err := s.SetHeadDetached(first); err != nil {
		t.Fatalf("SetHeadDetached: %v", err)
	}
	head, err := s.Head()
	if err != nil {
		t.Fatalf("Head: %v", err)
	}
	if !head.Detached() {
		t.Fatal("HEAD should be detached")
	}
	if head.OID != first {
		t.Errorf("HEAD = %s, want %s", head.OID, first)
	}

	// Committing while detached moves HEAD and leaves the branch behind.
	second := oid("c2")
	if err := s.UpdateHead(second); err != nil {
		t.Fatal(err)
	}
	if head, _ := s.Head(); head.OID != second {
		t.Errorf("detached HEAD = %s, want %s", head.OID, second)
	}
	if branch, _ := s.Resolve("refs/heads/main"); branch != first {
		t.Errorf("branch moved to %s; it should still be %s", branch, first)
	}
}

// TestBranchesAreIndependentPointers is why branching is free: two names into
// the same immutable graph, each 41 bytes.
func TestBranchesAreIndependentPointers(t *testing.T) {
	s, _ := newStore(t)
	shared := oid("shared base")

	if err := s.Update("refs/heads/main", shared); err != nil {
		t.Fatal(err)
	}
	if err := s.Update("refs/heads/feature", shared); err != nil {
		t.Fatal(err)
	}

	main, _ := s.Resolve("refs/heads/main")
	feature, _ := s.Resolve("refs/heads/feature")
	if main != feature {
		t.Fatal("both branches should start at the same commit")
	}

	// Moving one must not disturb the other.
	moved := oid("feature work")
	if err := s.Update("refs/heads/feature", moved); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Resolve("refs/heads/main"); got != shared {
		t.Errorf("main = %s, want %s", got, shared)
	}
	if got, _ := s.Resolve("refs/heads/feature"); got != moved {
		t.Errorf("feature = %s, want %s", got, moved)
	}
}

func TestSetHeadSymbolic(t *testing.T) {
	s, dir := newStore(t)
	target := oid("on feature")
	if err := s.Update("refs/heads/feature", target); err != nil {
		t.Fatal(err)
	}
	if err := s.SetHeadSymbolic("refs/heads/feature"); err != nil {
		t.Fatalf("SetHeadSymbolic: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, HeadFile))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "ref: refs/heads/feature\n" {
		t.Errorf("HEAD = %q, want %q", raw, "ref: refs/heads/feature\n")
	}

	head, err := s.Head()
	if err != nil {
		t.Fatal(err)
	}
	if head.Detached() || head.OID != target || head.ShortRef() != "feature" {
		t.Errorf("head = %+v, want feature at %s", head, target)
	}
}

// TestSymrefCycleTerminates proves the depth limit works. Refs are ordinary
// files a user can edit, so a self-referential ref is reachable in practice.
func TestSymrefCycleTerminates(t *testing.T) {
	s, dir := newStore(t)
	write := func(name, content string) {
		full := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("refs/heads/a", "ref: refs/heads/b\n")
	write("refs/heads/b", "ref: refs/heads/a\n")

	done := make(chan error, 1)
	go func() {
		_, err := s.Resolve("refs/heads/a")
		done <- err
	}()

	if err := <-done; err == nil {
		t.Fatal("Resolve of a cyclic symref succeeded, want an error")
	}
}

// TestInvalidRefNamesRejected is a security test: ref names become filesystem
// paths, so traversal must be impossible.
func TestInvalidRefNamesRejected(t *testing.T) {
	s, _ := newStore(t)
	bad := []string{
		"",
		"refs/heads/../../../../etc/passwd",
		"refs/heads/..",
		"main",                    // missing refs/ prefix
		"refs/heads/with space",   //
		"refs/heads/tilde~1",      //
		"refs/heads/caret^",       //
		"refs/heads/colon:",       //
		"refs/heads/question?",    //
		"refs/heads/star*",        //
		"refs/heads/open[",        //
		"refs/heads/name.lock",    //
		"refs/heads/at@{0}",       //
		`refs/heads/back\slash`,   //
		"refs/heads/",             // trailing slash
		"refs/heads//double",      // empty component
		"refs/heads/.hidden",      // component starting with .
		"refs/heads/ctrl\x01char", //
	}
	for _, name := range bad {
		t.Run(name, func(t *testing.T) {
			if err := s.Update(name, oid("x")); err == nil {
				t.Errorf("Update(%q) succeeded, want rejection", name)
			}
		})
	}
}

// TestTraversalDoesNotEscape confirms the validation actually prevents writes
// outside the repository, not merely returns an error.
func TestTraversalDoesNotEscape(t *testing.T) {
	s, dir := newStore(t)
	outside := filepath.Join(filepath.Dir(dir), "escaped")

	_ = s.Update("refs/heads/../../escaped", oid("x"))

	if _, err := os.Stat(outside); err == nil {
		t.Fatalf("a ref was written outside the repository at %s", outside)
	}
}

func TestResolveMissingRef(t *testing.T) {
	s, _ := newStore(t)
	if _, err := s.Resolve("refs/heads/nonexistent"); !errors.Is(err, ErrNotFound) {
		t.Errorf("error = %v, want ErrNotFound", err)
	}
	if s.Exists("refs/heads/nonexistent") {
		t.Error("Exists reported a missing ref")
	}
}

func TestCorruptHead(t *testing.T) {
	s, dir := newStore(t)
	if err := os.WriteFile(filepath.Join(dir, HeadFile), []byte("not a ref or an oid\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Head(); err == nil {
		t.Error("Head accepted a corrupt HEAD file")
	}
}

func TestBranchRef(t *testing.T) {
	if got := BranchRef("main"); got != "refs/heads/main" {
		t.Errorf("BranchRef(main) = %q, want refs/heads/main", got)
	}
}
