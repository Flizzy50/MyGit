package object

import (
	"bytes"
	"testing"
)

func oidOf(s string) OID { return HashPayload(TypeBlob, []byte(s)) }

// TestTreeSortsDirectoriesWithSlash is the headline test of Phase 5.
//
// Given a file "src.txt" and a directory "src", a naive sort of the plain names
// puts "src" first because it is a prefix. Git compares the directory as
// "src/", and '/' (0x2F) is greater than '.' (0x2E), so "src.txt" comes first.
// Getting this backwards produces trees that look correct, parse correctly, and
// hash differently from Git's forever.
func TestTreeSortsDirectoriesWithSlash(t *testing.T) {
	tree := Tree{
		{Name: "src", Mode: ModeTree, OID: oidOf("dir")},
		{Name: "src.txt", Mode: ModeRegular, OID: oidOf("file")},
		{Name: "a.txt", Mode: ModeRegular, OID: oidOf("a")},
	}
	tree.Sort()

	want := []string{"a.txt", "src.txt", "src"}
	for i, name := range want {
		if tree[i].Name != name {
			t.Fatalf("order = [%s %s %s], want %v",
				tree[0].Name, tree[1].Name, tree[2].Name, want)
		}
	}
}

// TestTreeSortAgainstRealGit pins the serialized bytes and resulting ID of the
// exact tree real Git produced for a/, src.txt, and src/ in a scratch repo.
// If the sort rule or the wire format regresses, this fails immediately.
func TestTreeSortAgainstRealGit(t *testing.T) {
	blobA, err := ParseOID("78981922613b2afb6025042ff6bd878ac1994e85")
	if err != nil {
		t.Fatal(err)
	}
	blobSrcTxt, err := ParseOID("adf54b79ca0749484a54978451840cdc0d986689")
	if err != nil {
		t.Fatal(err)
	}
	subTree, err := ParseOID("14210ba207b65ac3131da4cf407acc6cb8565b3f")
	if err != nil {
		t.Fatal(err)
	}

	// Deliberately constructed out of order to prove Serialize canonicalizes.
	tree := Tree{
		{Name: "src", Mode: ModeTree, OID: subTree},
		{Name: "a.txt", Mode: ModeRegular, OID: blobA},
		{Name: "src.txt", Mode: ModeRegular, OID: blobSrcTxt},
	}

	const wantID = "2b0c7d7c422758b229cd6ff32ec72950978a5bba"
	if got := HashPayload(TypeTree, tree.Serialize()).String(); got != wantID {
		t.Errorf("tree id = %s, want real Git's %s", got, wantID)
	}
}

// TestSerializeUsesRawOIDsAndBareModes locks in the two format details that
// differ from how IDs and modes appear everywhere else in mygit.
func TestSerializeUsesRawOIDsAndBareModes(t *testing.T) {
	oid := oidOf("content")
	tree := Tree{{Name: "file.txt", Mode: ModeRegular, OID: oid}}
	raw := tree.Serialize()

	want := append([]byte("100644 file.txt\x00"), oid[:]...)
	if !bytes.Equal(raw, want) {
		t.Fatalf("serialized = %q, want %q", raw, want)
	}
	// The hex form must not appear: IDs are stored as 20 raw bytes.
	if bytes.Contains(raw, []byte(oid.String())) {
		t.Error("tree contains a hex object id; it must be raw binary")
	}
}

// TestDirectoryModeHasNoLeadingZero guards the "40000" versus "040000" trap:
// the stored bytes use five characters, and only the pretty printer pads.
func TestDirectoryModeHasNoLeadingZero(t *testing.T) {
	tree := Tree{{Name: "src", Mode: ModeTree, OID: oidOf("x")}}

	if raw := tree.Serialize(); !bytes.HasPrefix(raw, []byte("40000 src\x00")) {
		t.Errorf("serialized directory entry = %q, want it to start with %q", raw, "40000 src\x00")
	}
	if pretty := tree.PrettyPrint(); !bytes.HasPrefix([]byte(pretty), []byte("040000 tree ")) {
		t.Errorf("pretty form = %q, want it to start with %q", pretty, "040000 tree ")
	}
}

func TestTreeRoundTrip(t *testing.T) {
	orig := Tree{
		{Name: "README.md", Mode: ModeRegular, OID: oidOf("readme")},
		{Name: "run.sh", Mode: ModeExecutable, OID: oidOf("script")},
		{Name: "link", Mode: ModeSymlink, OID: oidOf("target")},
		{Name: "src", Mode: ModeTree, OID: oidOf("subtree")},
	}

	parsed, err := ParseTree(orig.Serialize())
	if err != nil {
		t.Fatalf("ParseTree: %v", err)
	}
	if len(parsed) != len(orig) {
		t.Fatalf("got %d entries, want %d", len(parsed), len(orig))
	}
	orig.Sort()
	for i := range orig {
		if parsed[i] != orig[i] {
			t.Errorf("entry %d = %+v, want %+v", i, parsed[i], orig[i])
		}
	}
}

// TestEmptyTreeMatchesGit checks the well-known empty tree ID, which shows up
// in real repositories as the tree of a commit that adds nothing.
func TestEmptyTreeMatchesGit(t *testing.T) {
	const want = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"
	if got := HashPayload(TypeTree, Tree{}.Serialize()).String(); got != want {
		t.Errorf("empty tree id = %s, want %s", got, want)
	}
}

// TestSerializeIsDeterministic is the property deduplication rests on: the same
// entries in any order must produce identical bytes.
func TestSerializeIsDeterministic(t *testing.T) {
	a := Tree{
		{Name: "z.txt", Mode: ModeRegular, OID: oidOf("z")},
		{Name: "a.txt", Mode: ModeRegular, OID: oidOf("a")},
		{Name: "m", Mode: ModeTree, OID: oidOf("m")},
	}
	b := Tree{
		{Name: "m", Mode: ModeTree, OID: oidOf("m")},
		{Name: "a.txt", Mode: ModeRegular, OID: oidOf("a")},
		{Name: "z.txt", Mode: ModeRegular, OID: oidOf("z")},
	}
	if !bytes.Equal(a.Serialize(), b.Serialize()) {
		t.Fatal("trees with identical entries serialized differently")
	}
}

func TestParseTreeRejectsMalformed(t *testing.T) {
	valid := Tree{{Name: "f", Mode: ModeRegular, OID: oidOf("x")}}.Serialize()

	cases := map[string][]byte{
		"no space after mode": []byte("100644file\x00" + string(make([]byte, 20))),
		"no NUL after name":   []byte("100644 file"),
		"truncated oid":       valid[:len(valid)-5],
		"bad mode":            []byte("xyz file\x00" + string(make([]byte, 20))),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseTree(raw); err == nil {
				t.Errorf("ParseTree(%s) succeeded, want error", name)
			}
		})
	}
}

func TestParseEmptyTree(t *testing.T) {
	tree, err := ParseTree(nil)
	if err != nil {
		t.Fatalf("ParseTree of empty payload: %v", err)
	}
	if len(tree) != 0 {
		t.Errorf("got %d entries, want 0", len(tree))
	}
}
