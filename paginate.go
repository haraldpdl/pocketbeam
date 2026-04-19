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
// handler share one source of truth, which is the fix for the
// "draw clamps locally but pointer reads stale offset" bug that
// manifested as taps landing on the wrong row after pressing Next at
// the end of a list.
//
//	paginate(0, 10, 0)   → offset=0  start=0  end=0     // empty list
//	paginate(8, 10, 0)   → offset=0  start=0  end=8     // fits in one page
//	paginate(15, 10, 0)  → offset=0  start=0  end=10    // first page of two
//	paginate(15, 10, 10) → offset=5  start=5  end=15    // second page, offset clamped
//	paginate(15, 10, 99) → offset=5  start=5  end=15    // overshoot clamped to last page
//	paginate(15, 10, -3) → offset=0  start=0  end=10    // negative clamped to start
func paginate(total, maxRows, requestedOffset int) page {
	if maxRows < 1 {
		maxRows = 1
	}
	off := requestedOffset
	if off > total-maxRows {
		off = total - maxRows
	}
	if off < 0 {
		off = 0
	}
	end := off + maxRows
	if end > total {
		end = total
	}
	return page{offset: off, start: off, end: end}
}
