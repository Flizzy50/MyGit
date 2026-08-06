package cli

import (
	"errors"
	"fmt"
	"strings"

	"mygit/internal/graph"
	"mygit/internal/index"
	"mygit/internal/merge"
	"mygit/internal/object"
	"mygit/internal/refs"
	"mygit/internal/repository"
	"mygit/internal/worktree"
)

var mergeCmd = &Command{
	Name:    "merge",
	Summary: "join another branch's history into the current one",
	Usage:   "mygit merge <branch>",
	Run:     runMerge,
}

// runMerge combines another branch into the current one.
//
// Three outcomes are possible, and distinguishing them is entirely a question
// about the shape of the graph — no file is read to decide:
//
//	already up to date   theirs is an ancestor of ours; nothing to do
//	fast-forward         ours is an ancestor of theirs; just move the pointer
//	true merge           neither reaches the other; three-way merge required
//
//	  fast-forward                     true merge
//	  ────────────                     ──────────
//	  A ◀─ B ◀─ C  (theirs)            A ◀─ B ◀─ C   (ours)
//	       ▲                                ▲
//	       └ ours                           └─ D     (theirs)
//	  ours just moves to C             base is B; both sides changed
//
// The first two cases cost nothing at all, which is worth noticing: most merges
// in practice are fast-forwards, and Git answers them with a reachability query
// rather than any content comparison.
func runMerge(env *Env, args []string) error {
	fs := newFlagSet("merge")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("%w: merge takes exactly one branch", errUsage)
	}
	theirName := fs.Arg(0)

	repo, err := repository.Discover(env.Dir)
	if err != nil {
		return err
	}

	head, err := repo.Refs.Head()
	if err != nil {
		return err
	}
	if head.Unborn() {
		return errors.New("cannot merge: the current branch has no commits yet")
	}

	idx, err := index.Load(repo.Path("index"))
	if err != nil {
		return err
	}
	if idx.HasConflicts() {
		return fmt.Errorf("cannot merge: a previous merge is still unresolved\n"+
			"resolve and stage these paths first:\n\t%s",
			strings.Join(idx.ConflictedPaths(), "\n\t"))
	}
	// Conflicts can all be resolved without the merge being committed, so the
	// index alone is not enough to tell whether one is still open. MERGE_HEAD is
	// the authoritative signal, and starting a second merge over it would
	// discard the first merge's other parent.
	if _, _, merging, err := readMergeState(repo); err != nil {
		return err
	} else if merging {
		return errors.New("cannot merge: a merge is already in progress; commit it first")
	}

	theirs, err := resolveRevision(repo, theirName)
	if err != nil {
		return err
	}

	bases, err := graph.MergeBases(repo.Objects, head.OID, theirs)
	if err != nil {
		return err
	}
	if len(bases) == 0 {
		return fmt.Errorf("refusing to merge unrelated histories: %s shares no ancestor with HEAD", theirName)
	}

	// Case 1: their tip is already reachable from ours, so their work is
	// entirely contained in our history.
	if bases[0] == theirs && len(bases) == 1 {
		fmt.Fprintln(env.Stdout, "Already up to date.")
		return nil
	}

	// Case 2: our tip is an ancestor of theirs, so no merge commit is needed.
	// Moving the branch pointer forward produces exactly their history, and
	// recording a merge would add a node that says nothing.
	if len(bases) == 1 && bases[0] == head.OID {
		return fastForward(env, repo, idx, head, theirs, theirName)
	}

	// Case 3: a genuine three-way merge.
	if len(bases) > 1 {
		fmt.Fprintf(env.Stderr,
			"warning: %d merge bases found (criss-cross history); using %s\n"+
				"real Git would merge the bases recursively to synthesize a virtual base\n",
			len(bases), bases[0].String()[:7])
	}
	return threeWayMerge(env, repo, idx, head, bases[0], theirs, theirName)
}

// fastForward advances the branch without creating a merge commit.
func fastForward(env *Env, repo *repository.Repository, idx *index.Index, head refs.Head, theirs object.OID, theirName string) error {
	targetTree, err := treeOfCommit(repo, theirs)
	if err != nil {
		return err
	}

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
	if err := repo.Refs.UpdateHead(theirs); err != nil {
		return err
	}

	fmt.Fprintf(env.Stdout, "Fast-forward to %s (%s)\n", theirs.String()[:7], theirName)
	return nil
}

