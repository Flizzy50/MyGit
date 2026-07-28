package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// readFile returns a working-tree file's contents, failing if it is absent.
func readFile(t *testing.T, dir, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("reading %s: %v", rel, err)
	}
	return string(data)
}

func exists(dir, rel string) bool {
	_, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(rel)))
	return err == nil
}

// TestCheckoutRestoresSnapshot is the Phase 8 acceptance test: moving to an
// older commit reconstructs that commit's working tree exactly, including
// removing files that did not exist yet.
func TestCheckoutRestoresSnapshot(t *testing.T) {
	dir := initRepo(t)

	first := commitFile(t, dir, "a.txt", "version one", "first", "1700000000 +0000")

	pinIdentity(t, "1700000060 +0000")
	write(t, dir, "a.txt", "version two")
	write(t, dir, "b.txt", "added later")
	run(t, dir, "", "add", ".")
	run(t, dir, "", "commit", "-m", "second")

	if code, _, stderr := run(t, dir, "", "checkout", first); code != 0 {
		t.Fatalf("checkout failed (%d): %s", code, stderr)
	}

	if got := readFile(t, dir, "a.txt"); got != "version one" {
		t.Errorf("a.txt = %q, want %q", got, "version one")
	}
	// b.txt did not exist in the first commit and must be gone.
	if exists(dir, "b.txt") {
		t.Error("b.txt still exists; checkout did not delete it")
	}
}

// TestCheckoutRefusesToClobberLocalChanges is the safety property that matters
// most. The edit exists nowhere but the working tree, so losing it would be
// unrecoverable.
func TestCheckoutRefusesToClobberLocalChanges(t *testing.T) {
	dir := initRepo(t)
	first := commitFile(t, dir, "f.txt", "committed content", "first", "1700000000 +0000")
	commitFile(t, dir, "f.txt", "second content", "second", "1700000060 +0000")

	// An uncommitted edit.
	write(t, dir, "f.txt", "precious uncommitted work")

	code, _, stderr := run(t, dir, "", "checkout", first)
	if code == 0 {
		t.Fatal("checkout overwrote uncommitted changes")
	}
	if !strings.Contains(stderr, "local changes") {
		t.Errorf("stderr = %q, want it to mention local changes", stderr)
	}

	// The critical assertion: the working tree is completely untouched.
	if got := readFile(t, dir, "f.txt"); got != "precious uncommitted work" {
		t.Errorf("f.txt = %q; the rejected checkout modified it anyway", got)
	}
	// HEAD must not have moved either.
	_, head, _ := run(t, dir, "", "rev-parse", "--abbrev-ref", "HEAD")
	if strings.TrimSpace(head) != "main" {
		t.Errorf("HEAD = %q, want main", head)
	}
}

// TestCheckoutRefusesToClobberUntrackedFile covers the more dangerous case: the
// file was never staged, so no version of it exists in the object database.
func TestCheckoutRefusesToClobberUntrackedFile(t *testing.T) {
	dir := initRepo(t)
	first := commitFile(t, dir, "a.txt", "one", "first", "1700000000 +0000")

	pinIdentity(t, "1700000060 +0000")
	write(t, dir, "b.txt", "committed in second")
	run(t, dir, "", "add", ".")
	run(t, dir, "", "commit", "-m", "second")

	run(t, dir, "", "checkout", first) // b.txt removed
	write(t, dir, "b.txt", "untracked file created by hand")

	// Returning to the second commit would overwrite the untracked b.txt.
	code, _, stderr := run(t, dir, "", "checkout", "main")
	if code == 0 {
		t.Fatal("checkout overwrote an untracked file")
	}
	if !strings.Contains(stderr, "untracked") {
		t.Errorf("stderr = %q, want it to mention untracked", stderr)
	}
	if got := readFile(t, dir, "b.txt"); got != "untracked file created by hand" {
		t.Errorf("b.txt = %q; the untracked file was clobbered", got)
	}
}

// TestCheckoutAllowsCleanSwitch confirms the safety check is not overzealous.
func TestCheckoutAllowsCleanSwitch(t *testing.T) {
	dir := initRepo(t)
	first := commitFile(t, dir, "f.txt", "one", "first", "1700000000 +0000")
	second := commitFile(t, dir, "f.txt", "two", "second", "1700000060 +0000")

	for _, step := range []struct{ target, want string }{
		{first, "one"},
		{second, "two"},
		{first, "one"},
	} {
		if code, _, stderr := run(t, dir, "", "checkout", step.target); code != 0 {
			t.Fatalf("checkout %s failed (%d): %s", step.target[:7], code, stderr)
		}
		if got := readFile(t, dir, "f.txt"); got != step.want {
			t.Errorf("after checkout %s: f.txt = %q, want %q", step.target[:7], got, step.want)
		}
	}
}

