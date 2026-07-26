package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// pinIdentity fixes every input to the commit hash, so commit IDs are
// reproducible and can be compared against real Git byte for byte. This is only
// possible because the timestamp is an injectable input rather than an ambient
// clock read.
func pinIdentity(t *testing.T, date string) {
	t.Helper()
	t.Setenv("MYGIT_AUTHOR_NAME", "Test Author")
	t.Setenv("MYGIT_AUTHOR_EMAIL", "author@example.com")
	t.Setenv("MYGIT_COMMITTER_NAME", "Test Author")
	t.Setenv("MYGIT_COMMITTER_EMAIL", "author@example.com")
	t.Setenv("MYGIT_AUTHOR_DATE", date)
	t.Setenv("MYGIT_COMMITTER_DATE", date)
}

// TestCommitMatchesRealGit is the end-to-end acceptance test for Phase 6: the
// same file, the same identity, the same timestamp, and therefore the same
// commit ID that real `git commit` produced.
func TestCommitMatchesRealGit(t *testing.T) {
	pinIdentity(t, "1700000000 +0530")

	dir := initRepo(t)
	write(t, dir, "a.txt", "hello world")
	run(t, dir, "", "add", "a.txt")

	code, stdout, stderr := run(t, dir, "", "commit", "-m", "Initial commit")
	if code != 0 {
		t.Fatalf("commit failed (%d): %s", code, stderr)
	}
	if !strings.Contains(stdout, "root-commit") {
		t.Errorf("summary %q should mark the root commit", stdout)
	}

	_, out, _ := run(t, dir, "", "rev-parse", "HEAD")
	const want = "e56d42d988ad8d99aaaf551c1498a66a63c8c168"
	if got := strings.TrimSpace(out); got != want {
		t.Fatalf("commit id = %s, want real Git's %s", got, want)
	}
}

// TestFirstCommitCreatesBranch verifies the unborn branch from Phase 1 is
// resolved exactly when the first commit lands, and not before.
func TestFirstCommitCreatesBranch(t *testing.T) {
	pinIdentity(t, "1700000000 +0000")
	dir := initRepo(t)

	branch := filepath.Join(dir, ".mygit", "refs", "heads", "main")
	if _, err := os.Stat(branch); err == nil {
		t.Fatal("refs/heads/main existed before the first commit")
	}
	// HEAD already names the branch, but nothing resolves yet.
	if code, _, _ := run(t, dir, "", "rev-parse", "HEAD"); code == 0 {
		t.Error("rev-parse HEAD succeeded on an unborn branch")
	}
	if _, out, _ := run(t, dir, "", "rev-parse", "--abbrev-ref", "HEAD"); strings.TrimSpace(out) != "main" {
		t.Errorf("--abbrev-ref = %q, want main even when unborn", out)
	}

	write(t, dir, "f.txt", "content")
	run(t, dir, "", "add", "f.txt")
	if code, _, stderr := run(t, dir, "", "commit", "-m", "first"); code != 0 {
		t.Fatalf("commit failed (%d): %s", code, stderr)
	}

	if _, err := os.Stat(branch); err != nil {
		t.Fatalf("first commit did not create refs/heads/main: %v", err)
	}
}

// TestCommitChainBuildsHistory checks that each commit records its predecessor,
// forming the backward-linked graph that Phase 7 will walk.
func TestCommitChainBuildsHistory(t *testing.T) {
	dir := initRepo(t)

	var ids []string
	for i, msg := range []string{"first", "second", "third"} {
		pinIdentity(t, "170000000"+string(rune('0'+i))+" +0000")
		write(t, dir, "f.txt", "version "+msg)
		run(t, dir, "", "add", "f.txt")
		if code, _, stderr := run(t, dir, "", "commit", "-m", msg); code != 0 {
			t.Fatalf("commit %q failed (%d): %s", msg, code, stderr)
		}
		_, out, _ := run(t, dir, "", "rev-parse", "HEAD")
		ids = append(ids, strings.TrimSpace(out))
	}

	// Walk backwards: each commit's parent must be its predecessor.
	for i := len(ids) - 1; i > 0; i-- {
		_, body, _ := run(t, dir, "", "cat-file", "-p", ids[i])
		if !strings.Contains(body, "parent "+ids[i-1]) {
			t.Errorf("commit %d does not name %s as parent:\n%s", i, ids[i-1], body)
		}
	}

	// The root commit has no parent line at all.
	_, root, _ := run(t, dir, "", "cat-file", "-p", ids[0])
	if strings.Contains(root, "parent ") {
		t.Errorf("root commit has a parent line:\n%s", root)
	}
}

