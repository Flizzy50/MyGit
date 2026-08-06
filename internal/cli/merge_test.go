package cli

import (
	"strings"
	"testing"
)

// setupDivergent builds two branches that both changed since a shared base, and
// returns the repo directory.
func setupDivergent(t *testing.T, baseFiles, ourFiles, theirFiles map[string]string) string {
	t.Helper()
	dir := initRepo(t)

	pinIdentity(t, "1700000000 +0000")
	for path, content := range baseFiles {
		write(t, dir, path, content)
	}
	run(t, dir, "", "add", ".")
	run(t, dir, "", "commit", "-m", "base")

	run(t, dir, "", "checkout", "-b", "feature")
	pinIdentity(t, "1700000120 +0000")
	for path, content := range theirFiles {
		write(t, dir, path, content)
	}
	run(t, dir, "", "add", ".")
	run(t, dir, "", "commit", "-m", "their work")

	run(t, dir, "", "checkout", "main")
	pinIdentity(t, "1700000060 +0000")
	for path, content := range ourFiles {
		write(t, dir, path, content)
	}
	run(t, dir, "", "add", ".")
	run(t, dir, "", "commit", "-m", "our work")

	pinIdentity(t, "1700000180 +0000")
	return dir
}

// TestMergeFastForward covers the cheapest case: our tip is an ancestor of
// theirs, so the branch pointer just moves. No merge commit is created because
// one would record nothing.
func TestMergeFastForward(t *testing.T) {
	dir := initRepo(t)
	commitFile(t, dir, "f.txt", "base", "base", "1700000000 +0000")

	run(t, dir, "", "checkout", "-b", "feature")
	featTip := commitFile(t, dir, "f.txt", "feature work", "feature", "1700000060 +0000")
	run(t, dir, "", "checkout", "main")

	pinIdentity(t, "1700000120 +0000")
	code, stdout, stderr := run(t, dir, "", "merge", "feature")
	if code != 0 {
		t.Fatalf("merge failed (%d): %s", code, stderr)
	}
	if !strings.Contains(stdout, "Fast-forward") {
		t.Errorf("stdout = %q, want a fast-forward", stdout)
	}

	// main now points exactly at the feature tip: no new commit was made.
	if _, out, _ := run(t, dir, "", "rev-parse", "HEAD"); strings.TrimSpace(out) != featTip {
		t.Errorf("HEAD = %q, want %s", out, featTip)
	}
	if got := readFile(t, dir, "f.txt"); got != "feature work" {
		t.Errorf("f.txt = %q, want the feature content", got)
	}
	// History stays linear.
	_, log, _ := run(t, dir, "", "log", "--oneline")
	if strings.Contains(log, "Merge") {
		t.Errorf("a merge commit was created for a fast-forward:\n%s", log)
	}
}

func TestMergeAlreadyUpToDate(t *testing.T) {
	dir := initRepo(t)
	commitFile(t, dir, "f.txt", "base", "base", "1700000000 +0000")
	run(t, dir, "", "branch", "feature")
	tip := commitFile(t, dir, "f.txt", "more", "main work", "1700000060 +0000")

	pinIdentity(t, "1700000120 +0000")
	code, stdout, stderr := run(t, dir, "", "merge", "feature")
	if code != 0 {
		t.Fatalf("merge failed (%d): %s", code, stderr)
	}
	if !strings.Contains(stdout, "Already up to date") {
		t.Errorf("stdout = %q", stdout)
	}
	if _, out, _ := run(t, dir, "", "rev-parse", "HEAD"); strings.TrimSpace(out) != tip {
		t.Error("HEAD moved during an up-to-date merge")
	}
}

