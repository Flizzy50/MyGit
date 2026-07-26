// Package refs implements the reference layer: the mutable names that point
// into the immutable object graph.
//
// Everything below this layer is content-addressed and frozen. Objects can only
// be added, never changed, because their names are their hashes. But a version
// control system needs a moving "you are here", and that is what refs provide:
// a small set of mutable pointers layered over permanent data.
//
//	mutable                    immutable
//	───────                    ─────────
//	HEAD ──▶ refs/heads/main ──▶ commit C ──▶ commit B ──▶ commit A
//	         refs/heads/feat ──▶ commit E
//
// A branch is therefore not a copy, a container, or a chain of commits. It is a
// single file holding one 40-character object ID. Creating a branch writes 41
// bytes; deleting one removes 41 bytes. That is the entire reason branching in
// Git is instant, and why the operation that costs minutes in older systems
// costs nothing here.
//
// HEAD adds a second level of indirection by usually holding "ref: <name>"
// rather than an ID. Committing then never rewrites HEAD — it resolves HEAD one
// hop to find the current branch and moves that branch forward, so HEAD keeps
// naming the branch and follows along for free.
package refs

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"mygit/internal/fsutil"
	"mygit/internal/object"
)

// HeadFile is the name of the file holding the current position.
const HeadFile = "HEAD"

// HeadsPrefix is where branches live. The directory layout under refs/ is the
// namespace: branches in refs/heads, tags in refs/tags, remote-tracking
// branches in refs/remotes. Nothing distinguishes them but the path.
const HeadsPrefix = "refs/heads/"

// symrefPrefix marks a symbolic reference: a ref whose content names another
// ref instead of an object.
const symrefPrefix = "ref: "

// maxSymrefDepth bounds symbolic-reference chasing. A ref pointing at itself,
// or a cycle of refs, would otherwise loop forever — and refs are ordinary
// files a user can edit, so this is a reachable state, not a theoretical one.
const maxSymrefDepth = 5

var (
	// ErrNotFound reports that a reference does not exist. For HEAD's branch
	// this is the normal state of a repository with no commits yet.
	ErrNotFound = errors.New("reference not found")

	// ErrInvalidName reports a syntactically illegal reference name.
	ErrInvalidName = errors.New("invalid reference name")
)

// Store reads and writes the references of one repository.
type Store struct {
	gitDir string
}

// New returns a Store for the given .mygit directory.
func New(gitDir string) *Store { return &Store{gitDir: gitDir} }

// Head describes what HEAD currently points at.
//
// The two flags encode states that must be handled separately everywhere, and
// forgetting either is a classic source of bugs:
//
//   - Detached: HEAD holds an object ID directly instead of naming a branch.
//     Commits made here advance nothing, so they become unreachable the moment
//     HEAD moves away. That is the whole meaning of "detached HEAD".
//   - Unborn: HEAD names a branch whose file does not exist yet, the state
//     `init` leaves behind. The branch is created by the first commit.
type Head struct {
	Ref string     // branch ref name, or "" when detached
	OID object.OID // resolved commit, zero when unborn
}

// Detached reports whether HEAD holds an object ID rather than a branch name.
func (h Head) Detached() bool { return h.Ref == "" }

// Unborn reports whether HEAD resolves to no commit yet.
func (h Head) Unborn() bool { return h.OID.IsZero() }

// ShortRef strips the refs/heads/ prefix for display.
func (h Head) ShortRef() string { return strings.TrimPrefix(h.Ref, HeadsPrefix) }

// Head reads HEAD and resolves it as far as it can.
//
// An unborn branch is reported as a valid Head with a zero OID rather than as
// an error, because "on a branch with no commits yet" is a legitimate state
// that callers must distinguish from "something is broken".
func (s *Store) Head() (Head, error) {
	raw, err := s.readRefFile(HeadFile)
	if err != nil {
		return Head{}, fmt.Errorf("reading HEAD: %w", err)
	}

	target, isSymbolic := parseSymref(raw)
	if !isSymbolic {
		oid, err := object.ParseOID(raw)
		if err != nil {
			return Head{}, fmt.Errorf("HEAD is corrupt: %w", err)
		}
		return Head{OID: oid}, nil // detached
	}

	if err := validateRefName(target); err != nil {
		return Head{}, fmt.Errorf("HEAD names an invalid ref: %w", err)
	}

	oid, err := s.Resolve(target)
	if errors.Is(err, ErrNotFound) {
		return Head{Ref: target}, nil // unborn: named, but not yet created
	}
	if err != nil {
		return Head{}, err
	}
	return Head{Ref: target, OID: oid}, nil
}

// Resolve follows a reference to an object ID, chasing symbolic refs.
func (s *Store) Resolve(name string) (object.OID, error) {
	if err := validateRefName(name); err != nil {
		return object.OID{}, err
	}

	for depth := 0; depth < maxSymrefDepth; depth++ {
		raw, err := s.readRefFile(name)
		if err != nil {
			return object.OID{}, err
		}
		target, isSymbolic := parseSymref(raw)
		if !isSymbolic {
			return object.ParseOID(raw)
		}
		if err := validateRefName(target); err != nil {
			return object.OID{}, err
		}
		name = target
	}
	return object.OID{}, fmt.Errorf("reference %q: too many symbolic levels, possible cycle", name)
}

