package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// commitFile stages a file and commits it at a pinned timestamp, returning the
// new commit ID.
func commitFile(t *testing.T, dir, name, content, msg string, date string) string {
	t.Helper()
	pinIdentity(t, date)
	write(t, dir, name, content)
	if code, _, stderr := run(t, dir, "", "add", name); code != 0 {
		t.Fatalf("add %s failed (%d): %s", name, code, stderr)
	}
	if code, _, stderr := run(t, dir, "", "commit", "-m", msg); code != 0 {
		t.Fatalf("commit %q failed (%d): %s", msg, code, stderr)
	}
	_, out, _ := run(t, dir, "", "rev-parse", "HEAD")
	return strings.TrimSpace(out)
}

func TestLogNewestFirst(t *testing.T) {
	dir := initRepo(t)
	commitFile(t, dir, "f.txt", "one", "first commit", "1700000000 +0000")
	commitFile(t, dir, "f.txt", "two", "second commit", "1700000060 +0000")
	commitFile(t, dir, "f.txt", "three", "third commit", "1700000120 +0000")

	code, stdout, stderr := run(t, dir, "", "log", "--oneline")
	if code != 0 {
		t.Fatalf("log failed (%d): %s", code, stderr)
	}

	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3:\n%s", len(lines), stdout)
	}
	for i, want := range []string{"third commit", "second commit", "first commit"} {
		if !strings.HasSuffix(lines[i], want) {
			t.Errorf("line %d = %q, want it to end with %q", i, lines[i], want)
		}
	}
}

// TestLogFullFormat pins the default output shape against real Git's.
//
// The date string is Git's own rendering of Unix 1700000000 at +0530, verified
// with `git log --date=default`. Note it is the *author's* local wall clock,
// not UTC: the same instant is 22:13 in UTC but 03:43 the next day in +0530,
// which is precisely the detail the stored offset exists to preserve.
func TestLogFullFormat(t *testing.T) {
	dir := initRepo(t)
	oid := commitFile(t, dir, "f.txt", "x", "Subject line", "1700000000 +0530")

	code, stdout, stderr := run(t, dir, "", "log")
	if code != 0 {
		t.Fatalf("log failed (%d): %s", code, stderr)
	}

	want := "commit " + oid + "\n" +
		"Author: Test Author <author@example.com>\n" +
		"Date:   Wed Nov 15 03:43:20 2023 +0530\n" +
		"\n" +
		"    Subject line\n"
	if stdout != want {
		t.Errorf("log output:\n%q\nwant:\n%q", stdout, want)
	}
}

// TestLogMultiParagraphMessage checks the four-space indent and that blank
// lines inside a message stay blank rather than becoming whitespace-only lines.
func TestLogMultiParagraphMessage(t *testing.T) {
	dir := initRepo(t)
	pinIdentity(t, "1700000000 +0000")
	write(t, dir, "f.txt", "x")
	run(t, dir, "", "add", "f.txt")
	run(t, dir, "", "commit", "-m", "Subject\n\nBody paragraph.\n")

	_, stdout, _ := run(t, dir, "", "log")
	if !strings.Contains(stdout, "    Subject\n\n    Body paragraph.\n") {
		t.Errorf("message not indented as expected:\n%q", stdout)
	}
	if strings.Contains(stdout, "    \n") {
		t.Error("a blank message line was padded with trailing whitespace")
	}
}

func TestLogLimit(t *testing.T) {
	dir := initRepo(t)
	for i := 0; i < 5; i++ {
		commitFile(t, dir, "f.txt", strings.Repeat("x", i+1),
			"commit "+string(rune('A'+i)), "17000000"+string(rune('0'+i))+"0 +0000")
	}

	_, stdout, _ := run(t, dir, "", "log", "--oneline", "-n", "2")
	if lines := strings.Split(strings.TrimSpace(stdout), "\n"); len(lines) != 2 {
		t.Errorf("got %d lines with -n 2:\n%s", len(lines), stdout)
	}

	// A limit larger than history is not an error; it just shows everything.
	_, stdout, _ = run(t, dir, "", "log", "--oneline", "-n", "100")
	if lines := strings.Split(strings.TrimSpace(stdout), "\n"); len(lines) != 5 {
		t.Errorf("got %d lines with -n 100, want 5", len(lines))
	}
}

