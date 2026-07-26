package cli

import (
	"fmt"

	"mygit/internal/index"
	"mygit/internal/repository"
)

var writeTreeCmd = &Command{
	Name:    "write-tree",
	Summary: "write the current index out as a tree object",
	Usage:   "mygit write-tree",
	Run:     runWriteTree,
}

// runWriteTree turns the staging area into a persistent tree and prints the
// root ID.
//
// This is the bridge between the index and real history. Commit (Phase 6) is
// almost entirely this command plus a small object recording the root tree, a
// parent, and a message — which is why `write-tree` exists as plumbing in real
// Git too, and why understanding it makes commit almost anticlimactic.
func runWriteTree(env *Env, args []string) error {
	fs := newFlagSet("write-tree")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("%w: write-tree takes no arguments", errUsage)
	}

	repo, err := repository.Discover(env.Dir)
	if err != nil {
		return err
	}

	idx, err := index.Load(repo.Path("index"))
	if err != nil {
		return err
	}

	oid, err := index.BuildTree(idx, repo.Objects)
	if err != nil {
		return err
	}

	fmt.Fprintln(env.Stdout, oid)
	return nil
}