// TestMergeDifferentFilesCleanly is the everyday successful merge: two sides
// touched disjoint files, so the result is simply the union.
func TestMergeDifferentFilesCleanly(t *testing.T) {
	dir := setupDivergent(t,
		map[string]string{"shared.txt": "base content\n"},
		map[string]string{"ours.txt": "our new file\n"},
		map[string]string{"theirs.txt": "their new file\n"},
	)

	code, stdout, stderr := run(t, dir, "", "merge", "feature")
	if code != 0 {
		t.Fatalf("merge failed (%d): %s", code, stderr)
	}
	if !strings.Contains(stdout, "Merge made by the three-way strategy") {
		t.Errorf("stdout = %q", stdout)
	}

	// All three files present.
	for path, want := range map[string]string{
		"shared.txt": "base content\n",
		"ours.txt":   "our new file\n",
		"theirs.txt": "their new file\n",
	} {
		if got := readFile(t, dir, path); got != want {
			t.Errorf("%s = %q, want %q", path, got, want)
		}
	}

	// The merge commit has two parents, making history a DAG.
	_, out, _ := run(t, dir, "", "rev-parse", "HEAD")
	_, body, _ := run(t, dir, "", "cat-file", "-p", strings.TrimSpace(out))
	if n := strings.Count(body, "parent "); n != 2 {
		t.Errorf("merge commit has %d parents, want 2:\n%s", n, body)
	}

	// Both branches' work is reachable from the merge.
	_, log, _ := run(t, dir, "", "log", "--oneline")
	for _, want := range []string{"our work", "their work", "base"} {
		if !strings.Contains(log, want) {
			t.Errorf("history is missing %q:\n%s", want, log)
		}
	}
	if strings.Count(log, "base") != 1 {
		t.Errorf("the shared base appears more than once:\n%s", log)
	}
}

// TestMergeSameFileDifferentRegions is the case that justifies three-way
// merging at all: both sides edited one file and it still resolves.
func TestMergeSameFileDifferentRegions(t *testing.T) {
	dir := setupDivergent(t,
		map[string]string{"f.txt": "line one\nline two\nline three\nline four\nline five"},
		map[string]string{"f.txt": "OUR FIRST LINE\nline two\nline three\nline four\nline five"},
		map[string]string{"f.txt": "line one\nline two\nline three\nline four\nTHEIR LAST LINE"},
	)

	code, _, stderr := run(t, dir, "", "merge", "feature")
	if code != 0 {
		t.Fatalf("merge failed (%d): %s", code, stderr)
	}

	got := readFile(t, dir, "f.txt")
	want := "OUR FIRST LINE\nline two\nline three\nline four\nTHEIR LAST LINE"
	if got != want {
		t.Errorf("merged file =\n%q\nwant\n%q", got, want)
	}
}

// TestMergeConflict is the failure path, and checks all of it: markers in the
// file, stages in the index, a nonzero exit, and a refusal to commit.
func TestMergeConflict(t *testing.T) {
	dir := setupDivergent(t,
		map[string]string{"f.txt": "start\noriginal line\nend"},
		map[string]string{"f.txt": "start\nour version\nend"},
		map[string]string{"f.txt": "start\ntheir version\nend"},
	)

	code, _, stderr := run(t, dir, "", "merge", "feature")
	if code == 0 {
		t.Fatal("a conflicting merge exited 0")
	}
	if !strings.Contains(stderr, "CONFLICT") {
		t.Errorf("stderr = %q, want a CONFLICT report", stderr)
	}

	// The working file shows all three versions.
	content := readFile(t, dir, "f.txt")
	for _, want := range []string{"<<<<<<<", "our version", "|||||||", "original line", "=======", "their version", ">>>>>>>"} {
		if !strings.Contains(content, want) {
			t.Errorf("conflicted file missing %q:\n%s", want, content)
		}
	}

	// The index carries stages 1, 2, and 3 for the path.
	_, listing, _ := run(t, dir, "", "ls-files", "-s")
	for _, stage := range []string{"1\tf.txt", "2\tf.txt", "3\tf.txt"} {
		if !strings.Contains(listing, stage) {
			t.Errorf("index missing stage entry %q:\n%s", stage, listing)
		}
	}
	if strings.Contains(listing, "0\tf.txt") {
		t.Errorf("the conflicted path still has a stage-zero entry:\n%s", listing)
	}

	// Committing is refused while stages remain.
	pinIdentity(t, "1700000240 +0000")
	if code, _, stderr := run(t, dir, "", "commit", "-m", "premature"); code == 0 {
		t.Error("commit succeeded with unresolved conflicts")
	} else if !strings.Contains(stderr, "unresolved conflicts") {
		t.Errorf("commit error = %q", stderr)
	}

	// A second merge is refused too.
	if code, _, _ := run(t, dir, "", "merge", "feature"); code == 0 {
		t.Error("a second merge started while one was unresolved")
	}
}

