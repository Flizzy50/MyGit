// Package cli implements mygit's command dispatcher.
//
// This is hand-rolled on top of the standard library's flag package rather
// than built on a framework. A subcommand dispatcher is roughly forty lines,
// and writing it keeps the dependency list empty and the control flow visible:
// argv comes in, a command is looked up, it parses its own flags, it returns an
// error, the error becomes an exit code. Nothing is hidden behind reflection or
// registration magic.
//
// The layering rule this package enforces: commands parse arguments and format
// output. They contain no storage logic. Everything a command does is a call
// into object, store, or repository. That is what keeps those packages testable
// without a terminal, and what would let a future daemon or HTTP server reuse
// them unchanged.
package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"text/tabwriter"
)

// Command is one mygit subcommand.
type Command struct {
	Name    string
	Summary string // one line, shown in the command list
	Usage   string // synopsis line, shown on error and with -h
	Run     func(env *Env, args []string) error
}

// Env carries a command's I/O streams and working directory.
//
// Passing these explicitly instead of reaching for os.Stdout and os.Getwd lets
// tests drive commands with buffers and temporary directories, and run them in
// parallel — which is impossible when commands mutate process-global state.
type Env struct {
	Dir    string
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

// DefaultEnv returns an Env wired to the current process.
func DefaultEnv() *Env {
	dir, err := os.Getwd()
	if err != nil {
		dir = "."
	}
	return &Env{Dir: dir, Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr}
}

var registry = map[string]*Command{}

func register(cmds ...*Command) {
	for _, c := range cmds {
		registry[c.Name] = c
	}
}

func init() {
	register(
		initCmd,
		hashObjectCmd,
		catFileCmd,
		addCmd,
		lsFilesCmd,
		writeTreeCmd,
		commitCmd,
		revParseCmd,
		logCmd,
		checkoutCmd,
		branchCmd,
		mergeCmd,
	)
}

// errUsage signals that a command was invoked incorrectly, so the dispatcher
// prints the synopsis rather than a bare error.
var errUsage = errors.New("usage")

// Main dispatches args (excluding the program name) and returns an exit code.
func Main(env *Env, args []string) int {
	if len(args) == 0 {
		printUsage(env.Stdout)
		return 0
	}

	name := args[0]
	if name == "help" || name == "-h" || name == "--help" {
		printUsage(env.Stdout)
		return 0
	}

	cmd, ok := registry[name]
	if !ok {
		fmt.Fprintf(env.Stderr, "mygit: %q is not a mygit command\n\n", name)
		printUsage(env.Stderr)
		return 1
	}

	err := cmd.Run(env, args[1:])
	switch {
	case err == nil:
		return 0
	case errors.Is(err, flag.ErrHelp):
		fmt.Fprintf(env.Stdout, "usage: %s\n", cmd.Usage)
		return 0
	case errors.Is(err, errUsage):
		fmt.Fprintf(env.Stderr, "usage: %s\n", cmd.Usage)
		return 129 // Git's exit code for a usage error
	default:
		fmt.Fprintf(env.Stderr, "mygit %s: %v\n", name, err)
		return 1
	}
}

func printUsage(w io.Writer) {
	fmt.Fprint(w, "usage: mygit <command> [<args>]\n\navailable commands:\n")

	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	sort.Strings(names)

	tw := tabwriter.NewWriter(w, 0, 0, 3, ' ', 0)
	for _, name := range names {
		fmt.Fprintf(tw, "   %s\t%s\n", name, registry[name].Summary)
	}
	tw.Flush()
}

// newFlagSet builds a FlagSet that reports errors through the dispatcher
// instead of writing to stderr and terminating the process itself.
func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}
	return fs
}
