package cli

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"mygit/internal/index"
	"mygit/internal/object"
	"mygit/internal/repository"
)

var addCmd = &Command{
	Name:    "add",
	Summary: "stage file contents into the index",
	Usage:   "mygit add <pathspec>...",
	Run:     runAdd,
}

// runAdd stages paths: it hashes each file into a blob, stores that blob, and
// records an index entry pointing at it. This is the seam where Phase 2's
// object store and Phase 4's index meet — `add` is essentially `hash-object -w`
// followed by writing an index entry that remembers the result.
//
// The whole index is loaded, mutated in memory, and written back once at the
// end. That mirrors Git: the index is rewritten wholesale on every operation.
// Batching the save also makes `add` atomic in effect — either every argument
// is staged or, on error, the on-disk index is left untouched.
func runAdd(env *Env, args []string) error {
	fs := newFlagSet("add")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return fmt.Errorf("%w: nothing specified, nothing added", errUsage)
	}

	repo, err := repository.Discover(env.Dir)
	if err != nil {
		return err
	}

	idx, err := index.Load(repo.Path("index"))
	if err != nil {
		return err
	}

	for _, arg := range fs.Args() {
		if err := stagePath(repo, idx, env.Dir, arg); err != nil {
			return err
		}
	}

	return idx.Save(repo.Path("index"))
}

// stagePath stages one argument, which may be a file or a directory.
func stagePath(repo *repository.Repository, idx *index.Index, cwd, arg string) error {
	abs := arg
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(cwd, arg)
	}
	abs = filepath.Clean(abs)

	info, err := os.Lstat(abs)
	if err != nil {
		return fmt.Errorf("pathspec %q does not match any files", arg)
	}

	if info.IsDir() {
		return stageDir(repo, idx, abs)
	}
	return stageFile(repo, idx, abs, info)
}

// stageDir recursively stages every file under dir, which is how `add src/`
// works. It walks the tree, skips the repository's own metadata directory, and
// stages each regular file it finds.
//
// Empty directories vanish: with no files under them there is nothing to stage,
// and Git cannot represent an empty directory at all — there is no blob, so no
// tree entry, so no trace. That is the reason for the widespread .gitkeep
// convention, and mygit inherits the same limitation for the same reason.
func stageDir(repo *repository.Repository, idx *index.Index, dir string) error {
	return filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == repository.DirName {
				return filepath.SkipDir // never stage .mygit into itself
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		return stageFile(repo, idx, path, info)
	})
}

// stageFile hashes one file into a blob and records its index entry.
func stageFile(repo *repository.Repository, idx *index.Index, abs string, info os.FileInfo) error {
	rel, err := workTreeRelative(repo.WorkTree, abs)
	if err != nil {
		return err
	}

	// Read, then store: the blob's content is the file's exact bytes, so the
	// entry's OID is identical to `hash-object`'s output. Content-addressing
	// means staging an unchanged file writes no new object.
	content, err := os.ReadFile(abs)
	if err != nil {
		return fmt.Errorf("reading %s: %w", rel, err)
	}
	oid, err := repo.Objects.Put(object.TypeBlob, content)
	if err != nil {
		return err
	}

	mt := info.ModTime()
	idx.Add(&index.Entry{
		Path:      rel,
		Mode:      object.ModeFromFS(info.Mode()),
		OID:       oid,
		MtimeSec:  uint32(mt.Unix()),
		MtimeNsec: uint32(mt.Nanosecond()),
		Size:      uint32(info.Size()),
	})
	return nil
}

// workTreeRelative converts an absolute path into the slash-separated,
// work-tree-relative form the index and tree objects use.
//
// Forward slashes are mandatory regardless of OS: tree objects always use them,
// so a repository created on Windows and read on Linux must agree on path
// bytes. Storing native backslashes would make the same tree hash differently
// per platform. The containment check keeps `add ../outside` from smuggling a
// path that escapes the repository.
func workTreeRelative(workTree, abs string) (string, error) {
	rel, err := filepath.Rel(workTree, abs)
	if err != nil {
		return "", fmt.Errorf("locating %s within the work tree: %w", abs, err)
	}
	rel = filepath.ToSlash(rel)
	if rel == ".." || strings.HasPrefix(rel, "../") {
		return "", fmt.Errorf("%s is outside the repository", abs)
	}
	return rel, nil
}