// TestResolveConflictThenCommit completes the workflow: editing the file and
// staging it clears the stages and lets the merge be recorded.
func TestResolveConflictThenCommit(t *testing.T) {
	dir := setupDivergent(t,
		map[string]string{"f.txt": "start\noriginal\nend"},
		map[string]string{"f.txt": "start\nours\nend"},
		map[string]string{"f.txt": "start\ntheirs\nend"},
	)
	run(t, dir, "", "merge", "feature")

	// Resolve by hand.
	write(t, dir, "f.txt", "start\nreconciled by hand\nend")
	if code, _, stderr := run(t, dir, "", "add", "f.txt"); code != 0 {
		t.Fatalf("add failed (%d): %s", code, stderr)
	}

	_, listing, _ := run(t, dir, "", "ls-files", "-s")
	if !strings.Contains(listing, "0\tf.txt") {
		t.Errorf("staging did not produce a stage-zero entry:\n%s", listing)
	}
	for _, stage := range []string{"1\tf.txt", "2\tf.txt", "3\tf.txt"} {
		if strings.Contains(listing, stage) {
			t.Errorf("stage %q survived resolution:\n%s", stage, listing)
		}
	}

	pinIdentity(t, "1700000240 +0000")
	if code, _, stderr := run(t, dir, "", "commit", "-m", "resolve conflict"); code != 0 {
		t.Fatalf("commit after resolution failed (%d): %s", code, stderr)
	}
	if got := readFile(t, dir, "f.txt"); got != "start\nreconciled by hand\nend" {
		t.Errorf("f.txt = %q", got)
	}

	// The resolving commit must be a real merge commit. Without MERGE_HEAD it
	// would have a single parent, and the incoming branch would silently
	// disappear from history while its changes sat in the working tree.
	_, out, _ := run(t, dir, "", "rev-parse", "HEAD")
	_, body, _ := run(t, dir, "", "cat-file", "-p", strings.TrimSpace(out))
	if n := strings.Count(body, "parent "); n != 2 {
		t.Fatalf("resolving commit has %d parents, want 2:\n%s", n, body)
	}

	// Both branches' work is reachable, which is the point of the second parent.
	_, log, _ := run(t, dir, "", "log", "--oneline")
	for _, want := range []string{"resolve conflict", "our work", "their work", "base"} {
		if !strings.Contains(log, want) {
			t.Errorf("history is missing %q:\n%s", want, log)
		}
	}

	// The merge state is cleared, so the next commit is ordinary again.
	if exists(dir, ".mygit/MERGE_HEAD") {
		t.Error("MERGE_HEAD survived the merge commit")
	}
	if exists(dir, ".mygit/MERGE_MSG") {
		t.Error("MERGE_MSG survived the merge commit")
	}
}

// TestMergeStateBlocksSecondMerge covers the case where every conflict was
// resolved but the merge was never committed: the index looks clean, yet the
// merge is still open and its second parent must not be lost.
func TestMergeStateBlocksSecondMerge(t *testing.T) {
	dir := setupDivergent(t,
		map[string]string{"f.txt": "start\noriginal\nend"},
		map[string]string{"f.txt": "start\nours\nend"},
		map[string]string{"f.txt": "start\ntheirs\nend"},
	)
	run(t, dir, "", "merge", "feature")

	// Resolve everything, but do not commit.
	write(t, dir, "f.txt", "start\nresolved\nend")
	run(t, dir, "", "add", "f.txt")

	code, _, stderr := run(t, dir, "", "merge", "feature")
	if code == 0 {
		t.Fatal("a second merge started while one was still open")
	}
	if !strings.Contains(stderr, "already in progress") {
		t.Errorf("stderr = %q, want it to report the open merge", stderr)
	}
}

