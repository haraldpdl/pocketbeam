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
