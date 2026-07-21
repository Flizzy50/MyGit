// Package fsutil holds small filesystem helpers shared across mygit.
//
// It exists so the atomic-write pattern lives in exactly one place. The index,
// HEAD, and every ref are all mutable files that a crash must never leave
// half-written, and they all want the same write-temp, fsync, rename dance.
// The object store does not use this — its writes carry object-specific
// concerns (compression, read-only permissions) — but everything mutable does.
package fsutil

import (
	"fmt"
	"os"
	"path/filepath"
)

// AtomicWriteFile writes data to path so that a concurrent reader, or a reader
// after a crash, sees either the previous contents or the complete new
// contents, never a truncated mix.
//
// The guarantee comes from rename(2) being atomic within a filesystem: the new
// bytes are written to a temporary file in the same directory, flushed to
// stable storage, and then renamed onto the target in one indivisible step.
// The temp file must share the target's directory, because rename is only
// atomic within a single filesystem and only guaranteed cheap there.
func AtomicWriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)

	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp*")
	if err != nil {
		return fmt.Errorf("creating temporary file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		tmp.Close()
		os.Remove(tmpName) // harmless no-op once the rename has succeeded
	}()

	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	// Flush to durable storage before publishing the name, so a crash cannot
	// leave the rename visible while the data blocks are still only in cache.
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("syncing %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", path, err)
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		return fmt.Errorf("setting permissions on %s: %w", path, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("publishing %s: %w", path, err)
	}
	return nil
}
