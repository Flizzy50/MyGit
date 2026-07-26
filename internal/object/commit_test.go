package object

import (
	"strings"
	"testing"
	"time"
)

func testSig(t *testing.T) Signature {
	t.Helper()
	return Signature{
		Name:  "Test Author",
		Email: "author@example.com",
		When:  time.Unix(1700000000, 0).In(time.FixedZone("", 5*3600+30*60)),
	}
}

// TestCommitMatchesRealGit is the acceptance test for Phase 6. The expected ID
// came from real `git commit` with every input pinned through GIT_AUTHOR_DATE
// and friends. Matching it proves the header order, the signature format, the
// blank-line separator, and the message's trailing newline are all exactly
// right — any one of them being off changes the hash.
func TestCommitMatchesRealGit(t *testing.T) {
	tree, err := ParseOID("57f79e95a83ef23a750f432a947b4be8e64e8b97")
	if err != nil {
		t.Fatal(err)
	}

	sig := testSig(t)
	c := &Commit{
		Tree:      tree,
		Author:    sig,
		Committer: sig,
		Message:   "Initial commit",
	}

	const want = "e56d42d988ad8d99aaaf551c1498a66a63c8c168"
	if got := HashPayload(TypeCommit, c.Serialize()).String(); got != want {
		t.Errorf("commit id = %s, want real Git's %s\npayload:\n%s", got, want, c.Serialize())
	}
}

// TestRootCommitOmitsParentLine pins the format difference that makes a root
// commit a root commit: the parent header is absent, not empty or zero.
func TestRootCommitOmitsParentLine(t *testing.T) {
	sig := testSig(t)
	c := &Commit{Tree: HashPayload(TypeTree, nil), Author: sig, Committer: sig, Message: "root"}

	if !c.IsRoot() {
		t.Error("commit with no parents should report IsRoot")
	}
	if strings.Contains(string(c.Serialize()), "parent") {
		t.Errorf("root commit contains a parent line:\n%s", c.Serialize())
	}
}

func TestMergeCommitHasMultipleParents(t *testing.T) {
	sig := testSig(t)
	a, b := HashPayload(TypeCommit, []byte("a")), HashPayload(TypeCommit, []byte("b"))
	c := &Commit{Tree: HashPayload(TypeTree, nil), Parents: []OID{a, b}, Author: sig, Committer: sig, Message: "merge"}

	if !c.IsMerge() {
		t.Error("commit with two parents should report IsMerge")
	}

	payload := string(c.Serialize())
	if got := strings.Count(payload, "parent "); got != 2 {
		t.Errorf("found %d parent lines, want 2:\n%s", got, payload)
	}
	// Parent order is significant: the first parent is the branch that was
	// merged into, which is what "mainline" means for later history traversal.
	if idxA, idxB := strings.Index(payload, a.String()), strings.Index(payload, b.String()); idxA > idxB {
		t.Error("parent order was not preserved")
	}
}

func TestCommitRoundTrip(t *testing.T) {
	sig := testSig(t)
	orig := &Commit{
		Tree:      HashPayload(TypeTree, []byte("tree")),
		Parents:   []OID{HashPayload(TypeCommit, []byte("p1")), HashPayload(TypeCommit, []byte("p2"))},
		Author:    sig,
		Committer: Signature{Name: "Other Person", Email: "other@example.com", When: sig.When.Add(time.Hour)},
		Message:   "Subject line\n\nA body paragraph.\n\nAnother paragraph.\n",
	}

	parsed, err := ParseCommit(orig.Serialize())
	if err != nil {
		t.Fatalf("ParseCommit: %v", err)
	}

	if parsed.Tree != orig.Tree {
		t.Errorf("tree = %s, want %s", parsed.Tree, orig.Tree)
	}
	if len(parsed.Parents) != 2 || parsed.Parents[0] != orig.Parents[0] || parsed.Parents[1] != orig.Parents[1] {
		t.Errorf("parents = %v, want %v", parsed.Parents, orig.Parents)
	}
	if parsed.Author.Name != orig.Author.Name || parsed.Author.Email != orig.Author.Email {
		t.Errorf("author = %+v, want %+v", parsed.Author, orig.Author)
	}
	if !parsed.Author.When.Equal(orig.Author.When) {
		t.Errorf("author time = %v, want %v", parsed.Author.When, orig.Author.When)
	}
	if parsed.Committer.Name != orig.Committer.Name {
		t.Errorf("committer = %+v, want %+v", parsed.Committer, orig.Committer)
	}
	if parsed.Message != orig.Message {
		t.Errorf("message = %q, want %q", parsed.Message, orig.Message)
	}
}

// TestMessageWithBlankLinesSurvives confirms the blank line after the headers
// is the only delimiter: a message containing blank lines needs no escaping.
func TestMessageWithBlankLinesSurvives(t *testing.T) {
	sig := testSig(t)
	msg := "Subject\n\nBody with a blank line above.\n\ntree fake\nparent fake\n"
	c := &Commit{Tree: HashPayload(TypeTree, nil), Author: sig, Committer: sig, Message: msg}

	parsed, err := ParseCommit(c.Serialize())
	if err != nil {
		t.Fatalf("ParseCommit: %v", err)
	}
	// Lines that look like headers inside the body must not be parsed as such.
	if len(parsed.Parents) != 0 {
		t.Errorf("body text was parsed as a parent header: %v", parsed.Parents)
	}
	if parsed.Message != msg {
		t.Errorf("message = %q, want %q", parsed.Message, msg)
	}
}

