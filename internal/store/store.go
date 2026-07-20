// Package store implements the loose-object database: the content-addressable
// storage engine underneath everything else in mygit.
//
// On-disk layout mirrors real Git. An object with ID "95d09f2b10159347..."
// lives at:
//
//	objects/95/d09f2b10159347...
//
// The two-character shard directory exists for the filesystem's benefit, not
// Git's. A busy repository holds hundreds of thousands of objects, and many
// filesystems degrade badly on directories with that many entries — ext3
// without dir_index does linear scans, and even modern filesystems suffer in
// readdir and in directory-block cache pressure. Splitting on the first byte
// yields 256 buckets and, because hash output is uniform, they fill evenly.
// That is the entire justification: uniform hashing gives balanced sharding
// for free, which is also why the same trick appears in consistent hashing and
// in sharded key-value stores.
//
// File contents are the framed object (see package object) run through zlib.
// Note the ordering: the ID is the hash of the *uncompressed* frame.
// Compression is a storage-layer decision that must not influence identity —
// otherwise changing the compression level would rename every object in the
// repository.
package store

import (
	"compress/zlib"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"mygit/internal/object"
)

// ErrNotFound reports that an object is absent from this database. Callers
// distinguish it with errors.Is, since "you don't have it" and "your disk is
// broken" demand very different responses.
var ErrNotFound = errors.New("object not found")

// Store is a content-addressable object database backed by loose files.
type Store struct {
	root string // absolute path to the objects directory
}

// New returns a Store rooted at the given objects directory.
func New(root string) *Store { return &Store{root: root} }

// Root returns the objects directory this Store manages.
func (s *Store) Root() string { return s.root }

// path maps an object ID to its on-disk location.
func (s *Store) path(oid object.OID) string {
	hex := oid.String()
	return filepath.Join(s.root, hex[:2], hex[2:])
}

// Has reports whether the object is already present.
//
// Because IDs are derived from content, presence alone proves the stored bytes
// are the ones we were about to write. There is no need to compare contents,
// and no such thing as a stale entry.
func (s *Store) Has(oid object.OID) bool {
	_, err := os.Stat(s.path(oid))
	return err == nil
}

// Put frames, hashes, compresses, and stores a payload, returning its ID.
//
// Put is idempotent. Storing content that already exists is a no-op that
// returns the same ID, which is what makes deduplication automatic: a file
// unchanged across a thousand commits occupies one object on disk.
func (s *Store) Put(typ object.Type, payload []byte) (object.OID, error) {
	if !typ.Valid() {
		return object.OID{}, fmt.Errorf("cannot store unknown object type %q", typ)
	}

	serialized := object.Serialize(typ, payload)
	oid := object.Hash(serialized)

	if s.Has(oid) {
		return oid, nil
	}
	if err := s.write(oid, serialized); err != nil {
		return object.OID{}, err
	}
	return oid, nil
}

// Get loads an object by ID and returns its type and payload.
func (s *Store) Get(oid object.OID) (object.Type, []byte, error) {
	f, err := os.Open(s.path(oid))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil, fmt.Errorf("%w: %s", ErrNotFound, oid)
		}
		return "", nil, fmt.Errorf("opening object %s: %w", oid, err)
	}
	defer f.Close()

	zr, err := zlib.NewReader(f)
	if err != nil {
		return "", nil, fmt.Errorf("object %s is corrupt: %w", oid, err)
	}
	defer zr.Close()

	serialized, err := io.ReadAll(zr)
	if err != nil {
		return "", nil, fmt.Errorf("object %s is corrupt: %w", oid, err)
	}

	// Verify that the content still hashes to the name we looked it up under.
	// This is the integrity half of content-addressable storage: the key is a
	// checksum, so silent corruption anywhere between write and read — bit rot,
	// a truncated write, a bad cable — is detected rather than propagated into
	// a checkout. Real Git performs this check during fsck and when receiving
	// objects over the network; doing it on every read costs one SHA-1 pass
	// over data we have already decompressed, which is cheap next to the I/O.
	if actual := object.Hash(serialized); actual != oid {
		return "", nil, fmt.Errorf("object %s is corrupt: content hashes to %s", oid, actual)
	}

	return object.Deserialize(serialized)
}

// write persists a serialized object atomically.
//
// The sequence is write-to-temp, fsync, rename. Rename within a filesystem is
// atomic, so a reader observes either no file or the complete file, never a
// half-written one. Writing directly to the final path would leave a truncated
// object behind if the process died mid-write — and because that file's name
// asserts a hash its contents no longer satisfy, it would poison every later
// read of that ID. Objects are immutable, so this is the only write path that
// ever needs to be correct.
func (s *Store) write(oid object.OID, serialized []byte) error {
	final := s.path(oid)
	if err := os.MkdirAll(filepath.Dir(final), 0o755); err != nil {
		return fmt.Errorf("creating object directory: %w", err)
	}

	tmp, err := os.CreateTemp(s.root, "tmp_obj_*")
	if err != nil {
		return fmt.Errorf("creating temporary object: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		tmp.Close()
		os.Remove(tmpName) // no-op once the rename has succeeded
	}()

	zw := zlib.NewWriter(tmp)
	if _, err := zw.Write(serialized); err != nil {
		return fmt.Errorf("compressing object %s: %w", oid, err)
	}
	if err := zw.Close(); err != nil {
		return fmt.Errorf("flushing object %s: %w", oid, err)
	}
	// Force the bytes to durable storage before the rename publishes the name.
	// Without this, a crash can leave the directory entry visible while the
	// data blocks are still in page cache and lost.
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("syncing object %s: %w", oid, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing object %s: %w", oid, err)
	}

	// Objects are immutable; mark them read-only so accidental writes fail
	// loudly instead of silently invalidating the ID. Real Git does the same.
	if err := os.Chmod(tmpName, 0o444); err != nil {
		return fmt.Errorf("marking object %s read-only: %w", oid, err)
	}

	if err := os.Rename(tmpName, final); err != nil {
		// Another process may have written this object between our Has check
		// and now. That is harmless: content-addressing guarantees it wrote
		// byte-identical content, so the race has no losing side.
		if _, statErr := os.Stat(final); statErr == nil {
			return nil
		}
		return fmt.Errorf("publishing object %s: %w", oid, err)
	}
	return nil
}
