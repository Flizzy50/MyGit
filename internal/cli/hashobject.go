package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"mygit/internal/object"
	"mygit/internal/repository"
)

var hashObjectCmd = &Command{
	Name:    "hash-object",
	Summary: "compute an object ID, optionally storing the object",
	Usage:   "mygit hash-object [-t <type>] [-w] [--stdin] [<file>...]",
	Run:     runHashObject,
}

// runHashObject is the plumbing command that makes content-addressing visible.
//
// Hashing and storing are separate flags on purpose, and the split mirrors a
// real architectural boundary: computing an ID is a pure function of bytes,
// requiring no repository and having no side effects, while writing needs a
// database. Without -w this command is a calculator you can run anywhere.
func runHashObject(env *Env, args []string) error {
	fs := newFlagSet("hash-object")
	typeName := fs.String("t", string(object.TypeBlob), "object type")
	write := fs.Bool("w", false, "write the object into the object database")
	stdin := fs.Bool("stdin", false, "read the object from standard input")
	if err := fs.Parse(args); err != nil {
		return err
	}

	typ := object.Type(*typeName)
	if !typ.Valid() {
		return fmt.Errorf("invalid object type %q", *typeName)
	}
	if *stdin == (fs.NArg() > 0) {
		return fmt.Errorf("%w: give either --stdin or one or more files", errUsage)
	}

	// Resolve the repository once, up front, so a run that stores twenty files
	// fails immediately if there is nowhere to store them rather than halfway
	// through. Only -w needs a repository.
	var repo *repository.Repository
	if *write {
		var err error
		if repo, err = repository.Discover(env.Dir); err != nil {
			return err
		}
	}

	emit := func(payload []byte) error {
		if !*write {
			fmt.Fprintln(env.Stdout, object.HashPayload(typ, payload))
			return nil
		}
		oid, err := repo.Objects.Put(typ, payload)
		if err != nil {
			return err
		}
		fmt.Fprintln(env.Stdout, oid)
		return nil
	}

	if *stdin {
		payload, err := io.ReadAll(env.Stdin)
		if err != nil {
			return fmt.Errorf("reading standard input: %w", err)
		}
		return emit(payload)
	}

	for _, name := range fs.Args() {
		path := name
		if !filepath.IsAbs(path) {
			path = filepath.Join(env.Dir, path)
		}
		// Reading whole files into memory is the simplification here. Real Git
		// streams large blobs and hashes incrementally, because the frame's
		// size field must be known before the payload is written — Git solves
		// that by stat-ing the file for its length, then streaming. Our
		// approach is O(file size) in memory; see the notes in package store.
		payload, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("reading %s: %w", name, err)
		}
		if err := emit(payload); err != nil {
			return err
		}
	}
	return nil
}
