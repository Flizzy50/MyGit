package store

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"mygit/internal/object"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	root := filepath.Join(t.TempDir(), "objects")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("creating objects dir: %v", err)
	}
	return New(root)
}

func TestPutGetRoundTrip(t *testing.T) {
	s := newTestStore(t)
	payloads := map[string][]byte{
		"empty":  {},
		"text":   []byte("hello world"),
		"binary": {0x00, 0x01, 0xff, 0x00, 0x7f},
		"large":  bytes.Repeat([]byte("compress me "), 10000),
	}

	for name, want := range payloads {
		t.Run(name, func(t *testing.T) {
			oid, err := s.Put(object.TypeBlob, want)
			if err != nil {
				t.Fatalf("Put: %v", err)
			}
			typ, got, err := s.Get(oid)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if typ != object.TypeBlob {
				t.Errorf("type = %q, want blob", typ)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("payload round trip mismatch (%d bytes in, %d out)", len(want), len(got))
			}
		})
	}
}

// TestPutIsIdempotent is the deduplication guarantee: identical content stored
// repeatedly yields one ID and one file on disk.
func TestPutIsIdempotent(t *testing.T) {
	s := newTestStore(t)
	payload := []byte("the same content, over and over")

	first, err := s.Put(object.TypeBlob, payload)
	if err != nil {
		t.Fatalf("first Put: %v", err)
	}
	for i := 0; i < 5; i++ {
		again, err := s.Put(object.TypeBlob, payload)
		if err != nil {
			t.Fatalf("repeat Put: %v", err)
		}
		if again != first {
			t.Fatalf("Put returned %s then %s for identical content", first, again)
		}
	}

	if n := countObjects(t, s.Root()); n != 1 {
		t.Errorf("stored %d objects, want 1 — deduplication failed", n)
	}
}

// TestShardedLayout pins the objects/ab/cdef... layout, which the fetch
// protocol and any future repacking both depend on.
func TestShardedLayout(t *testing.T) {
	s := newTestStore(t)
	oid, err := s.Put(object.TypeBlob, []byte("hello world"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	hex := oid.String()
	want := filepath.Join(s.Root(), hex[:2], hex[2:])
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("expected object at %s: %v", want, err)
	}
}

// TestStoredBytesAreCompressed confirms the file on disk is a zlib stream and
// not the plaintext frame — and, importantly, that compression happens *after*
// hashing, so the ID is unaffected by it.
func TestStoredBytesAreCompressed(t *testing.T) {
	s := newTestStore(t)
	payload := bytes.Repeat([]byte("a"), 4096)

	oid, err := s.Put(object.TypeBlob, payload)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if oid != object.HashPayload(object.TypeBlob, payload) {
		t.Fatal("stored ID differs from the pure hash — compression leaked into identity")
	}

	hex := oid.String()
	onDisk, err := os.ReadFile(filepath.Join(s.Root(), hex[:2], hex[2:]))
	if err != nil {
		t.Fatalf("reading object: %v", err)
	}
	if len(onDisk) >= len(payload) {
		t.Errorf("stored %d bytes for a %d-byte highly compressible payload", len(onDisk), len(payload))
	}
	if bytes.Contains(onDisk, []byte("blob 4096\x00")) {
		t.Error("plaintext header found on disk; object was not compressed")
	}
}

func TestGetMissingObject(t *testing.T) {
	s := newTestStore(t)
	oid := object.HashPayload(object.TypeBlob, []byte("never stored"))

	if s.Has(oid) {
		t.Error("Has reported an object that was never written")
	}
	if _, _, err := s.Get(oid); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get error = %v, want ErrNotFound", err)
	}
}

// TestGetDetectsCorruption is the integrity property that content-addressing
// buys. We overwrite a stored object with a valid zlib stream framing
// *different* content, so decompression and parsing both succeed and only the
// hash check can catch it — exactly the bit-rot scenario.
func TestGetDetectsCorruption(t *testing.T) {
	s := newTestStore(t)
	oid, err := s.Put(object.TypeBlob, []byte("original content"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Store different content, then move it onto the first object's path.
	impostor, err := s.Put(object.TypeBlob, []byte("tampered content"))
	if err != nil {
		t.Fatalf("Put impostor: %v", err)
	}
	iHex, oHex := impostor.String(), oid.String()
	impostorPath := filepath.Join(s.Root(), iHex[:2], iHex[2:])
	victimPath := filepath.Join(s.Root(), oHex[:2], oHex[2:])

	tampered, err := os.ReadFile(impostorPath)
	if err != nil {
		t.Fatalf("reading impostor: %v", err)
	}
	// Objects are written read-only, so clear that before overwriting.
	if err := os.Chmod(victimPath, 0o644); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	if err := os.WriteFile(victimPath, tampered, 0o644); err != nil {
		t.Fatalf("tampering: %v", err)
	}

	_, _, err = s.Get(oid)
	if err == nil {
		t.Fatal("Get accepted a tampered object")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("corrupt")) {
		t.Errorf("error = %v, want it to report corruption", err)
	}
}

func TestGetRejectsNonZlibGarbage(t *testing.T) {
	s := newTestStore(t)
	oid := object.HashPayload(object.TypeBlob, []byte("whatever"))
	hex := oid.String()

	dir := filepath.Join(s.Root(), hex[:2])
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, hex[2:]), []byte("not a zlib stream"), 0o644); err != nil {
		t.Fatalf("writing garbage: %v", err)
	}

	if _, _, err := s.Get(oid); err == nil {
		t.Fatal("Get accepted a non-zlib file")
	}
}

func TestPutRejectsUnknownType(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Put(object.Type("widget"), []byte("x")); err == nil {
		t.Fatal("Put accepted an unknown object type")
	}
}

// TestNoTemporaryFilesLeftBehind guards the atomic-write path: after a
// successful write the only thing in the objects tree is the object itself.
func TestNoTemporaryFilesLeftBehind(t *testing.T) {
	s := newTestStore(t)
	for i := 0; i < 10; i++ {
		if _, err := s.Put(object.TypeBlob, []byte{byte(i)}); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}

	entries, err := os.ReadDir(s.Root())
	if err != nil {
		t.Fatalf("reading objects dir: %v", err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			t.Errorf("leftover file in objects root: %s", e.Name())
		}
	}
}

func countObjects(t *testing.T, root string) int {
	t.Helper()
	count := 0
	err := filepath.WalkDir(root, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			count++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking objects: %v", err)
	}
	return count
}
