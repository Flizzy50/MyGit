package cli

import (
	"errors"
	"fmt"
	"strings"

	"mygit/internal/index"
	"mygit/internal/object"
	"mygit/internal/refs"
	"mygit/internal/repository"
)

var commitCmd = &Command{
	Name:    "commit",
	Summary: "record the staged snapshot as a new commit",
	Usage:   "mygit commit -m <message>",
	Run:     runCommit,
}

// runCommit records the index as a permanent snapshot and advances the current
// branch.
//
// The whole operation is five steps, and it is worth seeing how little of it is
// new work by this point:
//
//  1. write-tree     turn the index into tree objects        (Phase 5)
//  2. read HEAD      find the parent, if any                 (refs)
//  3. build commit   tree + parents + identity + message     (this phase)
//  4. store commit   content-addressed, like everything else (Phase 2)
//  5. move the ref   advance the branch HEAD names           (refs)
//
// Only steps 3 and 5 are genuinely new. Committing is cheap precisely because
// the expensive part — hashing content — already happened at `add` time, and
// the index has been carrying the answer ever since.
func runCommit(env *Env, args []string) error {
	fs := newFlagSet("commit")
	message := fs.String("m", "", "commit message")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("%w: commit takes no positional arguments", errUsage)
	}
	if strings.TrimSpace(*message) == "" {
		return fmt.Errorf("%w: a commit message is required (-m)", errUsage)
	}

	repo, err := repository.Discover(env.Dir)
	if err != nil {
		return err
	}

	// Identity is resolved before any object is written, so a misconfigured
	// environment fails without leaving half a commit's worth of trees behind.
	author, err := signature("AUTHOR")
	if err != nil {
		return err
	}
	committer, err := signature("COMMITTER")
	if err != nil {
		// Committer defaults to author, which is the common case: the person
		// writing the change and the person recording it are the same. They
		// differ for patches applied by a maintainer, or after a rebase, which
		// is exactly why Git stores both.
		committer = author
	}

	idx, err := index.Load(repo.Path("index"))
	if err != nil {
		return err
	}

	tree, err := index.BuildTree(idx, repo.Objects)
	if err != nil {
		return err
	}

	head, err := repo.Refs.Head()
	if err != nil {
		return err
	}

	var parents []object.OID
	if !head.Unborn() {
		parents = append(parents, head.OID)
	}

	if err := checkSomethingToCommit(repo, head, tree); err != nil {
		return err
	}

	commit := &object.Commit{
		Tree:      tree,
		Parents:   parents,
		Author:    author,
		Committer: committer,
		Message:   *message,
	}

	oid, err := repo.Objects.Put(object.TypeCommit, commit.Serialize())
	if err != nil {
		return err
	}

	// The ref update is the last step and the only one that makes the commit
	// visible. Until this line runs, every object is already on disk but
	// unreachable — which is why a crash mid-commit loses nothing and corrupts
	// nothing. It simply leaves garbage that a future gc would collect.
	if err := repo.Refs.UpdateHead(oid); err != nil {
		return err
	}

	printCommitSummary(env, head, commit, oid)
	return nil
}

// checkSomethingToCommit refuses a commit that would record no change.
//
// The test is a single hash comparison: if the new root tree equals the parent
// commit's root tree, the snapshots are byte-identical and the commit would add
// nothing but a timestamp. This is the Merkle property paying off — proving two
// entire directory hierarchies are identical costs one 20-byte comparison, with
// no file reads at all.
func checkSomethingToCommit(repo *repository.Repository, head refs.Head, tree object.OID) error {
	if head.Unborn() {
		const emptyTree = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"
		if tree.String() == emptyTree {
			return errors.New("nothing to commit: no files staged")
		}
		return nil
	}

	typ, payload, err := repo.Objects.Get(head.OID)
	if err != nil {
		return fmt.Errorf("reading HEAD commit: %w", err)
	}
	if typ != object.TypeCommit {
		return fmt.Errorf("HEAD does not point at a commit (found %s)", typ)
	}
	parent, err := object.ParseCommit(payload)
	if err != nil {
		return err
	}
	if parent.Tree == tree {
		return errors.New("nothing to commit: the index matches HEAD")
	}
	return nil
}

// printCommitSummary reports the result in Git's familiar shape.
func printCommitSummary(env *Env, head refs.Head, commit *object.Commit, oid object.OID) {
	branch := head.ShortRef()
	if head.Detached() {
		branch = "detached HEAD"
	}

	rootNote := ""
	if commit.IsRoot() {
		rootNote = " (root-commit)"
	}

	fmt.Fprintf(env.Stdout, "[%s%s %s] %s\n",
		branch, rootNote, oid.String()[:7], commit.Summary())
}
