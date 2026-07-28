package cli

import (
	"fmt"
	"strings"

	"mygit/internal/graph"
	"mygit/internal/repository"
)

var logCmd = &Command{
	Name:    "log",
	Summary: "show commit history, newest first",
	Usage:   "mygit log [--oneline] [-n <count>] [<revision>]",
	Run:     runLog,
}

// gitDateFormat is Git's default log date layout, expressed with Go's reference
// time: "Tue Nov 14 20:23:20 2023 +0530".
const gitDateFormat = "Mon Jan 2 15:04:05 2006 -0700"

// runLog walks history backwards from a starting commit and prints it.
//
// The command is thin because all the difficulty lives in the traversal. What
// remains here is presentation, plus one decision worth naming: output is
// streamed as the walk produces it rather than collected and printed at the
// end. On a large repository with -n 10, that means ten commits are loaded, not
// a million, because the walker expands parents lazily and this loop stops
// asking.
func runLog(env *Env, args []string) error {
	fs := newFlagSet("log")
	oneline := fs.Bool("oneline", false, "print one abbreviated line per commit")
	maxCount := fs.Int("n", 0, "limit the number of commits shown")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 1 {
		return fmt.Errorf("%w: log takes at most one revision", errUsage)
	}
	if *maxCount < 0 {
		return fmt.Errorf("%w: -n must not be negative", errUsage)
	}

	repo, err := repository.Discover(env.Dir)
	if err != nil {
		return err
	}

	rev := "HEAD"
	if fs.NArg() == 1 {
		rev = fs.Arg(0)
	}
	start, err := resolveRevision(repo, rev)
	if err != nil {
		return err
	}

	walker, err := graph.NewWalker(repo.Objects, start)
	if err != nil {
		return err
	}

	for shown := 0; *maxCount == 0 || shown < *maxCount; shown++ {
		commit, ok, err := walker.Next()
		if err != nil {
			return err
		}
		if !ok {
			break
		}
		if *oneline {
			printOneline(env, commit)
		} else {
			printFull(env, commit, shown > 0)
		}
	}
	return nil
}

// printOneline renders the compact form: abbreviated ID and subject.
//
// Seven hex characters is Git's historical default abbreviation. It is a
// display convenience only — 28 bits is nowhere near collision-safe for a large
// repository, which is why Git resolves abbreviations by scanning for a unique
// match and lengthens them as a project grows. mygit prints seven but, as noted
// in the OID parser, refuses to accept abbreviations as input.
func printOneline(env *Env, c graph.Commit) {
	fmt.Fprintf(env.Stdout, "%s %s\n", c.OID.String()[:7], c.Summary())
}

// printFull renders Git's default multi-line format.
func printFull(env *Env, c graph.Commit, separate bool) {
	if separate {
		fmt.Fprintln(env.Stdout)
	}

	fmt.Fprintf(env.Stdout, "commit %s\n", c.OID)

	// A merge commit lists its parents, which is the only place log output
	// reveals that history is a DAG rather than a line.
	if c.IsMerge() {
		abbrev := make([]string, 0, len(c.Parents))
		for _, p := range c.Parents {
			abbrev = append(abbrev, p.String()[:7])
		}
		fmt.Fprintf(env.Stdout, "Merge: %s\n", strings.Join(abbrev, " "))
	}

	fmt.Fprintf(env.Stdout, "Author: %s <%s>\n", c.Author.Name, c.Author.Email)
	fmt.Fprintf(env.Stdout, "Date:   %s\n\n", c.Author.When.Format(gitDateFormat))

	// The message is indented by four spaces, and blank lines stay blank rather
	// than becoming lines of trailing whitespace.
	for _, line := range strings.Split(strings.TrimRight(c.Message, "\n"), "\n") {
		if line == "" {
			fmt.Fprintln(env.Stdout)
			continue
		}
		fmt.Fprintf(env.Stdout, "    %s\n", line)
	}
}