// TestCommitAdvancesBranchNotHead is the indirection test at the CLI level.
func TestCommitAdvancesBranchNotHead(t *testing.T) {
	dir := initRepo(t)
	headPath := filepath.Join(dir, ".mygit", "HEAD")

	for i, msg := range []string{"one", "two"} {
		pinIdentity(t, "170000000"+string(rune('0'+i))+" +0000")
		write(t, dir, "f.txt", msg)
		run(t, dir, "", "add", "f.txt")
		run(t, dir, "", "commit", "-m", msg)

		raw, err := os.ReadFile(headPath)
		if err != nil {
			t.Fatal(err)
		}
		if string(raw) != "ref: refs/heads/main\n" {
			t.Fatalf("HEAD was rewritten to %q; it should still name the branch", raw)
		}
	}
}

// TestUnchangedTreeRefused shows the Merkle shortcut in action: proving nothing
// changed is one hash comparison, with no file reads.
func TestUnchangedTreeRefused(t *testing.T) {
	pinIdentity(t, "1700000000 +0000")
	dir := initRepo(t)
	write(t, dir, "f.txt", "content")
	run(t, dir, "", "add", "f.txt")
	run(t, dir, "", "commit", "-m", "first")

	code, _, stderr := run(t, dir, "", "commit", "-m", "again")
	if code == 0 {
		t.Fatal("committing an unchanged index succeeded")
	}
	if !strings.Contains(stderr, "nothing to commit") {
		t.Errorf("error = %q, want it to mention nothing to commit", stderr)
	}
}

func TestCommitEmptyIndexRefused(t *testing.T) {
	pinIdentity(t, "1700000000 +0000")
	dir := initRepo(t)

	if code, _, stderr := run(t, dir, "", "commit", "-m", "empty"); code == 0 {
		t.Fatal("committing with nothing staged succeeded")
	} else if !strings.Contains(stderr, "nothing to commit") {
		t.Errorf("error = %q, want it to mention nothing to commit", stderr)
	}
}

// TestCommitStoresReachableTree confirms the commit points at a real tree that
// contains the staged files — the object graph is genuinely connected.
func TestCommitStoresReachableTree(t *testing.T) {
	pinIdentity(t, "1700000000 +0000")
	dir := initRepo(t)
	write(t, dir, "README.md", "# hi\n")
	write(t, dir, "src/main.go", "package main\n")
	run(t, dir, "", "add", ".")
	run(t, dir, "", "commit", "-m", "snapshot")

	_, out, _ := run(t, dir, "", "rev-parse", "HEAD")
	_, body, _ := run(t, dir, "", "cat-file", "-p", strings.TrimSpace(out))

	var treeID string
	for _, line := range strings.Split(body, "\n") {
		if rest, ok := strings.CutPrefix(line, "tree "); ok {
			treeID = rest
			break
		}
	}
	if treeID == "" {
		t.Fatalf("commit has no tree header:\n%s", body)
	}

	code, listing, stderr := run(t, dir, "", "cat-file", "-p", treeID)
	if code != 0 {
		t.Fatalf("commit's tree is not readable (%d): %s", code, stderr)
	}
	if !strings.Contains(listing, "README.md") || !strings.Contains(listing, "\tsrc") {
		t.Errorf("tree listing is missing staged entries:\n%s", listing)
	}
}

