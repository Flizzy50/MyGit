// Package repository owns the .mygit directory: its layout, its creation, and
// finding it from anywhere inside a working tree.
//
// The central separation is between the working tree — ordinary files you edit,
// exactly one version of each, mutable — and the repository, an append-only
// database holding every version ever recorded. The working tree is a mutable
// projection of one point in an immutable history. Keeping them apart is what
// lets Git rewind, branch, and compare without those operations meaning
// anything special to the tools that read your files.
//
// Storing metadata in a directory inside the working tree, rather than in a
// central server or a per-file sidecar, is what makes a Git clone a complete
// repository: copy the directory and you have the entire history, with no
// server to consult.
package repository

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"mygit/internal/refs"
	"mygit/internal/store"
)

// DirName is the metadata directory mygit creates inside a working tree.
const DirName = ".mygit"

// DefaultBranch is the branch HEAD points at in a fresh repository. Note that
// it does not exist yet at init time — see Init.
const DefaultBranch = "main"

// ErrNotARepository reports that no .mygit directory was found in the starting
// directory or any of its ancestors.
var ErrNotARepository = errors.New("not a mygit repository")

// Repository is an opened repository: a working tree, its metadata directory,
// and the two databases inside it.
//
// The pairing of Objects and Refs is the whole architecture in miniature.
// Objects is an immutable, content-addressed store that only ever grows; Refs
// is a tiny mutable layer of names pointing into it. Every operation from here
// on is some combination of "add objects" and "move a pointer".
type Repository struct {
	WorkTree string // absolute path to the working tree root
	GitDir   string // absolute path to the .mygit directory
	Objects  *store.Store
	Refs     *refs.Store
}

// Path joins path elements onto the repository's metadata directory.
func (r *Repository) Path(elem ...string) string {
	return filepath.Join(append([]string{r.GitDir}, elem...)...)
}

// Init creates or reinitializes a repository at path, reporting whether an
// existing repository was found.
//
// The layout created is:
//
//	.mygit/
//	├── objects/          content-addressable object database
//	├── refs/
//	│   ├── heads/        branches: one file per branch, holding a commit ID
//	│   └── tags/         tags: same shape, but not expected to move
//	└── HEAD              symbolic ref naming the current branch
//
// HEAD is written but refs/heads/main is not. That asymmetry is deliberate and
// is worth sitting with: HEAD holds "ref: refs/heads/main", a pointer to a
// branch that does not exist yet. Git calls this an unborn branch. It resolves
// to nothing until the first commit, at which point the commit machinery
// creates refs/heads/main and HEAD starts resolving. This is why a fresh
// repository reports being "on branch main" while `log` fails — and it is why
// the very first commit is the only one that has to create a ref rather than
// advance one.
func Init(path string) (*Repository, bool, error) {
	root, err := filepath.Abs(path)
	if err != nil {
		return nil, false, fmt.Errorf("resolving %s: %w", path, err)
	}
	gitDir := filepath.Join(root, DirName)

	existed := false
	if info, err := os.Stat(gitDir); err == nil {
		if !info.IsDir() {
			return nil, false, fmt.Errorf("%s exists and is not a directory", gitDir)
		}
		existed = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, false, fmt.Errorf("inspecting %s: %w", gitDir, err)
	}

	// MkdirAll is idempotent, so reinitializing an existing repository repairs
	// a missing subdirectory instead of failing. Real `git init` behaves the
	// same way.
	for _, dir := range []string{
		gitDir,
		filepath.Join(gitDir, "objects"),
		filepath.Join(gitDir, "refs"),
		filepath.Join(gitDir, "refs", "heads"),
		filepath.Join(gitDir, "refs", "tags"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, false, fmt.Errorf("creating %s: %w", dir, err)
		}
	}

	// Never overwrite an existing HEAD: it records which branch is checked out,
	// and clobbering it would silently move the user off their branch.
	headPath := filepath.Join(gitDir, "HEAD")
	if _, err := os.Stat(headPath); errors.Is(err, os.ErrNotExist) {
		head := fmt.Sprintf("ref: refs/heads/%s\n", DefaultBranch)
		if err := os.WriteFile(headPath, []byte(head), 0o644); err != nil {
			return nil, false, fmt.Errorf("creating HEAD: %w", err)
		}
	} else if err != nil {
		return nil, false, fmt.Errorf("inspecting HEAD: %w", err)
	}

	return open(root, gitDir), existed, nil
}

// Discover walks up from start looking for a .mygit directory and opens the
// first repository it finds.
//
// Searching ancestors is why Git commands work from any subdirectory of a
// project. The walk terminates at the filesystem root, detected by
// filepath.Dir returning its own argument — the portable way to spot the root
// on both Unix ("/") and Windows volume roots ("C:\").
func Discover(start string) (*Repository, error) {
	dir, err := filepath.Abs(start)
	if err != nil {
		return nil, fmt.Errorf("resolving %s: %w", start, err)
	}

	for {
		candidate := filepath.Join(dir, DirName)
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return open(dir, candidate), nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil, fmt.Errorf("%w (or any parent of %s)", ErrNotARepository, start)
		}
		dir = parent
	}
}

func open(workTree, gitDir string) *Repository {
	return &Repository{
		WorkTree: workTree,
		GitDir:   gitDir,
		Objects:  store.New(filepath.Join(gitDir, "objects")),
		Refs:     refs.New(gitDir),
	}
}
