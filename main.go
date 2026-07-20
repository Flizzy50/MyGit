// Command mygit is an educational reimplementation of Git's core: a
// content-addressable object database with version control layered on top.
//
// This file stays deliberately thin. Its only job is to translate between the
// operating system's process conventions — argv, standard streams, exit codes —
// and ordinary Go values. Everything testable lives under internal/.
package main

import (
	"os"

	"mygit/internal/cli"
)

func main() {
	os.Exit(cli.Main(cli.DefaultEnv(), os.Args[1:]))
}
