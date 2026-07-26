package cli

import (
	"fmt"
	"io"

	"mygit/internal/object"
	"mygit/internal/repository"
)

var catFileCmd = &Command{
	Name:    "cat-file",
	Summary: "inspect a stored object's type, size, or contents",
	Usage:   "mygit cat-file (-t | -s | -p) <object>",
	Run:     runCatFile,
}

// runCatFile is the inverse of hash-object and completes the round trip:
// bytes in, ID out, ID in, identical bytes out.
//
// The three modes exist because the framing carries three separable facts.
// -t and -s answer questions from the header alone, which is why real Git can
// serve them without decompressing an entire large blob; our loose-object
// reader decompresses everything, a simplification worth naming.
func runCatFile(env *Env, args []string) error {
	fs := newFlagSet("cat-file")
	showType := fs.Bool("t", false, "show the object type")
	showSize := fs.Bool("s", false, "show the object size in bytes")
	pretty := fs.Bool("p", false, "pretty-print the object contents")
	if err := fs.Parse(args); err != nil {
		return err
	}

	selected := 0
	for _, on := range []bool{*showType, *showSize, *pretty} {
		if on {
			selected++
		}
	}
	if selected != 1 || fs.NArg() != 1 {
		return fmt.Errorf("%w: give exactly one of -t, -s, -p and one object id", errUsage)
	}

	repo, err := repository.Discover(env.Dir)
	if err != nil {
		return err
	}

	oid, err := object.ParseOID(fs.Arg(0))
	if err != nil {
		return err
	}

	typ, payload, err := repo.Objects.Get(oid)
	if err != nil {
		return err
	}

	switch {
	case *showType:
		fmt.Fprintln(env.Stdout, typ)
	case *showSize:
		fmt.Fprintln(env.Stdout, len(payload))
	case *pretty:
		return prettyPrint(env, typ, payload)
	}
	return nil
}

// prettyPrint renders an object for human consumption, which means something
// different for each type.
//
// A blob is written verbatim; a tree is decoded into a readable listing. That
// asymmetry is the point of the -p flag: the stored bytes of a tree are binary
// (raw 20-byte IDs, no newlines) and dumping them to a terminal would be
// useless. `cat-file tree <id>` in real Git emits those raw bytes, while -p
// decodes — two different questions about the same object.
func prettyPrint(env *Env, typ object.Type, payload []byte) error {
	switch typ {
	case object.TypeTree:
		tree, err := object.ParseTree(payload)
		if err != nil {
			return err
		}
		_, err = io.WriteString(env.Stdout, tree.PrettyPrint())
		return err
	default:
		// Blob content is emitted byte for byte with no trailing newline added,
		// because adding one would misreport every file that lacks one.
		_, err := env.Stdout.Write(payload)
		return err
	}
}