// TestCommitterDefaultsToAuthor documents the fallback and, by extension, why
// the two signatures exist separately at all.
func TestCommitterDefaultsToAuthor(t *testing.T) {
	t.Setenv("MYGIT_AUTHOR_NAME", "Original Author")
	t.Setenv("MYGIT_AUTHOR_EMAIL", "orig@example.com")
	t.Setenv("MYGIT_AUTHOR_DATE", "1700000000 +0000")
	t.Setenv("MYGIT_COMMITTER_NAME", "")
	t.Setenv("MYGIT_COMMITTER_EMAIL", "")
	t.Setenv("GIT_COMMITTER_NAME", "")
	t.Setenv("GIT_COMMITTER_EMAIL", "")

	dir := initRepo(t)
	write(t, dir, "f.txt", "x")
	run(t, dir, "", "add", "f.txt")
	if code, _, stderr := run(t, dir, "", "commit", "-m", "m"); code != 0 {
		t.Fatalf("commit failed (%d): %s", code, stderr)
	}

	_, out, _ := run(t, dir, "", "rev-parse", "HEAD")
	_, body, _ := run(t, dir, "", "cat-file", "-p", strings.TrimSpace(out))
	if strings.Count(body, "Original Author") != 2 {
		t.Errorf("committer did not default to author:\n%s", body)
	}
}

// TestIdenticalCommitsDeduplicate shows commits obey the same content-addressed
// rules as every other object: same inputs, same ID, stored once.
func TestIdenticalCommitsDeduplicate(t *testing.T) {
	pinIdentity(t, "1700000000 +0000")

	ids := make([]string, 2)
	for i := range ids {
		dir := initRepo(t)
		write(t, dir, "f.txt", "identical content")
		run(t, dir, "", "add", "f.txt")
		run(t, dir, "", "commit", "-m", "identical message")
		_, out, _ := run(t, dir, "", "rev-parse", "HEAD")
		ids[i] = strings.TrimSpace(out)
	}
	if ids[0] != ids[1] {
		t.Errorf("identical commits in separate repositories got %s and %s", ids[0], ids[1])
	}
}

// TestTimestampChangesCommitID is the flip side, and the reason `git commit
// --amend` produces a new ID even with an unchanged message and tree.
func TestTimestampChangesCommitID(t *testing.T) {
	ids := make([]string, 2)
	for i, date := range []string{"1700000000 +0000", "1700000001 +0000"} {
		pinIdentity(t, date)
		dir := initRepo(t)
		write(t, dir, "f.txt", "same content")
		run(t, dir, "", "add", "f.txt")
		run(t, dir, "", "commit", "-m", "same message")
		_, out, _ := run(t, dir, "", "rev-parse", "HEAD")
		ids[i] = strings.TrimSpace(out)
	}
	if ids[0] == ids[1] {
		t.Error("commits one second apart produced the same id")
	}
}

func TestCommitErrors(t *testing.T) {
	repo := initRepo(t)
	bare := t.TempDir()

	cases := []struct {
		name string
		dir  string
		args []string
		env  func(*testing.T)
	}{
		{"no message", repo, []string{"commit"}, func(t *testing.T) { pinIdentity(t, "1700000000 +0000") }},
		{"blank message", repo, []string{"commit", "-m", "   "}, func(t *testing.T) { pinIdentity(t, "1700000000 +0000") }},
		{"positional argument", repo, []string{"commit", "-m", "x", "extra"}, func(t *testing.T) { pinIdentity(t, "1700000000 +0000") }},
		{"outside a repository", bare, []string{"commit", "-m", "x"}, func(t *testing.T) { pinIdentity(t, "1700000000 +0000") }},
		{"no identity", repo, []string{"commit", "-m", "x"}, func(t *testing.T) {
			for _, k := range []string{"MYGIT_AUTHOR_NAME", "MYGIT_AUTHOR_EMAIL", "GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL"} {
				t.Setenv(k, "")
			}
		}},
		{"bad date", repo, []string{"commit", "-m", "x"}, func(t *testing.T) {
			pinIdentity(t, "not-a-date")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.env(t)
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

func TestRevParseErrors(t *testing.T) {
	pinIdentity(t, "1700000000 +0000")
	dir := initRepo(t)

	for _, args := range [][]string{
		{"rev-parse"},
		{"rev-parse", "HEAD", "extra"},
		{"rev-parse", "no-such-branch"},
		{"rev-parse", "0000000000000000000000000000000000000000"},
		{"rev-parse", "--abbrev-ref", "main"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			if code, _, _ := run(t, dir, "", args...); code == 0 {
				t.Errorf("%v exited 0, want failure", args)
			}
		})
	}
}
