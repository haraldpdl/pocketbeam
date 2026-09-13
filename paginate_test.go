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

func TestLayoutListPage(t *testing.T) {
	// Reference-device numbers: the picker band on a 1264x1680 panel.
	const (
		top    = 340
		bottom = 1400
		rowH   = 110
		navH   = 60
		gap    = 20
	)
	cases := []struct {
		name                   string
		top, bottom, rowH      int
		total, offset          int
		wantOff, wantEnd       int
		wantPageSize, wantNavY int
		wantNav                bool
	}{
		{"fits_one_page", top, bottom, rowH, 5, 0, 0, 5, 8, 1240, false},
		{"exactly_one_page", top, bottom, rowH, 8, 0, 0, 8, 8, 1240, false},
		{"first_of_three", top, bottom, rowH, 20, 0, 0, 8, 8, 1240, true},
		{"last_page_is_short", top, bottom, rowH, 20, 16, 16, 20, 8, 1240, true},
		{"offset_clamped_to_last_page", top, bottom, rowH, 20, 999, 16, 20, 8, 1240, true},
		{"negative_offset", top, bottom, rowH, 20, -5, 0, 8, 8, 1240, true},
		// An up-row above the list shrinks the band by exactly one row.
		{"band_shrunk_by_up_row", top + rowH, bottom, rowH, 20, 0, 0, 7, 7, 1240, true},
		// Bands too small for even one row still render a single row.
		{"band_smaller_than_a_row", top, top + 100, rowH, 3, 0, 0, 1, 1, 470, true},
		{"zero_row_height", top, bottom, 0, 3, 0, 0, 3, 980, 1340, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := layoutListPage(tc.top, tc.bottom, tc.rowH, navH, gap, tc.total, tc.offset)
			if got.offset != tc.wantOff || got.end != tc.wantEnd {
				t.Errorf("range = (off=%d, end=%d), want (off=%d, end=%d)",
					got.offset, got.end, tc.wantOff, tc.wantEnd)
			}
			if got.pageSize != tc.wantPageSize {
				t.Errorf("pageSize = %d, want %d", got.pageSize, tc.wantPageSize)
			}
			if got.navY != tc.wantNavY {
				t.Errorf("navY = %d, want %d", got.navY, tc.wantNavY)
			}
			if got.navShown != tc.wantNav {
				t.Errorf("navShown = %v, want %v", got.navShown, tc.wantNav)
			}
			if got.end-got.offset > got.pageSize {
				t.Errorf("page holds %d rows, more than pageSize %d", got.end-got.offset, got.pageSize)
			}
		})
	}
}

// The nav row must not move when the last page is short, otherwise the
// Prev/Next buttons jump up the screen on the final page.
func TestLayoutListPageNavYIsStable(t *testing.T) {
	first := layoutListPage(340, 1400, 110, 60, 20, 20, 0)
	last := layoutListPage(340, 1400, 110, 60, 20, 20, 16)
	if first.navY != last.navY {
		t.Errorf("navY moved between pages: %d then %d", first.navY, last.navY)
	}
}

func TestPageStep(t *testing.T) {
	cases := []struct {
		name                        string
		offset, pageSize, direction int
		want                        int
	}{
		{"next", 0, 8, +1, 8},
		{"next_again", 8, 8, +1, 16},
		{"prev", 8, 8, -1, 0},
		{"prev_at_start", 0, 8, -1, 0},
		{"prev_past_start", 3, 8, -1, 0},
		{"unset_page_size_steps_one", 5, 0, +1, 6},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := pageStep(tc.offset, tc.pageSize, tc.direction); got != tc.want {
				t.Errorf("pageStep(%d, %d, %d) = %d, want %d",
					tc.offset, tc.pageSize, tc.direction, got, tc.want)
			}
		})
	}
}

// Paging forward then back returns to the same page start, which is what
// the picker relies on when the user walks a long feed.
func TestPageStepRoundTrip(t *testing.T) {
	p := layoutListPage(340, 1400, 110, 60, 20, 20, 0)
	fwd := pageStep(p.offset, p.pageSize, +1)
	back := pageStep(fwd, p.pageSize, -1)
	if back != p.offset {
		t.Errorf("offset after next+prev = %d, want %d", back, p.offset)
	}
}