// TestMessageNewlineNormalized shows why normalization exists: the same logical
// message typed with different trailing whitespace must hash identically.
func TestMessageNewlineNormalized(t *testing.T) {
	sig := testSig(t)
	mk := func(msg string) string {
		c := &Commit{Tree: HashPayload(TypeTree, nil), Author: sig, Committer: sig, Message: msg}
		return HashPayload(TypeCommit, c.Serialize()).String()
	}
	if mk("hello") != mk("hello\n") || mk("hello") != mk("hello\n\n\n") {
		t.Error("trailing newlines changed the commit id")
	}
}

// TestSignatureRoundTrip covers the right-to-left parse, including names that
// contain spaces and unusual characters.
func TestSignatureRoundTrip(t *testing.T) {
	cases := []Signature{
		testSig(t),
		{Name: "Ada Lovelace", Email: "ada@example.com", When: time.Unix(0, 0).In(time.UTC)},
		{Name: "Name With  Multiple   Spaces", Email: "x@y.z", When: time.Unix(1234567890, 0).In(time.FixedZone("", -8*3600))},
		{Name: "Zero Offset", Email: "z@z.z", When: time.Unix(1700000000, 0).In(time.UTC)},
	}
	for _, want := range cases {
		t.Run(want.Name, func(t *testing.T) {
			got, err := ParseSignature(want.String())
			if err != nil {
				t.Fatalf("ParseSignature(%q): %v", want.String(), err)
			}
			if got.Name != want.Name || got.Email != want.Email {
				t.Errorf("got %q <%s>, want %q <%s>", got.Name, got.Email, want.Name, want.Email)
			}
			if !got.When.Equal(want.When) {
				t.Errorf("time = %v, want %v", got.When, want.When)
			}
			// The offset must survive, not just the instant.
			if got.String() != want.String() {
				t.Errorf("re-serialized as %q, want %q", got.String(), want.String())
			}
		})
	}
}

// TestSignatureHalfHourZone specifically guards +0530, where a naive
// hours-only offset implementation would silently drop the minutes.
func TestSignatureHalfHourZone(t *testing.T) {
	sig := testSig(t)
	if got := sig.String(); !strings.HasSuffix(got, " 1700000000 +0530") {
		t.Errorf("signature = %q, want it to end with \" 1700000000 +0530\"", got)
	}
}

func TestParseCommitRejectsMalformed(t *testing.T) {
	cases := map[string]string{
		"no tree":         "author A <a@b> 1700000000 +0000\ncommitter A <a@b> 1700000000 +0000\n\nmsg\n",
		"no author":       "tree 57f79e95a83ef23a750f432a947b4be8e64e8b97\n\nmsg\n",
		"bad tree oid":    "tree notahash\nauthor A <a@b> 1 +0000\ncommitter A <a@b> 1 +0000\n\nmsg\n",
		"bad signature":   "tree 57f79e95a83ef23a750f432a947b4be8e64e8b97\nauthor no-email-here\ncommitter A <a@b> 1 +0000\n\nmsg\n",
		"bad timezone":    "tree 57f79e95a83ef23a750f432a947b4be8e64e8b97\nauthor A <a@b> 1 XYZ\ncommitter A <a@b> 1 +0000\n\nmsg\n",
		"headerless line": "tree 57f79e95a83ef23a750f432a947b4be8e64e8b97\ngarbage\n\nmsg\n",
		"empty":           "",
	}
	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseCommit([]byte(payload)); err == nil {
				t.Errorf("ParseCommit(%s) succeeded, want error", name)
			}
		})
	}
}

// TestParseCommitIgnoresUnknownHeaders keeps mygit able to read real history,
// which carries gpgsig and other fields we do not implement.
func TestParseCommitIgnoresUnknownHeaders(t *testing.T) {
	payload := "tree 57f79e95a83ef23a750f432a947b4be8e64e8b97\n" +
		"author A <a@b.c> 1700000000 +0000\n" +
		"committer A <a@b.c> 1700000000 +0000\n" +
		"gpgsig -----BEGIN PGP SIGNATURE-----\n" +
		"encoding ISO-8859-1\n" +
		"\nSigned commit\n"

	c, err := ParseCommit([]byte(payload))
	if err != nil {
		t.Fatalf("ParseCommit: %v", err)
	}
	if c.Summary() != "Signed commit" {
		t.Errorf("summary = %q, want %q", c.Summary(), "Signed commit")
	}
}

func TestSummary(t *testing.T) {
	cases := map[string]string{
		"one line":               "one line",
		"subject\n\nbody here\n": "subject",
		"\n\n  padded\n":         "padded",
	}
	for msg, want := range cases {
		c := &Commit{Message: msg}
		if got := c.Summary(); got != want {
			t.Errorf("Summary(%q) = %q, want %q", msg, got, want)
		}
	}
}
