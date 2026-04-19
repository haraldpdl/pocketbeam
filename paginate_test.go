package main

import "testing"

func TestPaginate(t *testing.T) {
	cases := []struct {
		name    string
		total   int
		max     int
		req     int
		wantOff int
		wantEnd int
	}{
		{"empty", 0, 10, 0, 0, 0},
		{"empty_overshoot", 0, 10, 99, 0, 0},
		{"fits_one_page", 8, 10, 0, 0, 8},
		{"exact_one_page", 10, 10, 0, 0, 10},
		{"two_pages_first", 15, 10, 0, 0, 10},
		{"two_pages_second", 15, 10, 10, 10, 15},
		{"overshoot_clamped_to_last_page_start", 15, 10, 99, 10, 15},
		{"three_pages_middle", 25, 10, 10, 10, 20},
		{"three_pages_last", 25, 10, 20, 20, 25},
		{"negative_clamped_to_zero", 15, 10, -3, 0, 10},
		{"single_row_page_size", 5, 1, 3, 3, 4},
		{"zero_max_treated_as_one", 5, 0, 2, 2, 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := paginate(tc.total, tc.max, tc.req)
			if got.offset != tc.wantOff || got.end != tc.wantEnd {
				t.Errorf("paginate(total=%d, max=%d, req=%d) = (off=%d, end=%d), want (off=%d, end=%d)",
					tc.total, tc.max, tc.req, got.offset, got.end, tc.wantOff, tc.wantEnd)
			}
			if got.start != got.offset {
				t.Errorf("start != offset for result %+v", got)
			}
		})
	}
}
