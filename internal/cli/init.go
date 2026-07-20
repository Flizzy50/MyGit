package cli

import (
	"fmt"

	"mygit/internal/repository"
)

var initCmd = &Command{
	Name:    "init",
	Summary: "create an empty mygit repository",
	Usage:   "mygit init [<directory>]",
	Run:     runInit,
}

func runInit(env *Env, args []string) error {
	fs := newFlagSet("init")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 1 {
		return fmt.Errorf("%w: expected at most one directory", errUsage)
	}

	dir := env.Dir
	if fs.NArg() == 1 {
		dir = fs.Arg(0)
	}

	repo, existed, err := repository.Init(dir)
	if err != nil {
		return err
	}

	verb := "Initialized empty"
	if existed {
		verb = "Reinitialized existing"
	}
	fmt.Fprintf(env.Stdout, "%s mygit repository in %s\n", verb, repo.GitDir)
	return nil
}
