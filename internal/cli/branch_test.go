package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBranchIsFortyOneBytes is the Phase 9 headline: creating a branch writes
// one small file and copies nothing.
func TestBranchIsFortyOneBytes(t *testing.T) {
	dir := initRepo(t)
	tip := commitFile(t, dir, "f.txt", "content", "first", "1700000000 +0000")

	objectsBefore := countFiles(t, filepath.Join(dir, ".mygit", "objects"))

	if code, _, stderr := run(t, dir, "", "branch", "feature"); code != 0 {
		t.Fatalf("branch failed (%d): %s", code, stderr)
	}

	raw, err := os.ReadFile(filepath.Join(dir, ".mygit", "refs", "heads", "feature"))
	if err != nil {
		t.Fatalf("branch file missing: %v", err)
	}
	if len(raw) != 41 {
		t.Errorf("branch file is %d bytes, want 41 (40 hex + newline)", len(raw))
	}
	if strings.TrimSpace(string(raw)) != tip {
		t.Errorf("branch points at %q, want %s", raw, tip)
	}

	// Not one object was written: the branch names history that already exists.
	if after := countFiles(t, filepath.Join(dir, ".mygit", "objects")); after != objectsBefore {
		t.Errorf("object count went %d -> %d; creating a branch copied data", objectsBefore, after)
	}
}

