// What the WebDAV directory picker puts on the shared picker screen: the
// path being browsed, its subdirectories as rows, and the button that
// syncs the folder the user stopped on. The listings and the taps live
// in ui_dirpicker_arm.go.

package main

import (
	"strings"
)

// dirPickerPathLen is how many runes of the path fit on the header line,
// dirPickerUpLen how much of the parent's name fits on the up-row after
// its "< Back to " prefix.
const (
	dirPickerPathLen = 60
	dirPickerUpLen   = 32
)

// dirPickerSnapshot is one consistent reading of the picker's state,
// taken under its lock: the directory being browsed and what was found
// in it.
type dirPickerSnapshot struct {
	path    string // absolute, always starts with "/"
	dirs    []string
	loading bool
	err     error
	offset  int
}

// dirPickerViewOf resolves a snapshot into what the screen shows.
func dirPickerViewOf(s dirPickerSnapshot) pickerView {
	v := pickerView{
		title:   "Select folder",
		crumb:   truncate(s.path, dirPickerPathLen),
		loading: s.loading,
		errLead: "Could not list folder:",
		offset:  s.offset,
	}
	if s.path != "/" && s.path != "" {
		v.upLabel = "< Back to " + truncate(dirParent(s.path), dirPickerUpLen)
	}
	if s.err != nil {
		v.errMsg = s.err.Error()
	}
	if s.loading || s.err != nil {
		// A folder that did not list has nothing to sync, so the button
		// that would save it is left off rather than offered and then
		// saving the path the user never got to see.
		return v
	}
	for _, d := range s.dirs {
		v.rows = append(v.rows, listRow{title: d})
	}
	v.primary = "Sync this folder"
	return v
}

// dirParent returns a human-readable label for the parent of an absolute
// WebDAV path. The root is shown as "/".
func dirParent(p string) string {
	if p == "" || p == "/" {
		return "/"
	}
	p = strings.TrimSuffix(p, "/")
	i := strings.LastIndex(p, "/")
	if i < 0 {
		return "/"
	}
	if i == 0 {
		return "/"
	}
	return p[strings.LastIndex(p[:i], "/")+1 : i]
}
