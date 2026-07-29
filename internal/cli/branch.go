package cli

import (
	"fmt"

	"mygit/internal/graph"
	"mygit/internal/object"
	"mygit/internal/refs"
	"mygit/internal/repository"
)

var branchCmd = &Command{
	Name:    "branch",
	Summary: "list, create, or delete branches",
	Usage:   "mygit branch [-d|-D] [<name> [<start-point>]]",
	Run:     runBranch,
}

// runBranch manages branches, which are the cheapest objects in the system.
//
// It is worth pausing on how little this command does. Creating a branch writes
// one file containing forty hex characters and a newline. Deleting one removes
// that file. No history is copied, no files are touched, no working tree is
// duplicated — because a branch is not a container of commits, it is a single
// pointer into a graph that already exists.
//
// This is the concrete reason branching is instant in Git and expensive in
// systems that model a branch as a directory copy on a server. The cost of an
// operation follows directly from how the data is represented, and Git chose a
// representation where the operation is trivially cheap.
func runBranch(env *Env, args []string) error {
	fs := newFlagSet("branch")
	del := fs.Bool("d", false, "delete a fully merged branch")
	forceDel := fs.Bool("D", false, "delete a branch regardless of merge state")
	if err := fs.Parse(args); err != nil {
		return err
	}

	repo, err := repository.Discover(env.Dir)
	if err != nil {
		return err
	}

	if *del || *forceDel {
		if fs.NArg() != 1 {
			return fmt.Errorf("%w: give exactly one branch to delete", errUsage)
		}
		return deleteBranch(env, repo, fs.Arg(0), *forceDel)
	}

	switch fs.NArg() {
	case 0:
		return listBranches(env, repo)
	case 1, 2:
		start := "HEAD"
		if fs.NArg() == 2 {
			start = fs.Arg(1)
		}
		return createBranch(env, repo, fs.Arg(0), start)
	default:
		return fmt.Errorf("%w: too many arguments", errUsage)
	}
}

// listBranches prints every branch, marking the current one.
func listBranches(env *Env, repo *repository.Repository) error {
	branches, err := repo.Refs.List(refs.HeadsPrefix)
	if err != nil {
		return err
	}

	head, err := repo.Refs.Head()
	if err != nil {
		return err
	}

	for _, branch := range branches {
		marker := "  "
		if !head.Detached() && head.Ref == branch.Name {
			marker = "* "
		}
		fmt.Fprintf(env.Stdout, "%s%s\n", marker, branch.Short())
	}

	// A detached HEAD is shown as a pseudo-branch, because otherwise the
	// listing would silently omit where the user actually is.
	if head.Detached() {
		fmt.Fprintf(env.Stdout, "* (HEAD detached at %s)\n", head.OID.String()[:7])
	}
	return nil
}

// createBranch points a new name at an existing commit.
//
// The new branch and the old one now name the same commit, and neither is more
// real than the other. There is no parent-child relationship between branches,
// no record that one was "created from" the other, and nothing distinguishing
// main from a branch made a second ago. Git stores only where each name points
// right now; the sense that a branch "came from" another lives entirely in the
// commit graph they happen to share.
func createBranch(env *Env, repo *repository.Repository, name, start string) error {
	ref := refs.BranchRef(name)

	if repo.Refs.Exists(ref) {
		return fmt.Errorf("branch %q already exists", name)
	}

	oid, err := resolveRevision(repo, start)
	if err != nil {
		return err
	}
	// Branches must point at commits. Allowing a branch to name a blob would
	// break every traversal that assumes otherwise.
	if typ, _, err := repo.Objects.Get(oid); err != nil {
		return err
	} else if typ != object.TypeCommit {
		return fmt.Errorf("%s is a %s, not a commit", start, typ)
	}

	if err := repo.Refs.Update(ref, oid); err != nil {
		return err
	}
	fmt.Fprintf(env.Stdout, "Created branch %s at %s\n", name, oid.String()[:7])
	return nil
}

// deleteBranch removes a branch, refusing by default to strand commits.
//
// The safety check is a reachability question: if the branch's tip is an
// ancestor of HEAD, every commit on it is already reachable through HEAD, so
// removing the name loses nothing. If it is not, the branch holds commits that
// exist on no other path, and deleting the ref makes them unreachable — still
// on disk, but findable only by remembering a hash nobody wrote down.
//
// This is where the object model's one real hazard shows up. Objects are
// immortal and immutable, but *names* are not, and a name is the only practical
// way to find anything. Git protects you here precisely because the loss is
// silent: nothing is deleted, so nothing looks wrong.
func deleteBranch(env *Env, repo *repository.Repository, name string, force bool) error {
	ref := refs.BranchRef(name)

	head, err := repo.Refs.Head()
	if err != nil {
		return err
	}
	if !head.Detached() && head.Ref == ref {
		return fmt.Errorf("cannot delete branch %q: it is currently checked out", name)
	}

	tip, err := repo.Refs.Resolve(ref)
	if err != nil {
		return fmt.Errorf("branch %q not found", name)
	}

	if !force {
		merged, err := isMergedIntoHead(repo, head, tip)
		if err != nil {
			return err
		}
		if !merged {
			return fmt.Errorf(
				"branch %q is not fully merged; its commits would become unreachable\n"+
					"use -D to delete it anyway", name)
		}
	}

	if err := repo.Refs.Delete(ref); err != nil {
		return err
	}
	fmt.Fprintf(env.Stdout, "Deleted branch %s (was %s)\n", name, tip.String()[:7])
	return nil
}

// isMergedIntoHead reports whether a branch tip is already reachable from HEAD.
func isMergedIntoHead(repo *repository.Repository, head refs.Head, tip object.OID) (bool, error) {
	if head.Unborn() {
		// Nothing is reachable from an unborn HEAD, so no branch can be merged
		// into it.
		return false, nil
	}
	return graph.IsAncestor(repo.Objects, tip, head.OID)
}
