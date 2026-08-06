package cli

import (
	"fmt"

	"mygit/internal/index"
	"mygit/internal/repository"
)

var lsFilesCmd = &Command{
	Name:    "ls-files",
	Summary: "list staged files, optionally with their metadata",
	Usage:   "mygit ls-files [-s]",
	Run:     runLsFiles,
}

// runLsFiles prints the index, the way `cat-file` prints an object: it is the
// window that makes the staging area inspectable and testable. With -s it
// mirrors `git ls-files --stage`, emitting the mode, blob ID, merge stage, and
// path of every entry.
func runLsFiles(env *Env, args []string) error {
	fs := newFlagSet("ls-files")
	stage := fs.Bool("s", false, "show staged contents' mode, object id, and stage")
	if err := fs.Parse(args); err != nil {
		return err
	}

	repo, err := repository.Discover(env.Dir)
	if err != nil {
		return err
	}

	idx, err := index.Load(repo.Path("index"))
	if err != nil {
		return err
	}

	for _, e := range idx.Entries() {
		if *stage {
			// The trailing number is the merge stage: 0 for an ordinary entry,
			// and 1, 2, or 3 for the base, ours, and theirs versions of a path
			// with an unresolved conflict. A conflicted path therefore appears
			// three times here, which is exactly how Git reports it.
			fmt.Fprintf(env.Stdout, "%s %s %d\t%s\n", e.Mode, e.OID, e.Stage, e.Path)
		} else {
			fmt.Fprintln(env.Stdout, e.Path)
		}
	}
	return nil
}