func countFiles(t *testing.T, root string) int {
	t.Helper()
	count := 0
	err := filepath.WalkDir(root, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			count++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
	return count
}

func TestBranchListMarksCurrent(t *testing.T) {
	dir := initRepo(t)
	commitFile(t, dir, "f.txt", "x", "first", "1700000000 +0000")
	run(t, dir, "", "branch", "feature")
	run(t, dir, "", "branch", "other")

	code, stdout, stderr := run(t, dir, "", "branch")
	if code != 0 {
		t.Fatalf("branch listing failed (%d): %s", code, stderr)
	}

	want := "  feature\n* main\n  other\n"
	if stdout != want {
		t.Errorf("listing:\n%q\nwant:\n%q", stdout, want)
	}
}

func TestBranchListShowsDetachedHead(t *testing.T) {
	dir := initRepo(t)
	first := commitFile(t, dir, "f.txt", "one", "first", "1700000000 +0000")
	commitFile(t, dir, "f.txt", "two", "second", "1700000060 +0000")

	run(t, dir, "", "checkout", first)

	_, stdout, _ := run(t, dir, "", "branch")
	if !strings.Contains(stdout, "(HEAD detached at "+first[:7]+")") {
		t.Errorf("listing does not report the detached position:\n%s", stdout)
	}
	if strings.Contains(stdout, "* main") {
		t.Errorf("main is marked current while HEAD is detached:\n%s", stdout)
	}
}

// TestBranchesDivergeIndependently is the model in miniature: two names into
// one immutable graph, each moving on its own.
func TestBranchesDivergeIndependently(t *testing.T) {
	dir := initRepo(t)
	base := commitFile(t, dir, "f.txt", "base", "base", "1700000000 +0000")

	run(t, dir, "", "branch", "feature")

	// Advance main.
	mainTip := commitFile(t, dir, "f.txt", "main work", "main commit", "1700000060 +0000")

	// feature must not have moved.
	_, out, _ := run(t, dir, "", "rev-parse", "feature")
	if strings.TrimSpace(out) != base {
		t.Errorf("feature = %q, want %s — it followed main", out, base)
	}

	// Advance feature.
	run(t, dir, "", "checkout", "feature")
	featTip := commitFile(t, dir, "f.txt", "feature work", "feature commit", "1700000120 +0000")

	if _, out, _ := run(t, dir, "", "rev-parse", "main"); strings.TrimSpace(out) != mainTip {
		t.Errorf("main = %q, want %s", out, mainTip)
	}
	if featTip == mainTip {
		t.Fatal("the two branches produced the same commit")
	}

	// Both histories reach the shared base, which exists once on disk.
	for _, branch := range []string{"main", "feature"} {
		_, log, _ := run(t, dir, "", "log", "--oneline", branch)
		if !strings.Contains(log, "base") {
			t.Errorf("%s history does not reach the base:\n%s", branch, log)
		}
	}
}

func TestBranchAtExplicitStartPoint(t *testing.T) {
	dir := initRepo(t)
	first := commitFile(t, dir, "f.txt", "one", "first", "1700000000 +0000")
	commitFile(t, dir, "f.txt", "two", "second", "1700000060 +0000")

	if code, _, stderr := run(t, dir, "", "branch", "from-first", first); code != 0 {
		t.Fatalf("branch failed (%d): %s", code, stderr)
	}
	if _, out, _ := run(t, dir, "", "rev-parse", "from-first"); strings.TrimSpace(out) != first {
		t.Errorf("from-first = %q, want %s", out, first)
	}
}

// TestCheckoutDashB covers create-and-switch, and asserts the working tree is
// untouched because the new branch starts where HEAD already is.
func TestCheckoutDashB(t *testing.T) {
	dir := initRepo(t)
	tip := commitFile(t, dir, "f.txt", "content", "first", "1700000000 +0000")

	code, stdout, stderr := run(t, dir, "", "checkout", "-b", "feature")
	if code != 0 {
		t.Fatalf("checkout -b failed (%d): %s", code, stderr)
	}
	if !strings.Contains(stdout, "Switched to branch 'feature'") {
		t.Errorf("stdout = %q, want a switch message", stdout)
	}

	if _, out, _ := run(t, dir, "", "rev-parse", "--abbrev-ref", "HEAD"); strings.TrimSpace(out) != "feature" {
		t.Errorf("HEAD = %q, want feature", out)
	}
	if _, out, _ := run(t, dir, "", "rev-parse", "HEAD"); strings.TrimSpace(out) != tip {
		t.Errorf("HEAD resolves to %q, want the unchanged tip %s", out, tip)
	}
	if got := readFile(t, dir, "f.txt"); got != "content" {
		t.Errorf("f.txt = %q; creating a branch changed the working tree", got)
	}
}

// TestSwitchingBranchesRewritesWorkTree is Phase 10 end to end: HEAD, the
// index, and the files all follow the branch.
func TestSwitchingBranchesRewritesWorkTree(t *testing.T) {
	dir := initRepo(t)
	commitFile(t, dir, "shared.txt", "base", "base", "1700000000 +0000")

	run(t, dir, "", "checkout", "-b", "feature")
	pinIdentity(t, "1700000060 +0000")
	write(t, dir, "feature-only.txt", "exists only on feature")
	run(t, dir, "", "add", ".")
	run(t, dir, "", "commit", "-m", "feature work")

	// Back to main: the feature-only file must disappear.
	if code, _, stderr := run(t, dir, "", "checkout", "main"); code != 0 {
		t.Fatalf("checkout main failed (%d): %s", code, stderr)
	}
	if exists(dir, "feature-only.txt") {
		t.Error("feature-only.txt survived the switch to main")
	}
	if _, listing, _ := run(t, dir, "", "ls-files"); strings.Contains(listing, "feature-only") {
		t.Errorf("the index still lists feature-only.txt:\n%s", listing)
	}

	// And back to feature: it must reappear.
	if code, _, stderr := run(t, dir, "", "checkout", "feature"); code != 0 {
		t.Fatalf("checkout feature failed (%d): %s", code, stderr)
	}
	if got := readFile(t, dir, "feature-only.txt"); got != "exists only on feature" {
		t.Errorf("feature-only.txt = %q, want it restored", got)
	}
}

// TestDeleteMergedBranch confirms deletion is allowed when nothing is stranded.
func TestDeleteMergedBranch(t *testing.T) {
	dir := initRepo(t)
	commitFile(t, dir, "f.txt", "content", "first", "1700000000 +0000")

	// A branch at the same commit as HEAD is trivially merged.
	run(t, dir, "", "branch", "feature")

	code, stdout, stderr := run(t, dir, "", "branch", "-d", "feature")
	if code != 0 {
		t.Fatalf("deleting a merged branch failed (%d): %s", code, stderr)
	}
	if !strings.Contains(stdout, "Deleted branch feature") {
		t.Errorf("stdout = %q", stdout)
	}
	if exists(dir, ".mygit/refs/heads/feature") {
		t.Error("the branch file still exists")
	}
}

// TestDeleteUnmergedBranchRefused is the safety property. The commits are not
// deleted — they become unreachable, which is worse, because nothing looks
// broken afterwards.
func TestDeleteUnmergedBranchRefused(t *testing.T) {
	dir := initRepo(t)
	commitFile(t, dir, "f.txt", "base", "base", "1700000000 +0000")

	run(t, dir, "", "checkout", "-b", "feature")
	orphan := commitFile(t, dir, "f.txt", "feature work", "feature commit", "1700000060 +0000")
	run(t, dir, "", "checkout", "main")

	code, _, stderr := run(t, dir, "", "branch", "-d", "feature")
	if code == 0 {
		t.Fatal("deleting an unmerged branch succeeded")
	}
	if !strings.Contains(stderr, "not fully merged") {
		t.Errorf("stderr = %q, want it to mention the merge state", stderr)
	}
	if !exists(dir, ".mygit/refs/heads/feature") {
		t.Error("the branch was deleted despite the refusal")
	}

	// -D forces it, and demonstrates what "deleting a branch" actually does:
	// the commit object survives, but nothing reaches it.
	if code, _, stderr := run(t, dir, "", "branch", "-D", "feature"); code != 0 {
		t.Fatalf("forced delete failed (%d): %s", code, stderr)
	}
	if code, _, _ := run(t, dir, "", "cat-file", "-t", orphan); code != 0 {
		t.Error("the commit object was destroyed; deleting a ref must not delete objects")
	}
	if code, _, _ := run(t, dir, "", "log", "--oneline", "feature"); code == 0 {
		t.Error("the deleted branch is still resolvable")
	}
}

func TestCannotDeleteCurrentBranch(t *testing.T) {
	dir := initRepo(t)
	commitFile(t, dir, "f.txt", "x", "first", "1700000000 +0000")

	code, _, stderr := run(t, dir, "", "branch", "-d", "main")
	if code == 0 {
		t.Fatal("deleting the checked-out branch succeeded")
	}
	if !strings.Contains(stderr, "currently checked out") {
		t.Errorf("stderr = %q", stderr)
	}
}

func TestBranchErrors(t *testing.T) {
	bare := t.TempDir()
	repo := initRepo(t)
	commitFile(t, repo, "f.txt", "x", "first", "1700000000 +0000")
	run(t, repo, "", "branch", "existing")

	cases := []struct {
		name string
		dir  string
		args []string
	}{
		{"outside a repository", bare, []string{"branch"}},
		{"duplicate name", repo, []string{"branch", "existing"}},
		{"unknown start point", repo, []string{"branch", "newone", "no-such-rev"}},
		{"too many arguments", repo, []string{"branch", "a", "HEAD", "extra"}},
		{"delete without a name", repo, []string{"branch", "-d"}},
		{"delete a missing branch", repo, []string{"branch", "-d", "no-such-branch"}},
		{"invalid name", repo, []string{"branch", "bad name with spaces"}},
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

// TestBranchOnUnbornHead documents behaviour before the first commit: there is
// no commit to point a branch at.
func TestBranchOnUnbornHead(t *testing.T) {
	dir := initRepo(t)

	if code, stdout, _ := run(t, dir, "", "branch"); code != 0 || strings.TrimSpace(stdout) != "" {
		t.Errorf("listing on an empty repository: code=%d stdout=%q", code, stdout)
	}
	if code, _, _ := run(t, dir, "", "branch", "feature"); code == 0 {
		t.Error("created a branch with no commits to point at")
	}
}