// TestLogFromRevision confirms history can be walked from any starting point,
// not only HEAD — the same traversal, a different root.
func TestLogFromRevision(t *testing.T) {
	dir := initRepo(t)
	first := commitFile(t, dir, "f.txt", "one", "first", "1700000000 +0000")
	commitFile(t, dir, "f.txt", "two", "second", "1700000060 +0000")
	commitFile(t, dir, "f.txt", "three", "third", "1700000120 +0000")

	_, stdout, _ := run(t, dir, "", "log", "--oneline", first)
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	if len(lines) != 1 || !strings.HasSuffix(lines[0], "first") {
		t.Errorf("log from the root commit showed:\n%s", stdout)
	}
}

// TestLogMergeCommit builds a genuine merge by hand — Phase 11 does not exist
// yet — and checks that log reports both parents and visits the shared
// ancestor exactly once.
func TestLogMergeCommit(t *testing.T) {
	dir := initRepo(t)
	base := commitFile(t, dir, "f.txt", "base", "base commit", "1700000000 +0000")

	// Two children of the same base, built by resetting the branch pointer
	// between commits.
	left := commitFile(t, dir, "f.txt", "left side", "left commit", "1700000060 +0000")
	setBranch(t, dir, base)
	right := commitFile(t, dir, "g.txt", "right side", "right commit", "1700000120 +0000")

	// Hand-assemble the merge commit object and point the branch at it.
	_, treeOut, _ := run(t, dir, "", "write-tree")
	tree := strings.TrimSpace(treeOut)
	payload := "tree " + tree + "\n" +
		"parent " + right + "\n" +
		"parent " + left + "\n" +
		"author Test Author <author@example.com> 1700000180 +0000\n" +
		"committer Test Author <author@example.com> 1700000180 +0000\n" +
		"\nMerge left into right\n"

	code, out, stderr := run(t, dir, payload, "hash-object", "-t", "commit", "-w", "--stdin")
	if code != 0 {
		t.Fatalf("writing merge commit failed (%d): %s", code, stderr)
	}
	merge := strings.TrimSpace(out)
	setBranch(t, dir, merge)

	code, stdout, stderr := run(t, dir, "", "log")
	if code != 0 {
		t.Fatalf("log failed (%d): %s", code, stderr)
	}

	if !strings.Contains(stdout, "Merge: "+right[:7]+" "+left[:7]) {
		t.Errorf("merge line missing or wrong:\n%s", stdout)
	}

	// Four commits total, and the shared base appears exactly once.
	_, oneline, _ := run(t, dir, "", "log", "--oneline")
	lines := strings.Split(strings.TrimSpace(oneline), "\n")
	if len(lines) != 4 {
		t.Fatalf("got %d commits, want 4:\n%s", len(lines), oneline)
	}
	if got := strings.Count(oneline, "base commit"); got != 1 {
		t.Errorf("shared ancestor appeared %d times, want 1:\n%s", got, oneline)
	}

	// Newest first: merge, then right (newer), then left, then base.
	for i, want := range []string{"Merge left into right", "right commit", "left commit", "base commit"} {
		if !strings.HasSuffix(lines[i], want) {
			t.Errorf("line %d = %q, want it to end with %q", i, lines[i], want)
		}
	}
}

// setBranch points refs/heads/main at a commit, standing in for the reset and
// branch commands that arrive in later phases.
func setBranch(t *testing.T, dir, oid string) {
	t.Helper()
	path := filepath.Join(dir, ".mygit", "refs", "heads", "main")
	if err := os.WriteFile(path, []byte(oid+"\n"), 0o644); err != nil {
		t.Fatalf("updating branch: %v", err)
	}
}

func TestLogErrors(t *testing.T) {
	bare := t.TempDir()
	empty := initRepo(t)

	populated := initRepo(t)
	commitFile(t, populated, "f.txt", "x", "only", "1700000000 +0000")

	cases := []struct {
		name string
		dir  string
		args []string
	}{
		{"outside a repository", bare, []string{"log"}},
		{"unborn HEAD", empty, []string{"log"}},
		{"unknown revision", populated, []string{"log", "no-such-branch"}},
		{"too many revisions", populated, []string{"log", "HEAD", "HEAD"}},
		{"negative limit", populated, []string{"log", "-n", "-1"}},
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
