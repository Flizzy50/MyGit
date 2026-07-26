package object

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Signature is a name, an email, and a moment: the "who and when" of a commit.
//
// The timestamp is stored as Unix seconds plus a separate UTC offset, rather
// than as a formatted local time. That split is deliberate and worth noticing:
// the seconds give a globally comparable instant, while the offset preserves
// the author's local wall clock as a human detail. Git can therefore sort
// commits from every timezone consistently and still show you "3pm" as the
// author experienced it.
type Signature struct {
	Name  string
	Email string
	When  time.Time
}

// String renders the signature exactly as it appears inside a commit object:
//
//	Name <email> 1700000000 +0530
//
// The offset uses Go's reference layout "-0700", which emits a sign, two hours
// digits, and two minutes digits — matching Git byte for byte, including for
// half-hour zones like +0530.
func (s Signature) String() string {
	return fmt.Sprintf("%s <%s> %d %s", s.Name, s.Email, s.When.Unix(), s.When.Format("-0700"))
}

// ParseSignature reads the form String produces.
//
// Parsing works right to left — offset, then timestamp, then the bracketed
// email — because a person's name may contain spaces and even angle brackets,
// while the trailing fields have fixed shape. Scanning from the end is the only
// unambiguous way to split the line.
func ParseSignature(line string) (Signature, error) {
	openAngle := strings.LastIndexByte(line, '<')
	closeAngle := strings.LastIndexByte(line, '>')
	if openAngle < 0 || closeAngle < openAngle {
		return Signature{}, fmt.Errorf("malformed signature %q: no <email>", line)
	}

	name := strings.TrimSpace(line[:openAngle])
	email := line[openAngle+1 : closeAngle]

	fields := strings.Fields(line[closeAngle+1:])
	if len(fields) != 2 {
		return Signature{}, fmt.Errorf("malformed signature %q: want timestamp and timezone", line)
	}

	secs, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil {
		return Signature{}, fmt.Errorf("malformed signature %q: bad timestamp", line)
	}

	offset, err := parseZoneOffset(fields[1])
	if err != nil {
		return Signature{}, fmt.Errorf("malformed signature %q: %w", line, err)
	}

	return Signature{
		Name:  name,
		Email: email,
		When:  time.Unix(secs, 0).In(time.FixedZone("", offset)),
	}, nil
}

// parseZoneOffset converts "+0530" into seconds east of UTC.
func parseZoneOffset(s string) (int, error) {
	if len(s) != 5 || (s[0] != '+' && s[0] != '-') {
		return 0, fmt.Errorf("bad timezone %q", s)
	}
	hours, err := strconv.Atoi(s[1:3])
	if err != nil {
		return 0, fmt.Errorf("bad timezone %q", s)
	}
	mins, err := strconv.Atoi(s[3:5])
	if err != nil {
		return 0, fmt.Errorf("bad timezone %q", s)
	}
	offset := hours*3600 + mins*60
	if s[0] == '-' {
		offset = -offset
	}
	return offset, nil
}

// Commit is a snapshot bound to a point in history.
//
// A commit is astonishingly small: one root tree, some parent IDs, two
// signatures, and a message. Everything people attribute to commits — diffs,
// "what changed", history — is derived, never stored. A commit records a
// complete state, not a change, and the change is computed on demand by
// diffing against a parent.
//
// The parent pointers are what turn a pile of snapshots into a graph, and their
// direction is the design decision that makes Git work. Each commit names its
// parents; parents know nothing of their children. That backwards edge is why
// commits are immutable: appending a child never touches the parent, so a
// commit's ID — and everything reachable from it — is frozen the moment it is
// written.
//
//	       parent pointers point BACKWARD
//	A ◀────── B ◀────── C          (C is HEAD)
//	▲                              writing D touches nothing:
//	└── each commit names its parent, never its children
//
// Multiple parents mean a merge commit, which is what makes the structure a DAG
// rather than a linked list. Phase 11 depends on this entirely.
type Commit struct {
	Tree      OID
	Parents   []OID
	Author    Signature
	Committer Signature
	Message   string
}