// Update points a reference at an object, creating it if absent.
//
// Creation and movement are the same operation because a ref is just a file
// holding an ID. This is what lets the first commit create refs/heads/main
// without any special case: it writes the file that did not exist, and every
// later commit overwrites it.
func (s *Store) Update(name string, oid object.OID) error {
	if err := validateRefName(name); err != nil {
		return err
	}
	return s.writeRefFile(name, oid.String()+"\n")
}

// UpdateHead moves whatever HEAD currently designates.
//
// This is the single most important function in the package, and the payoff of
// HEAD's indirection. On a branch it advances the branch and leaves HEAD
// untouched, so HEAD keeps saying "ref: refs/heads/main" and points at the new
// commit automatically. Detached, there is no branch to move, so it rewrites
// HEAD itself. Commit calls this and never needs to know which case it is in.
func (s *Store) UpdateHead(oid object.OID) error {
	head, err := s.Head()
	if err != nil {
		return err
	}
	if head.Detached() {
		return s.writeRefFile(HeadFile, oid.String()+"\n")
	}
	return s.Update(head.Ref, oid)
}

// SetHeadSymbolic points HEAD at a branch without touching the working tree.
// This is the "attach" half of switching branches; Phase 8 supplies the other
// half, which is making the files on disk match.
func (s *Store) SetHeadSymbolic(target string) error {
	if err := validateRefName(target); err != nil {
		return err
	}
	return s.writeRefFile(HeadFile, symrefPrefix+target+"\n")
}

// SetHeadDetached points HEAD directly at a commit, detaching it.
func (s *Store) SetHeadDetached(oid object.OID) error {
	return s.writeRefFile(HeadFile, oid.String()+"\n")
}

// Exists reports whether a reference is present.
func (s *Store) Exists(name string) bool {
	_, err := s.readRefFile(name)
	return err == nil
}

// BranchRef expands a short branch name into its full reference name.
func BranchRef(name string) string { return HeadsPrefix + name }

// readRefFile returns a ref file's contents with surrounding whitespace
// removed. Trailing newlines are conventional in ref files, and tolerating
// stray whitespace keeps hand-edited refs working.
func (s *Store) readRefFile(name string) (string, error) {
	data, err := os.ReadFile(filepath.Join(s.gitDir, filepath.FromSlash(name)))
	if errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// writeRefFile stores a ref atomically.
//
// Atomicity matters more here than almost anywhere else in the system. A ref is
// the only thing making a commit reachable, so a torn write during commit could
// leave a branch pointing at nothing, orphaning history that is otherwise
// perfectly intact on disk. Objects are safe because they are immutable; refs
// are the mutable frontier, and the frontier is what needs protecting.
func (s *Store) writeRefFile(name, content string) error {
	full := filepath.Join(s.gitDir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return fmt.Errorf("creating ref directory: %w", err)
	}
	return fsutil.AtomicWriteFile(full, []byte(content), 0o644)
}

// parseSymref splits "ref: refs/heads/main" into its target.
func parseSymref(raw string) (target string, ok bool) {
	if rest, found := strings.CutPrefix(raw, symrefPrefix); found {
		return strings.TrimSpace(rest), true
	}
	return "", false
}

// validateRefName rejects names that are malformed or unsafe.
//
// Refs become filesystem paths, so an unvalidated name is a path traversal:
// "refs/heads/../../../../etc/passwd" would escape the repository entirely.
// The rules below also reject the syntax Git forbids for its own reasons —
// names ending in ".lock" collide with its locking scheme, and ".." and "@{"
// are reserved by the revision syntax.
func validateRefName(name string) error {
	switch {
	case name == "":
		return fmt.Errorf("%w: empty", ErrInvalidName)
	case name == HeadFile:
		return nil // HEAD is the one legal name outside refs/
	case !strings.HasPrefix(name, "refs/"):
		return fmt.Errorf("%w: %q must begin with refs/", ErrInvalidName, name)
	case strings.Contains(name, `\`):
		return fmt.Errorf("%w: %q contains a backslash", ErrInvalidName, name)
	case strings.Contains(name, ".."):
		return fmt.Errorf("%w: %q contains ..", ErrInvalidName, name)
	case strings.Contains(name, "@{"):
		return fmt.Errorf("%w: %q contains @{", ErrInvalidName, name)
	case strings.HasSuffix(name, ".lock"):
		return fmt.Errorf("%w: %q ends with .lock", ErrInvalidName, name)
	case strings.HasSuffix(name, "/"):
		return fmt.Errorf("%w: %q ends with /", ErrInvalidName, name)
	}

	// Defense in depth: even after the checks above, confirm the cleaned path
	// has not escaped its prefix.
	if cleaned := path.Clean(name); cleaned != name || strings.HasPrefix(cleaned, "/") {
		return fmt.Errorf("%w: %q is not a normalized path", ErrInvalidName, name)
	}

	for _, component := range strings.Split(name, "/") {
		if component == "" {
			return fmt.Errorf("%w: %q has an empty path component", ErrInvalidName, name)
		}
		if strings.HasPrefix(component, ".") {
			return fmt.Errorf("%w: %q has a component starting with .", ErrInvalidName, name)
		}
		for _, r := range component {
			if r < 0x20 || r == 0x7f || strings.ContainsRune(" ~^:?*[", r) {
				return fmt.Errorf("%w: %q contains a forbidden character", ErrInvalidName, name)
			}
		}
	}
	return nil
}