// TestCheckoutUpdatesIndex verifies all three trees move together. If the index
// were left describing the old commit, the checked-out files would immediately
// look modified.
func TestCheckoutUpdatesIndex(t *testing.T) {
	dir := initRepo(t)
	first := commitFile(t, dir, "a.txt", "one", "first", "1700000000 +0000")

	pinIdentity(t, "1700000060 +0000")
	write(t, dir, "a.txt", "two")
	write(t, dir, "b.txt", "new file")
	run(t, dir, "", "add", ".")
	run(t, dir, "", "commit", "-m", "second")

	run(t, dir, "", "checkout", first)

	_, listing, _ := run(t, dir, "", "ls-files")
	if strings.TrimSpace(listing) != "a.txt" {
		t.Errorf("index lists %q, want just a.txt", strings.TrimSpace(listing))
	}

	// The decisive check: with the index correct, committing again must report
	// nothing to commit rather than resurrecting the old snapshot.
	pinIdentity(t, "1700000120 +0000")
	if code, _, stderr := run(t, dir, "", "commit", "-m", "should be empty"); code == 0 {
		t.Error("a commit succeeded immediately after checkout; the index is stale")
	} else if !strings.Contains(stderr, "nothing to commit") {
		t.Errorf("error = %q, want nothing to commit", stderr)
	}
}

// TestCheckoutPrunesEmptyDirectories covers Git's inability to represent an
// empty directory: when the last file in one disappears, so must the directory.
func TestCheckoutPrunesEmptyDirectories(t *testing.T) {
	dir := initRepo(t)
	first := commitFile(t, dir, "root.txt", "root", "first", "1700000000 +0000")

	pinIdentity(t, "1700000060 +0000")
	write(t, dir, "deep/nested/file.txt", "nested content")
	run(t, dir, "", "add", ".")
	run(t, dir, "", "commit", "-m", "add nested")

	if !exists(dir, "deep/nested/file.txt") {
		t.Fatal("setup failed: nested file missing")
	}

	if code, _, stderr := run(t, dir, "", "checkout", first); code != 0 {
		t.Fatalf("checkout failed (%d): %s", code, stderr)
	}
	if exists(dir, "deep") {
		t.Error("directory 'deep' survived checkout despite having no files")
	}
	if !exists(dir, "root.txt") {
		t.Error("root.txt was wrongly removed")
	}
}

