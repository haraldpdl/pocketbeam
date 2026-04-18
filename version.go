package main

import (
	"fmt"
	"os"
)

const version = "0.0.1-dev"

func die(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(2)
}