// threeWayMerge performs the real merge and either commits it or leaves the
// conflicts staged for a human.
func threeWayMerge(env *Env, repo *repository.Repository, idx *index.Index, head refs.Head, base, theirs object.OID, theirName string) error {
	baseTree, err := treeOfCommit(repo, base)
	if err != nil {
		return err
	}
	ourTree, err := treeOfCommit(repo, head.OID)
	if err != nil {
		return err
	}
	theirTree, err := treeOfCommit(repo, theirs)
	if err != nil {
		return err
	}

	ourLabel := head.ShortRef()
	if head.Detached() {
		ourLabel = "HEAD"
	}

	result, err := merge.Trees(repo.Objects, baseTree, ourTree, theirTree, ourLabel, theirName)
	if err != nil {
		return err
	}

	// The merged result is written to the working tree exactly as a checkout
	// would write it, conflict markers included — a conflicted file is an
	// ordinary file whose contents happen to show both versions, which is what
	// makes it editable in place.
	plan := worktree.BuildPlan(repo.WorkTree, worktree.FromIndex(idx), result.Tree)
	if conflicts := worktree.Validate(repo.WorkTree, plan, idx); len(conflicts) > 0 {
		return errors.New(worktree.FormatConflicts(conflicts))
	}
	if err := worktree.Apply(repo.Objects, repo.WorkTree, plan, result.Tree, idx); err != nil {
		return err
	}

	if !result.Clean() {
		recordConflicts(idx, result.Conflicts)
		if err := idx.Save(repo.Path("index")); err != nil {
			return err
		}
		// The merge is now suspended, and the other parent has to survive until
		// a human finishes it. Without recording it, the eventual commit would
		// have only one parent and the incoming branch's history would simply
		// vanish from the graph — the merge would appear never to have happened.
		if err := writeMergeState(repo, theirs, mergeMessage(theirName, ourLabel)); err != nil {
			return err
		}
		printConflictReport(env, result.Conflicts)
		return errors.New("automatic merge failed; fix conflicts, stage them, and commit")
	}

	if err := idx.Save(repo.Path("index")); err != nil {
		return err
	}
	return commitMerge(env, repo, idx, head, theirs, theirName)
}

// recordConflicts replaces the stage-zero entry of each conflicted path with
// the three competing versions.
//
// Removing stage zero is what makes the conflict visible to everything else in
// the system: commit refuses while any nonzero stage exists, so an unresolved
// merge cannot be recorded by accident.
func recordConflicts(idx *index.Index, conflicts []merge.Conflict) {
	for _, c := range conflicts {
		idx.Remove(c.Path)
		for _, side := range []struct {
			entry *worktree.Entry
			stage index.Stage
		}{
			{c.Base, index.StageBase},
			{c.Ours, index.StageOurs},
			{c.Theirs, index.StageTheirs},
		} {
			if side.entry == nil {
				continue // absent on that side, which is itself information
			}
			idx.Add(&index.Entry{
				Path:  c.Path,
				Stage: side.stage,
				Mode:  side.entry.Mode,
				OID:   side.entry.OID,
			})
		}
	}
}

func printConflictReport(env *Env, conflicts []merge.Conflict) {
	for _, c := range conflicts {
		fmt.Fprintf(env.Stderr, "CONFLICT (%s): %s\n", c.Reason, c.Path)
	}
}

// commitMerge records the merge commit: the one place a commit gets two
// parents, and therefore the only thing that makes history a DAG rather than a
// list.
//
// Parent order is significant. The first parent is the branch that was merged
// *into*, which is what gives "the mainline" its meaning in later history
// traversal and what `HEAD^1` selects.
func commitMerge(env *Env, repo *repository.Repository, idx *index.Index, head refs.Head, theirs object.OID, theirName string) error {
	author, err := signature("AUTHOR")
	if err != nil {
		return err
	}
	committer, err := signature("COMMITTER")
	if err != nil {
		committer = author
	}

	tree, err := index.BuildTree(idx, repo.Objects)
	if err != nil {
		return err
	}

	ourLabel := head.ShortRef()
	if head.Detached() {
		ourLabel = "HEAD"
	}

	commit := &object.Commit{
		Tree:      tree,
		Parents:   []object.OID{head.OID, theirs},
		Author:    author,
		Committer: committer,
		Message:   fmt.Sprintf("Merge branch '%s' into %s", theirName, ourLabel),
	}

	oid, err := repo.Objects.Put(object.TypeCommit, commit.Serialize())
	if err != nil {
		return err
	}
	if err := repo.Refs.UpdateHead(oid); err != nil {
		return err
	}

	fmt.Fprintf(env.Stdout, "Merge made by the three-way strategy.\n[%s %s] %s\n",
		ourLabel, oid.String()[:7], commit.Summary())
	return nil
}

// treeOfCommit loads and flattens a commit's root tree.
func treeOfCommit(repo *repository.Repository, commitOID object.OID) (worktree.Tree, error) {
	treeOID, err := commitTree(repo, commitOID)
	if err != nil {
		return nil, err
	}
	return worktree.Flatten(repo.Objects, treeOID)
}