// TestCheckoutDetachesHead covers the state where HEAD holds a raw object ID.
func TestCheckoutDetachesHead(t *testing.T) {
	dir := initRepo(t)
	first := commitFile(t, dir, "f.txt", "one", "first", "1700000000 +0000")
	commitFile(t, dir, "f.txt", "two", "second", "1700000060 +0000")

	code, stdout, stderr := run(t, dir, "", "checkout", first)
	if code != 0 {
		t.Fatalf("checkout failed (%d): %s", code, stderr)
	}
	if !strings.Contains(stdout, "HEAD is now at "+first[:7]) {
		t.Errorf("stdout = %q, want it to report the detached position", stdout)
	}
	if !strings.Contains(stderr, "detached") {
		t.Errorf("stderr = %q, want a detached-HEAD warning", stderr)
	}

	// HEAD now holds the ID directly, not a "ref:" line.
	raw, err := os.ReadFile(filepath.Join(dir, ".mygit", "HEAD"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(raw)) != first {
		t.Errorf("HEAD = %q, want the raw commit id %s", raw, first)
	}
	if _, abbrev, _ := run(t, dir, "", "rev-parse", "--abbrev-ref", "HEAD"); strings.TrimSpace(abbrev) != "HEAD" {
		t.Errorf("--abbrev-ref = %q, want HEAD when detached", abbrev)
	}
}

// TestCheckoutBranchReattachesHead is the other half: naming a branch restores
// the symbolic ref, so later commits advance the branch again.
func TestCheckoutBranchReattachesHead(t *testing.T) {
	dir := initRepo(t)
	first := commitFile(t, dir, "f.txt", "one", "first", "1700000000 +0000")
	second := commitFile(t, dir, "f.txt", "two", "second", "1700000060 +0000")

	run(t, dir, "", "checkout", first) // detach

	code, stdout, stderr := run(t, dir, "", "checkout", "main")
	if code != 0 {
		t.Fatalf("checkout main failed (%d): %s", code, stderr)
	}
	if !strings.Contains(stdout, "Switched to branch 'main'") {
		t.Errorf("stdout = %q, want a branch switch message", stdout)
	}

	raw, err := os.ReadFile(filepath.Join(dir, ".mygit", "HEAD"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(raw)) != "ref: refs/heads/main" {
		t.Errorf("HEAD = %q, want the symbolic ref restored", raw)
	}
	if _, out, _ := run(t, dir, "", "rev-parse", "HEAD"); strings.TrimSpace(out) != second {
		t.Errorf("HEAD resolves to %q, want %s", out, second)
	}
	if got := readFile(t, dir, "f.txt"); got != "two" {
		t.Errorf("f.txt = %q, want two", got)
	}
}

// TestCommitWhileDetachedDoesNotMoveBranch demonstrates concretely why the
// detached warning exists: the new commit is reachable from nothing once HEAD
// moves away.
func TestCommitWhileDetachedDoesNotMoveBranch(t *testing.T) {
	dir := initRepo(t)
	first := commitFile(t, dir, "f.txt", "one", "first", "1700000000 +0000")
	second := commitFile(t, dir, "f.txt", "two", "second", "1700000060 +0000")

	run(t, dir, "", "checkout", first)

	pinIdentity(t, "1700000120 +0000")
	write(t, dir, "f.txt", "work done while detached")
	run(t, dir, "", "add", "f.txt")
	if code, stdout, stderr := run(t, dir, "", "commit", "-m", "detached work"); code != 0 {
		t.Fatalf("commit failed (%d): %s", code, stderr)
	} else if !strings.Contains(stdout, "detached HEAD") {
		t.Errorf("commit summary = %q, want it to say detached HEAD", stdout)
	}

	_, orphan, _ := run(t, dir, "", "rev-parse", "HEAD")
	orphanID := strings.TrimSpace(orphan)

	// main never moved.
	_, mainTip, _ := run(t, dir, "", "rev-parse", "main")
	if strings.TrimSpace(mainTip) != second {
		t.Errorf("main = %q, want %s — a detached commit moved the branch", mainTip, second)
	}

	// Switching away strands the commit: the object still exists, but no ref
	// reaches it. This is precisely what a reflog would otherwise rescue.
	run(t, dir, "", "checkout", "main")
	if code, _, _ := run(t, dir, "", "cat-file", "-t", orphanID); code != 0 {
		t.Error("the orphaned commit object vanished; it should still be on disk")
	}
	_, reachable, _ := run(t, dir, "", "log", "--oneline")
	if strings.Contains(reachable, "detached work") {
		t.Error("the orphaned commit is still reachable from main")
	}
}

// TestCheckoutRestoresDeletedFile covers index staleness. The index is a cache
// of what the working tree is believed to hold, and deleting a tracked file
// behind mygit's back makes that belief wrong. Checkout must notice and restore
// the file rather than trusting the cache and skipping it — this matches real
// Git, which restores tracked files deleted from disk.
func TestCheckoutRestoresDeletedFile(t *testing.T) {
	dir := initRepo(t)
	commitFile(t, dir, "a.txt", "content", "first", "1700000000 +0000")

	pinIdentity(t, "1700000060 +0000")
	write(t, dir, "nested/deep.txt", "nested content")
	run(t, dir, "", "add", ".")
	run(t, dir, "", "commit", "-m", "second")

	// Delete tracked files outside mygit's knowledge; the index still lists them.
	if err := os.Remove(filepath.Join(dir, "a.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(dir, "nested")); err != nil {
		t.Fatal(err)
	}

	if code, _, stderr := run(t, dir, "", "checkout", "main"); code != 0 {
		t.Fatalf("checkout failed (%d): %s", code, stderr)
	}

	if got := readFile(t, dir, "a.txt"); got != "content" {
		t.Errorf("a.txt = %q, want it restored to %q", got, "content")
	}
	if got := readFile(t, dir, "nested/deep.txt"); got != "nested content" {
		t.Errorf("nested/deep.txt = %q, want it restored", got)
	}
}

func TestCheckoutErrors(t *testing.T) {
	bare := t.TempDir()
	repo := initRepo(t)
	commitFile(t, repo, "f.txt", "x", "only", "1700000000 +0000")

	cases := []struct {
		name string
		dir  string
		args []string
	}{
		{"outside a repository", bare, []string{"checkout", "main"}},
		{"no argument", repo, []string{"checkout"}},
		{"two arguments", repo, []string{"checkout", "main", "extra"}},
		{"unknown revision", repo, []string{"checkout", "no-such-thing"}},
		{"missing object", repo, []string{"checkout", "0000000000000000000000000000000000000000"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, _, stderr := run(t, tc.dir, "", tc.args...)
			if code == 0 {
				t.Fatalf("%v exited 0, want failure", tc.args)
			}
			if stderr == "" {
				t.Error("failed without writing to stderr")
			}
		})
	}
}
