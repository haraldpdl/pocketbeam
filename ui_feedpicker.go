// What the OPDS feed picker puts on the shared picker screen: the
// breadcrumb of the level the user is at, its subsections as rows, and
// the buttons that either sync the level outright or accumulate a
// selection. The fetches and the taps live in ui_feedpicker_arm.go.

package main

import (
	"fmt"
)

// feedPickerCrumbLen is how many runes of the breadcrumb fit on the
// header line, and feedPickerUpLen how much of the parent's title fits
// on the up-row after its "< Back to " prefix.
const (
	feedPickerCrumbLen = 55
	feedPickerUpLen    = 32
)

// feedPickerSnapshot is one consistent reading of the picker's state,
// taken under its lock: the level being shown, where it sits in the
// tree, and what has been selected so far.
type feedPickerSnapshot struct {
	// titles are the ancestor titles with the current level's last, so
	// the whole slice is the breadcrumb and a single entry means the
	// root.
	titles   []string
	href     string // the current level's feed href
	loading  bool
	level    OPDSLevel
	err      error
	offset   int
	selected []FilterOption
}

// feedPickerViewOf resolves a snapshot into what the screen shows.
func feedPickerViewOf(s feedPickerSnapshot) pickerView {
	atRoot := len(s.titles) <= 1
	v := pickerView{
		title:   "Select filter",
		crumb:   breadcrumbPath(s.titles, feedPickerCrumbLen),
		loading: s.loading,
		errLead: "Could not load feed:",
		offset:  s.offset,
	}
	if !atRoot {
		v.upLabel = "< Back to " + truncate(s.titles[len(s.titles)-2], feedPickerUpLen)
	}
	if s.err != nil {
		v.errMsg = s.err.Error()
	}
	loaded := !s.loading && s.err == nil
	if loaded {
		for _, sub := range visibleSubsections(s.level) {
			row := listRow{title: sub.Name}
			if sub.CountKnown {
				row.subtitle = fmt.Sprintf("%d books", sub.Count)
			}
			v.rows = append(v.rows, row)
		}
	}

	if len(s.selected) == 0 {
		// Nothing picked yet: one button that saves the level the user
		// is on and closes. The book count is only known once the level
		// has loaded.
		v.primary = "Sync this level"
		if atRoot {
			v.primary = "Sync everything"
		} else if loaded {
			switch count, approx := levelBookCount(s.level); {
			case count > 0 && approx:
				v.primary = fmt.Sprintf("Sync this level (~%d books)", count)
			case count > 0:
				v.primary = fmt.Sprintf("Sync this level (%d books)", count)
			}
		}
		return v
	}
	// A selection is in progress: the top button adds or removes the
	// current level, the bottom one saves the set. At the root adding is
	// meaningless - the root is what "sync everything" already means -
	// so only the saving button is offered.
	if !atRoot {
		v.primary = "Add this level"
		if pickerContains(s.selected, s.href) {
			v.primary = "Remove this level"
		}
	}
	v.done = fmt.Sprintf("Done (%d selected)", len(s.selected))
	return v
}

// pickerContains reports whether sel already includes an option with the
// given href. Used to label the Add/Remove toggle.
func pickerContains(sel []FilterOption, href string) bool {
	for _, o := range sel {
		if o.Href == href {
			return true
		}
	}
	return false
}
