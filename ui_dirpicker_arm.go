// WebDAV directory picker: drill through the share to pick a sync folder.

package main

import (
	"context"
	"sync"

	ink "github.com/dennwc/inkview"
)

// dirPickerState holds the current WebDAV directory the user is drilling
// through. path is the currently-browsed directory (always absolute, always
// starts with "/"); dirs is the list of subdirectories to display. offset
// is the first-row index of the page on screen; where that page's rows
// land is geometry, derived from the layout when the screen is drawn and
// again when a tap is hit-tested.
type dirPickerState struct {
	mu      sync.Mutex
	loading bool
	path    string
	dirs    []string
	err     error
	offset  int
	// reqID identifies the listing currently being awaited. The up-row
	// is tappable while a listing is in flight, so a slow response can
	// land after the user has already moved to another folder; a fetch
	// whose id no longer matches drops its result instead of showing
	// one folder's subdirectories under another folder's path.
	reqID int
	// pickerFetch cancels the superseded listing so a stale request stops
	// working instead of merely having its result dropped.
	pickerFetch
}

// openDirPicker transitions to the directory picker, seeding the current
// path with startPath. If startPath is empty or invalid it falls back to
// the server root. The subdirectory listing is fetched in a goroutine.
func (a *app) openDirPicker(startPath string) {
	p := startPath
	if p == "" {
		p = "/"
	}
	a.dirPicker.mu.Lock()
	a.dirPicker.loading = true
	a.dirPicker.path = p
	a.dirPicker.dirs = nil
	a.dirPicker.err = nil
	a.dirPicker.offset = 0
	a.dirPicker.reqID++
	req := a.dirPicker.reqID
	ctx, cancel := a.dirPicker.restart()
	a.dirPicker.mu.Unlock()
	a.SetScreen(screenDirPicker)
	ink.Repaint()
	go a.fetchDirEntries(ctx, cancel, p, req)
}

// fetchDirEntries lists subdirectories of path on the configured WebDAV
// server and stores the result for the picker to render, unless the user
// has moved on and req is no longer the listing the picker waits for.
// ctx comes from dirPickerState.restart, so the next navigation cancels
// this listing; cancel is owned here and released when the fetch returns.
func (a *app) fetchDirEntries(ctx context.Context, cancel context.CancelFunc, p string, req int) {
	defer cancel()
	if err := a.ensureConnected(ctx); err != nil {
		a.dirPicker.mu.Lock()
		if a.dirPicker.reqID != req {
			a.dirPicker.mu.Unlock()
			return
		}
		a.dirPicker.loading = false
		a.dirPicker.err = err
		a.dirPicker.mu.Unlock()
		ink.Repaint()
		return
	}
	cfg := a.Config()
	src := NewWebDAVSource(cfg.Host, cfg.User, cfg.Pass, p)
	entries, err := src.ReadDir(ctx, src.Root)
	var dirs []string
	if err != nil {
		err = classifyWebDAVError(err)
	} else {
		for _, e := range entries {
			if e.IsDir() {
				dirs = append(dirs, e.Name())
			}
		}
	}
	a.dirPicker.mu.Lock()
	if a.dirPicker.reqID != req {
		a.dirPicker.mu.Unlock()
		return
	}
	a.dirPicker.loading = false
	a.dirPicker.dirs = dirs
	a.dirPicker.err = err
	a.dirPicker.mu.Unlock()
	ink.Repaint()
}

// dirPickerSnapshot reads the picker state as one consistent set of
// values under the lock, so neither the draw nor a tap works from a
// half-updated listing.
func (a *app) dirPickerSnapshot() dirPickerSnapshot {
	a.dirPicker.mu.Lock()
	defer a.dirPicker.mu.Unlock()
	return dirPickerSnapshot{
		path:    a.dirPicker.path,
		dirs:    append([]string(nil), a.dirPicker.dirs...),
		loading: a.dirPicker.loading,
		err:     a.dirPicker.err,
		offset:  a.dirPicker.offset,
	}
}

// dirPickerView is the snapshot resolved into the screen the shared
// picker draw paints.
func (a *app) dirPickerView() pickerView {
	return dirPickerViewOf(a.dirPickerSnapshot())
}

func (a *app) dirPickerKey(e ink.KeyEvent) bool {
	if e.Key == ink.KeyOk {
		a.dirPicker.mu.Lock()
		path := a.dirPicker.path
		a.dirPicker.mu.Unlock()
		a.setPath(path)
		a.SetScreen(screenSettings)
		ink.Repaint()
		return true
	}
	return false
}

func (a *app) dirPickerPointer(e ink.PointerEvent) bool {
	if e.Point.In(a.layout.backButton) {
		a.SetScreen(screenSettings)
		ink.Repaint()
		return true
	}
	// The tap is hit-tested against the geometry of the state as it is
	// now, which is the screen the user tapped unless a listing landed
	// in between - and then the rows they see are the new folder's too.
	snap := a.dirPickerSnapshot()
	g := a.layout.pickerGeometry(dirPickerViewOf(snap))

	if !g.up.Empty() && e.Point.In(g.up) {
		a.openDirPicker(parentDir(snap.path))
		return true
	}
	if !g.primary.Empty() && e.Point.In(g.primary) {
		a.setPath(snap.path)
		a.SetScreen(screenSettings)
		ink.Repaint()
		return true
	}
	if !g.list.prev.Empty() && e.Point.In(g.list.prev) {
		a.dirPickerPage(g.list, -1)
		return true
	}
	if !g.list.next.Empty() && e.Point.In(g.list.next) {
		a.dirPickerPage(g.list, +1)
		return true
	}

	// The view's rows are the subdirectories in order, so row i on
	// screen is dirs[offset+i].
	for i, r := range g.list.rows {
		if e.Point.In(r) {
			a.openDirPicker(normaliseRoot(snap.path) + "/" + snap.dirs[g.list.offset+i])
			return true
		}
	}
	return false
}

// dirPickerPage scrolls the directory list by one page and repaints,
// stepping from the clamped offset of the page the tap was hit-tested
// against so an overshoot cannot accumulate.
func (a *app) dirPickerPage(list pagedListRects, direction int) {
	a.dirPicker.mu.Lock()
	a.dirPicker.offset = pageStep(list.offset, list.pageSize, direction)
	a.dirPicker.mu.Unlock()
	ink.Repaint()
}

// parentDir returns the parent of p using forward-slash semantics. Root
// ("/") is its own parent so callers stop drilling up at the top.
func parentDir(p string) string {
	p = normaliseRoot(p)
	if p == "/" {
		return "/"
	}
	i := len(p) - 1
	for i > 0 && p[i] != '/' {
		i--
	}
	if i == 0 {
		return "/"
	}
	return p[:i]
}

// setPath updates the in-memory config's WebDAV sync path and persists it.
func (a *app) setPath(p string) {
	a.saveConfigChange(func(c *Config) { c.Path = normaliseRoot(p) })
}