// IsRoot reports whether this commit has no parents, making it a starting point
// of history. A repository can have several, notably after grafting unrelated
// histories together.
func (c *Commit) IsRoot() bool { return len(c.Parents) == 0 }

// IsMerge reports whether this commit has more than one parent.
func (c *Commit) IsMerge() bool { return len(c.Parents) > 1 }

// Serialize renders the commit to its stored payload:
//
//	tree <hex>
//	parent <hex>        (repeated; absent on a root commit)
//	author <signature>
//	committer <signature>
//
//	<message>
//
// Note the contrast with tree objects, which embed object IDs as twenty raw
// bytes. Commits use forty hex characters instead. The reason is that a commit
// is a text format meant to stay human-readable — you can `cat` one — while a
// tree is a dense binary record read only by machines. Same IDs, two encodings,
// chosen per format for different consumers.
//
// The blank line separating headers from the message is load-bearing: it is the
// only delimiter, which is why a message can contain anything at all, including
// blank lines, without escaping.
func (c *Commit) Serialize() []byte {
	var b strings.Builder

	fmt.Fprintf(&b, "tree %s\n", c.Tree)
	for _, p := range c.Parents {
		fmt.Fprintf(&b, "parent %s\n", p)
	}
	fmt.Fprintf(&b, "author %s\n", c.Author)
	fmt.Fprintf(&b, "committer %s\n", c.Committer)
	b.WriteByte('\n')
	b.WriteString(normalizeMessage(c.Message))

	return []byte(b.String())
}

// normalizeMessage guarantees the message ends with exactly one newline, as
// Git does. Without normalization the same logical message typed two ways
// would produce two different commit IDs.
func normalizeMessage(msg string) string {
	msg = strings.TrimRight(msg, "\n")
	if msg == "" {
		return ""
	}
	return msg + "\n"
}

// ParseCommit decodes a commit object's payload.
func ParseCommit(payload []byte) (*Commit, error) {
	text := string(payload)

	// The first empty line ends the headers; everything after it is the
	// message, taken verbatim.
	var header, message string
	if i := strings.Index(text, "\n\n"); i >= 0 {
		header, message = text[:i], text[i+2:]
	} else {
		header = strings.TrimRight(text, "\n")
	}

	c := &Commit{Message: message}
	var sawTree, sawAuthor, sawCommitter bool

	for _, line := range strings.Split(header, "\n") {
		key, value, found := strings.Cut(line, " ")
		if !found {
			return nil, fmt.Errorf("malformed commit header %q", line)
		}

		switch key {
		case "tree":
			oid, err := ParseOID(value)
			if err != nil {
				return nil, fmt.Errorf("commit tree: %w", err)
			}
			c.Tree, sawTree = oid, true

		case "parent":
			oid, err := ParseOID(value)
			if err != nil {
				return nil, fmt.Errorf("commit parent: %w", err)
			}
			c.Parents = append(c.Parents, oid)

		case "author":
			sig, err := ParseSignature(value)
			if err != nil {
				return nil, err
			}
			c.Author, sawAuthor = sig, true

		case "committer":
			sig, err := ParseSignature(value)
			if err != nil {
				return nil, err
			}
			c.Committer, sawCommitter = sig, true

		default:
			// Unknown headers are ignored rather than rejected. Real commits
			// carry fields mygit does not implement — gpgsig, encoding,
			// mergetag — and a reader that refused them could not read real
			// history. Preserving forward compatibility in a format this
			// long-lived matters more than strictness here.
		}
	}

	if !sawTree {
		return nil, fmt.Errorf("malformed commit: no tree header")
	}
	if !sawAuthor || !sawCommitter {
		return nil, fmt.Errorf("malformed commit: missing author or committer")
	}
	return c, nil
}

// Summary returns the first line of the message, the conventional one-line
// description used by log output.
func (c *Commit) Summary() string {
	line, _, _ := strings.Cut(strings.TrimSpace(c.Message), "\n")
	return line
}
