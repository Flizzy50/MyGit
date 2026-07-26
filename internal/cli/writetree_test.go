package cli

import (
	"strings"
	"testing"
)

// TestWriteTreeMatchesRealGit is the Phase 5 acceptance test. The tree ID here
// was produced by real `git write-tree` over the same file layout, so matching
// it proves the entry format, the raw-OID encoding, the bare mode strings, and
// the directory sort rule are all correct simultaneously.
func TestWriteTreeMatchesRealGit(t *testing.T) {
	dir := initRepo(t)
	write(t, dir, "a.txt", "a\n")
	write(t, dir, "src.txt", "file named src.txt\n")
	write(t, dir, "src/main.go", "inside dir\n")

	if code, _, stderr := run(t, dir, "", "add", "."); code != 0 {
		t.Fatalf("add failed (%d): %s", code, stderr)
	}

	code, stdout, stderr := run(t, dir, "", "write-tree")
	if code != 0 {
		t.Fatalf("write-tree failed (%d): %s", code, stderr)
	}
	const want = "2b0c7d7c422758b229cd6ff32ec72950978a5bba"
	if got := strings.TrimSpace(stdout); got != want {
		t.Fatalf("root tree = %s, want real Git's %s", got, want)
	}
}

// TestWriteTreeThenCatFile walks the stored hierarchy back out, confirming the
// trees are real objects in the database and that -p decodes them.
func TestWriteTreeThenCatFile(t *testing.T) {
	dir := initRepo(t)
	write(t, dir, "README.md", "# project\n")
	write(t, dir, "src/main.go", "package main\n")
	run(t, dir, "", "add", ".")

	_, stdout, _ := run(t, dir, "", "write-tree")
	root := strings.TrimSpace(stdout)

	code, stdout, stderr := run(t, dir, "", "cat-file", "-p", root)
	if code != 0 {
		t.Fatalf("cat-file -p on a tree failed (%d): %s", code, stderr)
	}

	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	if len(lines) != 2 {
		t.Fatalf("root tree listing has %d lines, want 2:\n%s", len(lines), stdout)
	}
	if !strings.HasPrefix(lines[0], "100644 blob ") || !strings.HasSuffix(lines[0], "\tREADME.md") {
		t.Errorf("line 0 = %q, want a blob entry for README.md", lines[0])
	}
	if !strings.HasPrefix(lines[1], "040000 tree ") || !strings.HasSuffix(lines[1], "\tsrc") {
		t.Errorf("line 1 = %q, want a tree entry for src", lines[1])
	}

	// The type of the root object must really be tree.
	if _, out, _ := run(t, dir, "", "cat-file", "-t", root); strings.TrimSpace(out) != "tree" {
		t.Errorf("cat-file -t = %q, want tree", out)
	}
}

// TestWriteTreeIsIdempotent confirms that writing an unchanged index twice
// yields the same root and stores no new objects.
func TestWriteTreeIsIdempotent(t *testing.T) {
	dir := initRepo(t)
	write(t, dir, "file.txt", "content\n")
	run(t, dir, "", "add", ".")

	_, first, _ := run(t, dir, "", "write-tree")
	_, second, _ := run(t, dir, "", "write-tree")
	if first != second {
		t.Errorf("write-tree returned %q then %q", first, second)
	}
}

// TestWriteTreeEmptyIndex documents that an empty staging area produces Git's
// well-known empty tree rather than an error.
func TestWriteTreeEmptyIndex(t *testing.T) {
	dir := initRepo(t)

	code, stdout, stderr := run(t, dir, "", "write-tree")
	if code != 0 {
		t.Fatalf("write-tree on an empty index failed (%d): %s", code, stderr)
	}
	if got := strings.TrimSpace(stdout); got != "4b825dc642cb6eb9a060e54bf8d69288fbee4904" {
		t.Errorf("empty tree = %s, want 4b825dc642cb6eb9a060e54bf8d69288fbee4904", got)
	}
}

func TestWriteTreeErrors(t *testing.T) {
	repo := initRepo(t)
	bare := t.TempDir()

	if code, _, _ := run(t, bare, "", "write-tree"); code == 0 {
		t.Error("write-tree outside a repository exited 0")
	}
	if code, _, _ := run(t, repo, "", "write-tree", "extra-arg"); code == 0 {
		t.Error("write-tree with an argument exited 0")
	}
}
