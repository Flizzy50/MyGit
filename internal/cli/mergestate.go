package cli

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"mygit/internal/fsutil"
	"mygit/internal/object"
	"mygit/internal/repository"
)

// A conflicted merge cannot finish in one command: it stops, hands the working
// tree to a person, and resumes later when they commit. Everything needed to
// resume therefore has to outlive the process, and these two files are where it
// lives.
//
//	MERGE_HEAD  the commit being merged in, which becomes the second parent
//	MERGE_MSG   the prepared commit message
//
// MERGE_HEAD is the important one. Losing it would not produce an error — it
// would produce an ordinary one-parent commit, silently dropping the incoming
// branch from history while the working tree still contained its changes. The
// files would be right and the graph would be a lie, which is the worst
// possible failure for a version control system.
//
// Their presence is also what makes "a merge is in progress" a persistent,
// inspectable fact rather than something inferred.
const (
	mergeHeadFile = "MERGE_HEAD"
	mergeMsgFile  = "MERGE_MSG"
)

// mergeMessage builds the conventional merge commit subject.
func mergeMessage(theirName, ourLabel string) string {
	return fmt.Sprintf("Merge branch '%s' into %s", theirName, ourLabel)
}

// writeMergeState records a suspended merge.
func writeMergeState(repo *repository.Repository, theirs object.OID, message string) error {
	if err := fsutil.AtomicWriteFile(repo.Path(mergeHeadFile), []byte(theirs.String()+"\n"), 0o644); err != nil {
		return err
	}
	return fsutil.AtomicWriteFile(repo.Path(mergeMsgFile), []byte(message+"\n"), 0o644)
}

// readMergeState reports whether a merge is suspended, and if so returns the
// commit that must become the second parent.
func readMergeState(repo *repository.Repository) (object.OID, string, bool, error) {
	raw, err := os.ReadFile(repo.Path(mergeHeadFile))
	if errors.Is(err, os.ErrNotExist) {
		return object.OID{}, "", false, nil
	}
	if err != nil {
		return object.OID{}, "", false, fmt.Errorf("reading %s: %w", mergeHeadFile, err)
	}

	oid, err := object.ParseOID(strings.TrimSpace(string(raw)))
	if err != nil {
		return object.OID{}, "", false, fmt.Errorf("%s is corrupt: %w", mergeHeadFile, err)
	}

	message := ""
	if msg, err := os.ReadFile(repo.Path(mergeMsgFile)); err == nil {
		message = strings.TrimSpace(string(msg))
	}
	return oid, message, true, nil
}

// clearMergeState ends the merge.
//
// This runs only after the merge commit has been written and the branch moved.
// Clearing earlier would leave a window in which a crash lost the second parent
// while the resolution was still unrecorded, turning a recoverable interruption
// into a silently wrong history.
func clearMergeState(repo *repository.Repository) error {
	for _, name := range []string{mergeHeadFile, mergeMsgFile} {
		if err := os.Remove(repo.Path(name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("clearing %s: %w", name, err)
		}
	}
	return nil
}
