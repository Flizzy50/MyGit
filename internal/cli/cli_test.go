package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// run drives a command exactly as main would, but against buffers and a
// temporary directory, so end-to-end behaviour is testable without a shell.
func run(t *testing.T, dir, stdin string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errBuf bytes.Buffer
	env := &Env{
		Dir:    dir,
		Stdin:  strings.NewReader(stdin),
		Stdout: &out,
		Stderr: &errBuf,
	}
	code = Main(env, args)
	return code, out.String(), errBuf.String()
}

func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if code, _, stderr := run(t, dir, "", "init"); code != 0 {
		t.Fatalf("init failed (%d): %s", code, stderr)
	}
	return dir
}

// TestEndToEndHashAndRetrieve is the Phase 1-3 acceptance test: store content,
// get an ID back, and recover byte-identical content from that ID alone.
func TestEndToEndHashAndRetrieve(t *testing.T) {
	dir := initRepo(t)
	const content = "hello world"

	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte(content), 0o644); err != nil {
		t.Fatalf("writing file: %v", err)
	}

	code, stdout, stderr := run(t, dir, "", "hash-object", "-w", "file.txt")
	if code != 0 {
		t.Fatalf("hash-object failed (%d): %s", code, stderr)
	}
	oid := strings.TrimSpace(stdout)
	if oid != "95d09f2b10159347eece71399a7e2e907ea3df4f" {
		t.Fatalf("object id = %s, want real Git's id for %q", oid, content)
	}

	code, stdout, stderr = run(t, dir, "", "cat-file", "-p", oid)
	if code != 0 {
		t.Fatalf("cat-file -p failed (%d): %s", code, stderr)
	}
	if stdout != content {
		t.Errorf("cat-file -p = %q, want %q", stdout, content)
	}

	if _, stdout, _ = run(t, dir, "", "cat-file", "-t", oid); strings.TrimSpace(stdout) != "blob" {
		t.Errorf("cat-file -t = %q, want blob", stdout)
	}
	if _, stdout, _ = run(t, dir, "", "cat-file", "-s", oid); strings.TrimSpace(stdout) != "11" {
		t.Errorf("cat-file -s = %q, want 11", stdout)
	}
}

// TestCatFilePreservesBytesExactly guards against the classic mistake of
// adding a trailing newline on output, which would corrupt any file lacking
// one and make checkout produce files that differ from what was committed.
func TestCatFilePreservesBytesExactly(t *testing.T) {
	dir := initRepo(t)
	content := []byte{0x00, 0xff, 'n', 'o', ' ', 'n', 'e', 'w', 'l', 'i', 'n', 'e'}

	if err := os.WriteFile(filepath.Join(dir, "binary.dat"), content, 0o644); err != nil {
		t.Fatalf("writing file: %v", err)
	}

	_, stdout, _ := run(t, dir, "", "hash-object", "-w", "binary.dat")
	oid := strings.TrimSpace(stdout)

	code, stdout, stderr := run(t, dir, "", "cat-file", "-p", oid)
	if code != 0 {
		t.Fatalf("cat-file failed (%d): %s", code, stderr)
	}
	if stdout != string(content) {
		t.Errorf("cat-file -p = %q, want %q", stdout, content)
	}
}

func TestHashObjectFromStdin(t *testing.T) {
	dir := initRepo(t)

	code, stdout, stderr := run(t, dir, "hello world", "hash-object", "--stdin")
	if code != 0 {
		t.Fatalf("hash-object --stdin failed (%d): %s", code, stderr)
	}
	if got := strings.TrimSpace(stdout); got != "95d09f2b10159347eece71399a7e2e907ea3df4f" {
		t.Errorf("id = %s, want 95d09f2b10159347eece71399a7e2e907ea3df4f", got)
	}
}

// TestHashObjectWithoutWriteStoresNothing pins the separation between
// computing an ID (pure) and storing an object (a side effect).
func TestHashObjectWithoutWriteStoresNothing(t *testing.T) {
	dir := initRepo(t)

	code, stdout, stderr := run(t, dir, "hello world", "hash-object", "--stdin")
	if code != 0 {
		t.Fatalf("hash-object failed (%d): %s", code, stderr)
	}
	oid := strings.TrimSpace(stdout)

	if code, _, _ := run(t, dir, "", "cat-file", "-p", oid); code == 0 {
		t.Error("cat-file found an object that hash-object was told not to write")
	}
}

