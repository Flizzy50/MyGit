package cli

import (
	"fmt"
	"os"
	"time"

	"mygit/internal/object"
)

// signature builds the author or committer signature for a new commit.
//
// role is "AUTHOR" or "COMMITTER". Both are resolved the same way, from
// environment variables, checking mygit's own names first and falling back to
// Git's so an existing shell configuration works unchanged.
//
// Real Git resolves identity through a layered configuration system — system,
// global, then repository .git/config — with environment variables as the
// highest-priority override. mygit implements only the override layer. That is
// a genuine simplification, but it preserves the part that matters here: commit
// identity is an input to the commit, not a property of the machine.
func signature(role string) (object.Signature, error) {
	name := firstEnv("MYGIT_"+role+"_NAME", "GIT_"+role+"_NAME")
	email := firstEnv("MYGIT_"+role+"_EMAIL", "GIT_"+role+"_EMAIL")

	if name == "" || email == "" {
		return object.Signature{}, fmt.Errorf(
			"cannot determine identity: set %s_NAME and %s_EMAIL "+
				"(for example MYGIT_%s_NAME=\"Your Name\")", role, role, role)
	}

	when, err := commitTime(role)
	if err != nil {
		return object.Signature{}, err
	}
	return object.Signature{Name: name, Email: email, When: when}, nil
}

// commitTime returns the timestamp for a signature, honoring a date override.
//
// The override exists for the same reason Git's does: a commit ID is a hash of
// every one of its fields, timestamp included, so a commit is only reproducible
// if the clock is an injectable input rather than an ambient one. This is what
// makes byte-exact comparison against real Git possible in the tests, and it is
// the general lesson — hashing anything that reads a clock means the clock is
// part of your interface.
func commitTime(role string) (time.Time, error) {
	raw := firstEnv("MYGIT_"+role+"_DATE", "GIT_"+role+"_DATE")
	if raw == "" {
		return time.Now(), nil
	}

	// Accept Git's rawest form, "<unix seconds> <±hhmm>", which is exactly what
	// a signature stores. Parsing it through the same code that reads commits
	// keeps one definition of the format.
	sig, err := object.ParseSignature("x <x> " + raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid %s_DATE %q: want \"<unix seconds> <±hhmm>\"", role, raw)
	}
	return sig.When, nil
}

func firstEnv(names ...string) string {
	for _, n := range names {
		if v := os.Getenv(n); v != "" {
			return v
		}
	}
	return ""
}
