package main

import (
	"strings"
)

// breadcrumbPath joins segments with " / ", collapsing middle segments
// with an ellipsis when the joined string exceeds maxLen runes.
// Preserves as many trailing segments as fit after the first, so the
// user keeps orientation near where they are in the tree. When nothing
// collapses well enough, the full joined path is simply truncated.
func breadcrumbPath(parts []string, maxLen int) string {
	if len(parts) == 0 {
		return ""
	}
	full := strings.Join(parts, " / ")
	if len([]rune(full)) <= maxLen || len(parts) <= 2 {
		return truncate(full, maxLen)
	}
	first := parts[0]
	// Iterate n=1..len-1: smaller n means more trailing segments kept
	// after "...". The first candidate that fits is the one with the
	// most context preserved.
	for n := 1; n < len(parts); n++ {
		candidate := first + " / ... / " + strings.Join(parts[n:], " / ")
		if len([]rune(candidate)) <= maxLen {
			return candidate
		}
	}
	return truncate(full, maxLen)
}
