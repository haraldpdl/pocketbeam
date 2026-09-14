package main

import (
	"fmt"
	"time"
)

// truncate shortens s to at most n runes, adding "..." when cut.
func truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 3 {
		return string(r[:n])
	}
	return string(r[:n-3]) + "..."
}

// humanAgo renders a past time as a short phrase like "2 minutes ago",
// "3 hours ago", "yesterday", "4 days ago". Resolution degrades with age;
// this is for the main screen, not logs.
func humanAgo(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := time.Since(t)
	switch {
	case d < 45*time.Second:
		return "just now"
	case d < 90*time.Second:
		return "1 minute ago"
	case d < time.Hour:
		return fmt.Sprintf("%d minutes ago", int(d.Minutes()))
	case d < 90*time.Minute:
		return "1 hour ago"
	case d < 24*time.Hour:
		return fmt.Sprintf("%d hours ago", int(d.Hours()))
	case d < 48*time.Hour:
		return "yesterday"
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%d days ago", int(d.Hours()/24))
	default:
		return t.Format("2006-01-02")
	}
}

// formatBytes renders b as a short human-readable size. Binary units
// (KiB/MiB/GiB) are skipped in favour of base-10 because free-space
// estimates are already approximate and base-10 matches how every
// PocketBook file dialog phrases sizes.
func formatBytes(b int64) string {
	if b < 0 {
		b = 0
	}
	const (
		kb = 1000
		mb = 1000 * kb
		gb = 1000 * mb
	)
	switch {
	case b >= gb:
		return fmt.Sprintf("%.1f GB", float64(b)/float64(gb))
	case b >= mb:
		return fmt.Sprintf("%.0f MB", float64(b)/float64(mb))
	case b >= kb:
		return fmt.Sprintf("%.0f KB", float64(b)/float64(kb))
	default:
		return fmt.Sprintf("%d B", b)
	}
}
