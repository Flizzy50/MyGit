package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, dir, rel, content string) {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing %s: %v", rel, err)
	}
}

// TestAddStagesWithRealGitBlobID is the acceptance test for Phase 4: after
// `add`, the index records the same blob ID real Git would compute, and the
// blob itself is stored so cat-file can recover it.
func TestAddStagesWithRealGitBlobID(t *testing.T) {
	dir := initRepo(t)
	write(t, dir, "file.txt", "hello world")

	if code, _, stderr := run(t, dir, "", "add", "file.txt"); code != 0 {
		t.Fatalf("add failed (%d): %s", code, stderr)
	}

	code, stdout, stderr := run(t, dir, "", "ls-files", "-s")
	if code != 0 {
		t.Fatalf("ls-files failed (%d): %s", code, stderr)
	}
	want := "100644 95d09f2b10159347eece71399a7e2e907ea3df4f 0\tfile.txt\n"
	if stdout != want {
		t.Fatalf("ls-files -s = %q, want %q", stdout, want)
	}

	// The blob must actually be in the object store, not merely referenced.
	if code, out, _ := run(t, dir, "", "cat-file", "-p", "95d09f2b10159347eece71399a7e2e907ea3df4f"); code != 0 || out != "hello world" {
		t.Errorf("staged blob not retrievable: code=%d out=%q", code, out)
	}
}

// TestAddDirectoryRecurses checks that staging a directory stages every file
// beneath it, with work-tree-relative, slash-separated paths.
func TestAddDirectoryRecurses(t *testing.T) {
	dir := initRepo(t)
	write(t, dir, "src/main.go", "package main\n")
	write(t, dir, "src/util/helper.go", "package util\n")
	write(t, dir, "README.md", "# project\n")

	if code, _, stderr := run(t, dir, "", "add", "."); code != 0 {
		t.Fatalf("add . failed (%d): %s", code, stderr)
	}

	_, stdout, _ := run(t, dir, "", "ls-files")
	got := strings.Fields(stdout)
	want := []string{"README.md", "src/main.go", "src/util/helper.go"}
	if len(got) != len(want) {
		t.Fatalf("staged %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("path %d = %q, want %q (sorted, forward slashes)", i, got[i], want[i])
		}
	}
}

// TestAddSkipsMygitDir guards against the recursion swallowing the repository's
// own metadata directory.
func TestAddSkipsMygitDir(t *testing.T) {
	dir := initRepo(t)
	write(t, dir, "keep.txt", "content")

	if code, _, stderr := run(t, dir, "", "add", "."); code != 0 {
		t.Fatalf("add . failed (%d): %s", code, stderr)
	}

	_, stdout, _ := run(t, dir, "", "ls-files")
	if strings.Contains(stdout, ".mygit") {
		t.Errorf("index contains .mygit internals:\n%s", stdout)
	}
}

// TestReAddUpdatesEntry confirms that editing and re-adding a file replaces the
// staged version rather than duplicating the path.
func TestReAddUpdatesEntry(t *testing.T) {
	dir := initRepo(t)
	write(t, dir, "file.txt", "version one")
	run(t, dir, "", "add", "file.txt")

	write(t, dir, "file.txt", "version two is longer")
	if code, _, stderr := run(t, dir, "", "add", "file.txt"); code != 0 {
		t.Fatalf("second add failed (%d): %s", code, stderr)
	}

	_, stdout, _ := run(t, dir, "", "ls-files", "-s")
	if lines := strings.Count(stdout, "\n"); lines != 1 {
		t.Fatalf("index has %d entries, want 1:\n%s", lines, stdout)
	}
	// The staged OID must reflect the new content, not the old.
	if strings.Contains(stdout, "95d09f2b") {
		t.Error("index still references the old blob after re-add")
	}
}

// TestAddIsAtomicOnError checks the batch-save behavior: if any argument fails,
// the on-disk index is left untouched rather than half-updated.
func TestAddIsAtomicOnError(t *testing.T) {
	dir := initRepo(t)
	write(t, dir, "good.txt", "fine")

	code, _, _ := run(t, dir, "", "add", "good.txt", "missing.txt")
	if code == 0 {
		t.Fatal("add with a missing path unexpectedly succeeded")
	}

	// Because the save happens once at the end, the failure means nothing was
	// written — good.txt is not staged either.
	_, stdout, _ := run(t, dir, "", "ls-files")
	if strings.TrimSpace(stdout) != "" {
		t.Errorf("index was modified despite the failed batch:\n%s", stdout)
	}
}

func TestAddEmptyFile(t *testing.T) {
	dir := initRepo(t)
	write(t, dir, "empty.txt", "")

	if code, _, stderr := run(t, dir, "", "add", "empty.txt"); code != 0 {
		t.Fatalf("add of empty file failed (%d): %s", code, stderr)
	}

	_, stdout, _ := run(t, dir, "", "ls-files", "-s")
	// The empty blob has a well-known ID, identical to real Git's.
	if !strings.Contains(stdout, "e69de29bb2d1d6434b8b29ae775ad8c2e48c5391") {
		t.Errorf("empty file staged with wrong blob id:\n%s", stdout)
	}
}

func TestAddFromSubdirectory(t *testing.T) {
	dir := initRepo(t)
	write(t, dir, "src/main.go", "package main\n")
	sub := filepath.Join(dir, "src")

	if code, _, stderr := run(t, sub, "", "add", "main.go"); code != 0 {
		t.Fatalf("add from subdirectory failed (%d): %s", code, stderr)
	}

	// The stored path is relative to the work tree root, not the cwd.
	_, stdout, _ := run(t, sub, "", "ls-files")
	if strings.TrimSpace(stdout) != "src/main.go" {
		t.Errorf("path = %q, want src/main.go", strings.TrimSpace(stdout))
	}
}

func TestAddErrors(t *testing.T) {
	repo := initRepo(t)
	bare := t.TempDir()

	cases := []struct {
		name string
		dir  string
		args []string
	}{
		{"no arguments", repo, []string{"add"}},
		{"missing file", repo, []string{"add", "nonexistent.txt"}},
		{"outside a repository", bare, []string{"add", "whatever.txt"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if code, _, _ := run(t, tc.dir, "", tc.args...); code == 0 {
				t.Fatalf("%v exited 0, want failure", tc.args)
			}
		})
	}
}
