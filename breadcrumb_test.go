package main

import "testing"

func TestBreadcrumbPath(t *testing.T) {
	cases := []struct {
		name   string
		parts  []string
		maxLen int
		want   string
	}{
		{
			"single_root",
			[]string{"Calibre-Web Automated"},
			60,
			"Calibre-Web Automated",
		},
		{
			"two_levels_fit",
			[]string{"Calibre-Web", "Shelves"},
			60,
			"Calibre-Web / Shelves",
		},
		{
			"three_levels_fit",
			[]string{"Calibre-Web", "Shelves", "to-pocketbook"},
			60,
			"Calibre-Web / Shelves / to-pocketbook",
		},
		{
			"deep_path_collapses_middle",
			[]string{"Calibre-Web", "Authors", "By letter", "P", "Pratchett", "Discworld"},
			50,
			"Calibre-Web / ... / P / Pratchett / Discworld",
		},
		{
			"very_long_final_segment_truncated",
			[]string{"A", "B", "Something ridiculously long that won't fit no matter what"},
			30,
			"A / B / Something ridiculou...",
		},
		{
			"empty_returns_empty",
			nil,
			10,
			"",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := breadcrumbPath(tc.parts, tc.maxLen)
			if got != tc.want {
				t.Errorf("breadcrumbPath(%v, %d) = %q, want %q", tc.parts, tc.maxLen, got, tc.want)
			}
		})
	}
}