func TestHashObjectMultipleFiles(t *testing.T) {
	dir := initRepo(t)
	for _, name := range []string{"a.txt", "b.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o644); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
	}

	code, stdout, stderr := run(t, dir, "", "hash-object", "-w", "a.txt", "b.txt")
	if code != 0 {
		t.Fatalf("hash-object failed (%d): %s", code, stderr)
	}
	if lines := strings.Fields(stdout); len(lines) != 2 {
		t.Fatalf("got %d ids, want 2: %q", len(lines), stdout)
	}
}

// TestCommandsWorkFromSubdirectory exercises repository discovery through the
// CLI, the way a user actually hits it.
func TestCommandsWorkFromSubdirectory(t *testing.T) {
	dir := initRepo(t)
	sub := filepath.Join(dir, "src", "pkg")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("creating subdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sub, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("writing file: %v", err)
	}

	code, stdout, stderr := run(t, sub, "", "hash-object", "-w", "main.go")
	if code != 0 {
		t.Fatalf("hash-object from subdirectory failed (%d): %s", code, stderr)
	}

	oid := strings.TrimSpace(stdout)
	if code, _, stderr := run(t, sub, "", "cat-file", "-t", oid); code != 0 {
		t.Fatalf("cat-file from subdirectory failed (%d): %s", code, stderr)
	}
}

func TestInitReportsReinitialization(t *testing.T) {
	dir := t.TempDir()

	if _, stdout, _ := run(t, dir, "", "init"); !strings.Contains(stdout, "Initialized empty") {
		t.Errorf("first init said %q", stdout)
	}
	if _, stdout, _ := run(t, dir, "", "init"); !strings.Contains(stdout, "Reinitialized existing") {
		t.Errorf("second init said %q", stdout)
	}
}

func TestErrorPaths(t *testing.T) {
	repo := initRepo(t)
	bare := t.TempDir()

	cases := []struct {
		name string
		dir  string
		args []string
	}{
		{"unknown command", repo, []string{"frobnicate"}},
		{"write outside a repository", bare, []string{"hash-object", "-w", "--stdin"}},
		{"cat-file outside a repository", bare, []string{"cat-file", "-p", "95d09f2b10159347eece71399a7e2e907ea3df4f"}},
		{"cat-file with no mode", repo, []string{"cat-file", "95d09f2b10159347eece71399a7e2e907ea3df4f"}},
		{"cat-file with two modes", repo, []string{"cat-file", "-t", "-s", "95d09f2b10159347eece71399a7e2e907ea3df4f"}},
		{"cat-file with abbreviated id", repo, []string{"cat-file", "-p", "95d09f2b"}},
		{"cat-file on a missing object", repo, []string{"cat-file", "-p", "0000000000000000000000000000000000000000"}},
		{"hash-object with no input", repo, []string{"hash-object"}},
		{"hash-object with stdin and a file", repo, []string{"hash-object", "--stdin", "file.txt"}},
		{"hash-object with a bad type", repo, []string{"hash-object", "-t", "widget", "--stdin"}},
		{"hash-object on a missing file", repo, []string{"hash-object", "nonexistent.txt"}},
		{"init with too many arguments", repo, []string{"init", "a", "b"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, _, stderr := run(t, tc.dir, "", tc.args...)
			if code == 0 {
				t.Fatalf("%v exited 0, want failure", tc.args)
			}
			if stderr == "" {
				t.Error("failed without writing anything to stderr")
			}
		})
	}
}

func TestUsageOutput(t *testing.T) {
	dir := t.TempDir()

	code, stdout, _ := run(t, dir, "", "help")
	if code != 0 {
		t.Fatalf("help exited %d", code)
	}
	for _, name := range []string{"init", "hash-object", "cat-file"} {
		if !strings.Contains(stdout, name) {
			t.Errorf("help output omits %q", name)
		}
	}

	if code, stdout, _ := run(t, dir, ""); code != 0 || !strings.Contains(stdout, "usage:") {
		t.Errorf("bare invocation: code=%d stdout=%q", code, stdout)
	}
}
