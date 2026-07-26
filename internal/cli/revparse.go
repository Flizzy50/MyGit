package cli

import (
	"fmt"

	"mygit/internal/object"
	"mygit/internal/refs"
	"mygit/internal/repository"
)

var revParseCmd = &Command{
	Name:    "rev-parse",
	Summary: "resolve a revision to an object id",
	Usage:   "mygit rev-parse [--abbrev-ref] <revision>",
	Run:     runRevParse,
}

// runRevParse turns a human-written revision into an object ID.
//
// This is the plumbing that makes the reference layer inspectable, the way
// cat-file exposes objects and ls-files exposes the index. It is also where
// "revision syntax" begins: users type names, and something has to translate
// them into the hashes the object database actually speaks.
func runRevParse(env *Env, args []string) error {
	fs := newFlagSet("rev-parse")
	abbrevRef := fs.Bool("abbrev-ref", false, "print the branch name HEAD points at")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("%w: rev-parse takes exactly one revision", errUsage)
	}
	rev := fs.Arg(0)

	repo, err := repository.Discover(env.Dir)
	if err != nil {
		return err
	}

	if *abbrevRef {
		if rev != "HEAD" {
			return fmt.Errorf("--abbrev-ref is only supported for HEAD")
		}
		head, err := repo.Refs.Head()
		if err != nil {
			return err
		}
		if head.Detached() {
			fmt.Fprintln(env.Stdout, "HEAD")
			return nil
		}
		fmt.Fprintln(env.Stdout, head.ShortRef())
		return nil
	}

	oid, err := resolveRevision(repo, rev)
	if err != nil {
		return err
	}
	fmt.Fprintln(env.Stdout, oid)
	return nil
}

// resolveRevision maps a revision string to an object ID.
//
// The lookup order matters and mirrors Git's: a full object ID is taken
// literally, then HEAD, then a branch name. Git's real resolution order is
// longer — tags, remote-tracking branches, abbreviations, and suffixes like
// HEAD~2 and HEAD^ — but the shape is the same, and mygit's version is the
// piece every later phase needs.
func resolveRevision(repo *repository.Repository, rev string) (object.OID, error) {
	if oid, err := object.ParseOID(rev); err == nil {
		if !repo.Objects.Has(oid) {
			return object.OID{}, fmt.Errorf("no such object: %s", rev)
		}
		return oid, nil
	}

	if rev == refs.HeadFile {
		head, err := repo.Refs.Head()
		if err != nil {
			return object.OID{}, err
		}
		if head.Unborn() {
			return object.OID{}, fmt.Errorf("HEAD does not point at any commit yet")
		}
		return head.OID, nil
	}

	if oid, err := repo.Refs.Resolve(refs.BranchRef(rev)); err == nil {
		return oid, nil
	}
	return object.OID{}, fmt.Errorf("unknown revision %q", rev)
}
