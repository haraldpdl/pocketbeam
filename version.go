package main

import (
	"fmt"
	"os"
)

// version is overridable at build time via `-ldflags "-X main.version=..."`
// so release binaries embed their release tag.
var version = "0.0.1-dev"

func die(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(2)
}