// TestResolveInFavourOfOursStillCommits checks the case where resolution makes
// the tree identical to HEAD's. The "nothing to commit" guard must not fire,
// because the merge commit is meaningful even with an unchanged tree.
func TestResolveInFavourOfOursStillCommits(t *testing.T) {
	dir := setupDivergent(t,
		map[string]string{"f.txt": "start\noriginal\nend"},
		map[string]string{"f.txt": "start\nours\nend"},
		map[string]string{"f.txt": "start\ntheirs\nend"},
	)
	run(t, dir, "", "merge", "feature")

	// Resolve by keeping our version exactly, so the tree matches HEAD's.
	write(t, dir, "f.txt", "start\nours\nend")
	run(t, dir, "", "add", "f.txt")

	pinIdentity(t, "1700000240 +0000")
	if code, _, stderr := run(t, dir, "", "commit", "-m", "keep ours"); code != 0 {
		t.Fatalf("commit failed (%d): %s", code, stderr)
	}

	_, out, _ := run(t, dir, "", "rev-parse", "HEAD")
	_, body, _ := run(t, dir, "", "cat-file", "-p", strings.TrimSpace(out))
	if n := strings.Count(body, "parent "); n != 2 {
		t.Errorf("commit has %d parents, want 2:\n%s", n, body)
	}
}

// TestMergeModifyDeleteConflict covers the case content analysis cannot settle:
// the two sides disagree about whether the file should exist.
func TestMergeModifyDeleteConflict(t *testing.T) {
	dir := initRepo(t)
	pinIdentity(t, "1700000000 +0000")
	write(t, dir, "keep.txt", "anchor\n")
	write(t, dir, "doomed.txt", "original\n")
	run(t, dir, "", "add", ".")
	run(t, dir, "", "commit", "-m", "base")

	// Theirs modifies the file.
	run(t, dir, "", "checkout", "-b", "feature")
	pinIdentity(t, "1700000120 +0000")
	write(t, dir, "doomed.txt", "modified by them\n")
	run(t, dir, "", "add", ".")
	run(t, dir, "", "commit", "-m", "modify")

	// Ours deletes it, staged by rebuilding the index from scratch.
	run(t, dir, "", "checkout", "main")
	pinIdentity(t, "1700000060 +0000")
	rmFile(t, dir, "doomed.txt")
	rmIndex(t, dir)
	run(t, dir, "", "add", ".")
	run(t, dir, "", "commit", "-m", "delete")

	pinIdentity(t, "1700000180 +0000")
	code, _, stderr := run(t, dir, "", "merge", "feature")
	if code == 0 {
		t.Fatal("a modify/delete conflict merged cleanly")
	}
	if !strings.Contains(stderr, "deleted by us") {
		t.Errorf("stderr = %q, want a modify/delete report", stderr)
	}
}

// TestMergeIdenticalChangesNoConflict confirms two people making the same edit
// is not treated as a conflict.
func TestMergeIdenticalChangesNoConflict(t *testing.T) {
	dir := setupDivergent(t,
		map[string]string{"f.txt": "a\nold\nc"},
		map[string]string{"f.txt": "a\nnew\nc"},
		map[string]string{"f.txt": "a\nnew\nc"},
	)

	code, _, stderr := run(t, dir, "", "merge", "feature")
	if code != 0 {
		t.Fatalf("identical edits conflicted (%d): %s", code, stderr)
	}
	if got := readFile(t, dir, "f.txt"); got != "a\nnew\nc" {
		t.Errorf("f.txt = %q, want the change applied once", got)
	}
}

func TestMergeErrors(t *testing.T) {
	bare := t.TempDir()
	empty := initRepo(t)
	repo := initRepo(t)
	commitFile(t, repo, "f.txt", "x", "first", "1700000000 +0000")

	cases := []struct {
		name string
		dir  string
		args []string
	}{
		{"outside a repository", bare, []string{"merge", "main"}},
		{"unborn HEAD", empty, []string{"merge", "main"}},
		{"no argument", repo, []string{"merge"}},
		{"two arguments", repo, []string{"merge", "a", "b"}},
		{"unknown branch", repo, []string{"merge", "no-such-branch"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pinIdentity(t, "1700000000 +0000")
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
