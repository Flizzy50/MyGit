package object

import (
	"bytes"
	"strings"
	"testing"
)

// TestBlobIDsMatchRealGit pins our framing against object IDs produced by real
// Git (`git hash-object --stdin`). These constants are the contract: if a
// refactor changes them, mygit has stopped being able to read Git's objects,
// which is the whole point of copying the format.
func TestBlobIDsMatchRealGit(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    string
	}{
		{"empty", "", "e69de29bb2d1d6434b8b29ae775ad8c2e48c5391"},
		{"hello world", "hello world", "95d09f2b10159347eece71399a7e2e907ea3df4f"},
		{"trailing newline", "hello world\n", "3b18e512dba79e4c8300dd08aeb37f8e728b8dad"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := HashPayload(TypeBlob, []byte(tc.content)).String(); got != tc.want {
				t.Errorf("HashPayload(blob, %q) = %s, want %s", tc.content, got, tc.want)
			}
		})
	}
}

// TestSerializeFraming asserts the exact bytes that get hashed. Everything
// downstream — dedup, integrity, cross-compatibility — depends on this layout.
func TestSerializeFraming(t *testing.T) {
	got := Serialize(TypeBlob, []byte("hello world"))
	want := []byte("blob 11\x00hello world")
	if !bytes.Equal(got, want) {
		t.Fatalf("Serialize = %q, want %q", got, want)
	}
}

// TestTypeAffectsID is the reason the type belongs inside the digest: identical
// payloads stored under different types must not collide.
func TestTypeAffectsID(t *testing.T) {
	payload := []byte("same bytes")
	if HashPayload(TypeBlob, payload) == HashPayload(TypeCommit, payload) {
		t.Fatal("blob and commit with identical payloads produced the same object ID")
	}
}

func TestRoundTrip(t *testing.T) {
	cases := []struct {
		name    string
		typ     Type
		payload []byte
	}{
		{"empty blob", TypeBlob, []byte{}},
		{"text blob", TypeBlob, []byte("hello world")},
		{"binary blob with NUL", TypeBlob, []byte{0x00, 0xff, 0x00, 0x1b, 0x7f}},
		{"large blob", TypeBlob, bytes.Repeat([]byte("x"), 1<<16)},
		{"tree", TypeTree, []byte("100644 file\x00" + strings.Repeat("\x01", 20))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			typ, payload, err := Deserialize(Serialize(tc.typ, tc.payload))
			if err != nil {
				t.Fatalf("Deserialize: %v", err)
			}
			if typ != tc.typ {
				t.Errorf("type = %q, want %q", typ, tc.typ)
			}
			if !bytes.Equal(payload, tc.payload) {
				t.Errorf("payload = %q, want %q", payload, tc.payload)
			}
		})
	}
}

// TestDeserializeRejectsMalformed covers the corruption Deserialize is meant to
// catch. A payload containing NUL bytes (the "binary blob" round-trip above) is
// the subtle one: the split must use the *first* NUL, and the size field is
// what proves the rest of the payload survived intact.
func TestDeserializeRejectsMalformed(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"no NUL", "blob 11 hello world"},
		{"no space", "blob11\x00hello world"},
		{"unknown type", "widget 5\x00hello"},
		{"size too large", "blob 99\x00hello world"},
		{"size too small", "blob 2\x00hello world"},
		{"empty size", "blob \x00hello world"},
		{"signed size", "blob +11\x00hello world"},
		{"non-numeric size", "blob eleven\x00hello world"},
		{"empty input", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := Deserialize([]byte(tc.raw)); err == nil {
				t.Fatalf("Deserialize(%q) succeeded, want error", tc.raw)
			}
		})
	}
}

func TestParseOID(t *testing.T) {
	const valid = "95d09f2b10159347eece71399a7e2e907ea3df4f"

	oid, err := ParseOID(valid)
	if err != nil {
		t.Fatalf("ParseOID(%q): %v", valid, err)
	}
	if oid.String() != valid {
		t.Errorf("round trip = %s, want %s", oid, valid)
	}

	for _, bad := range []string{
		"",
		"95d09f2b", // abbreviated
		"95d09f2b10159347eece71399a7e2e907ea3df4",   // 39 digits
		"95d09f2b10159347eece71399a7e2e907ea3df4ff", // 41 digits
		"zzd09f2b10159347eece71399a7e2e907ea3df4f",  // not hex
	} {
		if _, err := ParseOID(bad); err == nil {
			t.Errorf("ParseOID(%q) succeeded, want error", bad)
		}
	}
}

func TestZeroOID(t *testing.T) {
	var zero OID
	if !zero.IsZero() {
		t.Error("zero value OID should report IsZero")
	}
	if HashPayload(TypeBlob, nil).IsZero() {
		t.Error("a real hash should never report IsZero")
	}
}
