package main

// page captures the result of paginating a flat list for a single
// screen: the clamped offset (safe to store back, safe to index with),
// plus the half-open slice range [start, end) to render. maxRows is the
// number of rows the draw area can fit.
type page struct {
	offset int
	start  int
	end    int
}

// paginate clamps requestedOffset against the length of a list and
// returns the range of rows to render. Keeping this in a pure function
// (no UI state, no locking) lets both the draw path and the pointer
// handler share one source of truth.
//
// Last-page semantics: the final page shows fewer rows when total is
// not a multiple of pageSize, rather than backing the offset up to
// keep a full window. That way every page's first row is at a stable
// offset (0, pageSize, 2*pageSize, ...) and the "Page N of M" label
// always matches what the user sees.
//
//	paginate(0, 10, 0)    → offset=0   end=0    // empty list
//	paginate(8, 10, 0)    → offset=0   end=8    // fits in one page
//	paginate(15, 10, 0)   → offset=0   end=10   // page 1 of 2
//	paginate(15, 10, 10)  → offset=10  end=15   // page 2 of 2, 5 rows
//	paginate(15, 10, 99)  → offset=10  end=15   // overshoot clamped to last page start
//	paginate(15, 10, -3)  → offset=0   end=10   // negative clamped to start
func paginate(total, pageSize, requestedOffset int) page {
	if pageSize < 1 {
		pageSize = 1
	}
	off := requestedOffset
	if off < 0 {
		off = 0
	}
	if total > 0 {
		lastPageStart := ((total - 1) / pageSize) * pageSize
		if off > lastPageStart {
			off = lastPageStart
		}
	} else {
		off = 0
	}
	end := off + pageSize
	if end > total {
		end = total
	}
	return page{offset: off, start: off, end: end}
}

// listPage is the geometry of one page of a fixed-row-height list drawn
// into a vertical band: which slice of the list is visible, how many
// rows a full page holds, and where the page-navigation buttons go.
type listPage struct {
	offset   int  // clamped index of the first visible row
	end      int  // one past the last visible row
	pageSize int  // rows a full page holds; also the Prev/Next step
	navY     int  // top edge of the page-navigation buttons
	navShown bool // true when the list does not fit on one page
}

// layoutListPage fits as many rows of rowH as fit between top and
// bottom, keeping navH plus gap free at the bottom for the page
// buttons, and clamps offset onto a real page start.
//
// The nav row sits at a fixed y (a full page below top) so it does not
// jump upwards on a short last page, and never below bottom-navH so it
// cannot land on whatever the caller reserved the space under the band
// for (the Sync / Add button, which is hit-tested first).
func layoutListPage(top, bottom, rowH, navH, gap, total, offset int) listPage {
	if rowH < 1 {
		rowH = 1
	}
	pageSize := (bottom - top - navH - gap) / rowH
	if pageSize < 1 {
		pageSize = 1
	}
	p := paginate(total, pageSize, offset)
	navY := top + pageSize*rowH + gap
	// A band too short for one row plus the nav floors pageSize up to 1,
	// which puts the nav row past bottom.
	if navY+navH > bottom {
		navY = bottom - navH
	}
	return listPage{
		offset:   p.offset,
		end:      p.end,
		pageSize: pageSize,
		navY:     navY,
		navShown: total > pageSize,
	}
}

// pageStep moves a stored offset one page in direction (+1 next, -1
// previous). Only the lower bound is enforced here; the upper bound is
// paginate's job on the next draw, which is where the current list
// length is known.
func pageStep(offset, pageSize, direction int) int {
	if pageSize < 1 {
		pageSize = 1
	}
	off := offset + direction*pageSize
	if off < 0 {
		off = 0
	}
	return off
}
