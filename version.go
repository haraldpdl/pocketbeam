package main

import (
	"fmt"
	"os"
)

// version is injected at build time from `git describe --tags --always --dirty`
// via `-ldflags "-X main.version=..."`. scripts/release.sh wires this up for
// release builds; a bare `go build` leaves the default "dev" in place, which
// makes the on-device updater always report "update available" so local
// test builds don't silently impersonate a release.
var version = "dev"

func die(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(2)
}
