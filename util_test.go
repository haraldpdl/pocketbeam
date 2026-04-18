package main

import (
	"testing"
	"time"
)

func TestHumanAgo(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name string
		at   time.Time
		want string
	}{
		{"zero", time.Time{}, "never"},
		{"just now", now.Add(-10 * time.Second), "just now"},
		{"one minute", now.Add(-80 * time.Second), "1 minute ago"},
		{"ten minutes", now.Add(-10 * time.Minute), "10 minutes ago"},
		{"one hour", now.Add(-75 * time.Minute), "1 hour ago"},
		{"four hours", now.Add(-4 * time.Hour), "4 hours ago"},
		{"yesterday", now.Add(-30 * time.Hour), "yesterday"},
		{"three days", now.Add(-3 * 24 * time.Hour), "3 days ago"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := humanAgo(tc.at); got != tc.want {
				t.Errorf("humanAgo = %q, want %q", got, tc.want)
			}
		})
	}
}
