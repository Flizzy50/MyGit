package cli

import (
	"errors"
	"fmt"

	"mygit/internal/index"
	"mygit/internal/object"
	"mygit/internal/refs"
	"mygit/internal/repository"
	"mygit/internal/worktree"
)

var checkoutCmd = &Command{
	Name:    "checkout",
	Summary: "restore the working tree to a branch or commit",
	Usage:   "mygit checkout <branch|commit>",
	Run:     runCheckout,
}

// runCheckout switches the working tree, the index, and HEAD to a target.
//
// All three move together, and that is the part worth stating plainly. Checkout
// is not "restore some files" — it repositions every layer of the system at
// once:
//
//	HEAD    ─▶ now names the target branch, or the commit itself
//	index   ─▶ rewritten to describe the target's tree exactly
//	worktree─▶ files created, overwritten, and deleted to match
//
// Leaving any one of the three behind produces a repository that lies about
// itself. An index still describing the old commit would make every file look
// modified; an unmoved HEAD would make the next commit record the wrong parent.
func runCheckout(env *Env, args []string) error {
	fs := newFlagSet("checkout")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("%w: checkout takes exactly one branch or commit", errUsage)
	}
	target := fs.Arg(0)

	repo, err := repository.Discover(env.Dir)
	if err != nil {
		return err
	}

	// Branch names win over raw object IDs, matching Git. The distinction
	// decides whether HEAD ends up attached or detached, which is the only
	// difference between the two forms of checkout.
	branchRef := refs.BranchRef(target)
	isBranch := repo.Refs.Exists(branchRef)

	commitOID, err := resolveRevision(repo, target)
	if err != nil {
		return err
	}

	treeOID, err := commitTree(repo, commitOID)
	if err != nil {
		return err
	}

	idx, err := index.Load(repo.Path("index"))
	if err != nil {
		return err
	}

	targetTree, err := worktree.Flatten(repo.Objects, treeOID)
	if err != nil {
		return err
	}

	// Plan, validate, then act. Nothing on disk changes until the plan is known
	// to be safe, so a rejected checkout leaves the working tree untouched.
	plan := worktree.BuildPlan(repo.WorkTree, worktree.FromIndex(idx), targetTree)
	if conflicts := worktree.Validate(repo.WorkTree, plan, idx); len(conflicts) > 0 {
		return errors.New(worktree.FormatConflicts(conflicts))
	}

	if err := worktree.Apply(repo.Objects, repo.WorkTree, plan, targetTree, idx); err != nil {
		return err
	}
	if err := idx.Save(repo.Path("index")); err != nil {
		return err
	}

	// HEAD moves last. Until this line the repository still describes the old
	// position, so an interrupted checkout is a recoverable inconsistency
	// rather than a lost one: rerunning the command finishes the job.
	if isBranch {
		if err := repo.Refs.SetHeadSymbolic(branchRef); err != nil {
			return err
		}
		fmt.Fprintf(env.Stdout, "Switched to branch '%s'\n", target)
		return nil
	}

	if err := repo.Refs.SetHeadDetached(commitOID); err != nil {
		return err
	}
	printDetachedNotice(env, commitOID, repo)
	return nil
}

// commitTree resolves a commit to its root tree.
func commitTree(repo *repository.Repository, commitOID object.OID) (object.OID, error) {
	typ, payload, err := repo.Objects.Get(commitOID)
	if err != nil {
		return object.OID{}, err
	}
	if typ != object.TypeCommit {
		return object.OID{}, fmt.Errorf("%s is a %s, not a commit", commitOID, typ)
	}
	commit, err := object.ParseCommit(payload)
	if err != nil {
		return object.OID{}, err
	}
	return commit.Tree, nil
}

// printDetachedNotice explains the detached state, because it is the one
// situation where committing can silently produce unreachable work.
//
// With HEAD holding a raw object ID there is no branch to advance, so any
// commit made here is referenced by nothing the moment HEAD moves elsewhere.
// Real Git prints a longer version of this warning for the same reason, and
// keeps a reflog so the commits remain findable; mygit has no reflog, so the
// warning is the only safety net there is.
func printDetachedNotice(env *Env, commitOID object.OID, repo *repository.Repository) {
	summary := ""
	if typ, payload, err := repo.Objects.Get(commitOID); err == nil && typ == object.TypeCommit {
		if c, err := object.ParseCommit(payload); err == nil {
			summary = " " + c.Summary()
		}
	}
	fmt.Fprintf(env.Stdout, "HEAD is now at %s%s\n", commitOID.String()[:7], summary)
	fmt.Fprintf(env.Stderr,
		"warning: HEAD is detached. New commits will not belong to any branch\n"+
			"and will be unreachable once you switch away.\n")
}
