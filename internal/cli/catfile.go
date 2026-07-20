package cli

import (
	"fmt"

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
		// A blob's payload is its file content verbatim, so pretty-printing is
		// just writing it out — no trailing newline is added, because doing so
		// would misreport files that lack one. Trees and commits will need
		// real formatters once those types exist.
		if _, err := env.Stdout.Write(payload); err != nil {
			return err
		}
	}
	return nil
}
