package repository

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestInitCreatesLayout(t *testing.T) {
	root := t.TempDir()

	repo, existed, err := Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if existed {
		t.Error("Init reported an existing repository in an empty directory")
	}

	for _, dir := range []string{"objects", "refs", "refs/heads", "refs/tags"} {
		info, err := os.Stat(repo.Path(filepath.FromSlash(dir)))
		if err != nil {
			t.Errorf("missing %s: %v", dir, err)
			continue
		}
		if !info.IsDir() {
			t.Errorf("%s exists but is not a directory", dir)
		}
	}

	head, err := os.ReadFile(repo.Path("HEAD"))
	if err != nil {
		t.Fatalf("reading HEAD: %v", err)
	}
	if want := "ref: refs/heads/" + DefaultBranch + "\n"; string(head) != want {
		t.Errorf("HEAD = %q, want %q", head, want)
	}
}

// TestInitLeavesDefaultBranchUnborn documents the unborn-branch state: HEAD
// names a branch whose ref file does not exist yet. The first commit creates
// it. Anything that resolves HEAD must handle this.
func TestInitLeavesDefaultBranchUnborn(t *testing.T) {
	repo, _, err := Init(t.TempDir())
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	branch := repo.Path("refs", "heads", DefaultBranch)
	if _, err := os.Stat(branch); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("refs/heads/%s should not exist before the first commit", DefaultBranch)
	}
}

// TestInitIsIdempotent checks that reinitializing repairs the layout without
// disturbing HEAD — clobbering HEAD would silently move the user off whatever
// branch or detached commit they had checked out.
func TestInitIsIdempotent(t *testing.T) {
	root := t.TempDir()

	repo, _, err := Init(root)
	if err != nil {
		t.Fatalf("first Init: %v", err)
	}

	custom := "ref: refs/heads/feature\n"
	if err := os.WriteFile(repo.Path("HEAD"), []byte(custom), 0o644); err != nil {
		t.Fatalf("rewriting HEAD: %v", err)
	}
	if err := os.RemoveAll(repo.Path("refs", "tags")); err != nil {
		t.Fatalf("removing refs/tags: %v", err)
	}

	if _, existed, err := Init(root); err != nil {
		t.Fatalf("second Init: %v", err)
	} else if !existed {
		t.Error("second Init did not report an existing repository")
	}

	if head, err := os.ReadFile(repo.Path("HEAD")); err != nil {
		t.Fatalf("reading HEAD: %v", err)
	} else if string(head) != custom {
		t.Errorf("reinit overwrote HEAD: got %q, want %q", head, custom)
	}
	if _, err := os.Stat(repo.Path("refs", "tags")); err != nil {
		t.Errorf("reinit did not restore refs/tags: %v", err)
	}
}

func TestInitRejectsFileAtGitDir(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, DirName), []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("writing decoy: %v", err)
	}
	if _, _, err := Init(root); err == nil {
		t.Fatal("Init succeeded despite .mygit being a regular file")
	}
}

// TestDiscoverWalksUp is why mygit commands work from any subdirectory.
func TestDiscoverWalksUp(t *testing.T) {
	root := t.TempDir()
	if _, _, err := Init(root); err != nil {
		t.Fatalf("Init: %v", err)
	}

	deep := filepath.Join(root, "src", "internal", "deep")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatalf("creating nested dirs: %v", err)
	}

	repo, err := Discover(deep)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	// t.TempDir may hand back a symlinked path (notably /var on macOS), so
	// compare resolved paths rather than raw strings.
	gotWork, _ := filepath.EvalSymlinks(repo.WorkTree)
	wantWork, _ := filepath.EvalSymlinks(root)
	if gotWork != wantWork {
		t.Errorf("WorkTree = %s, want %s", gotWork, wantWork)
	}
}

// TestDiscoverStopsAtFilesystemRoot proves the upward walk terminates rather
// than looping forever when there is no repository anywhere above.
func TestDiscoverStopsAtFilesystemRoot(t *testing.T) {
	orphan := filepath.Join(t.TempDir(), "no", "repo", "here")
	if err := os.MkdirAll(orphan, 0o755); err != nil {
		t.Fatalf("creating dirs: %v", err)
	}

	if _, err := Discover(orphan); !errors.Is(err, ErrNotARepository) {
		t.Fatalf("Discover error = %v, want ErrNotARepository", err)
	}
}

func TestInitInSubdirectoryArgument(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "newproject")

	repo, _, err := Init(target)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if _, err := os.Stat(repo.Path("objects")); err != nil {
		t.Errorf("Init did not create the nested repository: %v", err)
	}
}
