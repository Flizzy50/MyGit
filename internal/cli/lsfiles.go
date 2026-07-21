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
			// The trailing "0" is the merge stage. mygit only ever writes
			// stage 0, the ordinary unconflicted state. Stages 1, 2, and 3 —
			// base, ours, theirs — appear only during an unresolved merge, and
			// arrive in Phase 11. Emitting the column now keeps the output
			// shape stable once conflicts exist.
			fmt.Fprintf(env.Stdout, "%s %s 0\t%s\n", e.Mode, e.OID, e.Path)
		} else {
			fmt.Fprintln(env.Stdout, e.Path)
		}
	}
	return nil
}
