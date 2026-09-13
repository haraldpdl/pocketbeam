// WebDAV directory picker: drill through the share to pick a sync folder.

package main

import (
	"context"
	"image"
	"strings"
	"sync"

	ink "github.com/dennwc/inkview"
)

// dirPickerState holds the current WebDAV directory the user is drilling
// through. path is the currently-browsed directory (always absolute, always
// starts with "/"); dirs is the list of subdirectories to display.
type dirPickerState struct {
	mu       sync.Mutex
	loading  bool
	path     string
	dirs     []string
	err      error
	offset   int
	pageSize int
	// reqID identifies the listing currently being awaited. The up-row
	// is tappable while a listing is in flight, so a slow response can
	// land after the user has already moved to another folder; a fetch
	// whose id no longer matches drops its result instead of showing
	// one folder's subdirectories under another folder's path.
	reqID int
	// pickerFetch cancels the superseded listing so a stale request stops
	// working instead of merely having its result dropped.
	pickerFetch
	rowRects     []image.Rectangle // paginated directory rows
	upRect       image.Rectangle   // fixed ".. (up)" row, empty when at root
	prevPageRect image.Rectangle
	nextPageRect image.Rectangle
	selectRect   image.Rectangle // "Sync this folder" button
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
	a.dirPicker.rowRects = nil
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

func (a *app) drawDirPicker() {
	title := a.font(ink.DefaultFontBold, 64)
	title.SetActive(ink.Black)

	body := a.font(ink.DefaultFont, 32)
	rowTitleFont := a.font(ink.DefaultFontBold, 36)
	rowSubFont := a.font(ink.DefaultFont, 28)
	smallFont := a.font(ink.DefaultFont, 26)
	btnFont := a.font(ink.DefaultFontBold, 44)

	a.dirPicker.mu.Lock()
	loading := a.dirPicker.loading
	path := a.dirPicker.path
	dirs := append([]string(nil), a.dirPicker.dirs...)
	pickErr := a.dirPicker.err
	offset := a.dirPicker.offset
	a.dirPicker.mu.Unlock()

	title.SetActive(ink.Black)
	ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(140)}, "Select folder")

	smallFont.SetActive(ink.DarkGray)
	ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(210)}, truncate(path, 60))
	a.drawHairline(a.layout.margin, a.layout.screen.X-a.layout.margin, a.layout.sy(240))

	// Reserve space at the bottom for the "Sync this folder" button.
	selectBtnH := a.layout.sy(100)
	areaBottom := a.layout.pickerAreaBottom - selectBtnH - a.layout.sy(40)

	var list pagedListRects
	var upRect, selectRect image.Rectangle

	// Up-row: full-width list-row styled, "<" on the left. Drawn on every
	// pass, including while loading and after a failed listing, because it
	// is the only way back to the parent folder: the on-screen Back button
	// and the hardware Back key both leave the picker for Settings and
	// throw the whole drill-down away. It also lets the user walk away
	// from a listing that is still in flight.
	listTop := a.layout.pickerAreaTop
	if path != "/" && path != "" {
		rowH := a.layout.rowH()
		upRect = image.Rect(a.layout.margin, listTop, a.layout.screen.X-a.layout.margin, listTop+rowH-a.layout.sy(20))
		a.drawHairline(upRect.Min.X, upRect.Max.X, upRect.Min.Y)
		rowTitleFont.SetActive(ink.Black)
		titleY := upRect.Min.Y + (upRect.Dy()-a.layout.fpx(36))/2
		ink.DrawString(image.Point{X: upRect.Min.X + a.layout.sx(40), Y: titleY},
			"< Back to "+truncate(dirParent(path), 32))
		listTop += rowH
	}

	// Status text shares the band with the list, so it starts below the
	// up-row rather than at a fixed y.
	msgY := listTop + a.layout.sy(20)
	if loading {
		body.SetActive(ink.Black)
		ink.DrawString(image.Point{X: a.layout.margin, Y: msgY}, "Loading...")
		a.showHourglassAt(image.Point{X: a.layout.margin, Y: msgY + a.layout.sy(60)})
	} else if pickErr != nil {
		body.SetActive(ink.Black)
		ink.DrawString(image.Point{X: a.layout.margin, Y: msgY}, "Could not list folder:")
		ink.DrawString(image.Point{X: a.layout.margin, Y: msgY + a.layout.sy(50)}, truncate(pickErr.Error(), 60))
	} else {
		rows := make([]listRow, 0, len(dirs))
		for _, d := range dirs {
			rows = append(rows, listRow{title: d})
		}
		list = a.drawPagedList(
			listFonts{rowTitle: rowTitleFont, rowSub: rowSubFont, button: btnFont, label: smallFont},
			listTop, areaBottom, rows, offset)

		// Select button sits just above the Back button.
		selectY2 := a.layout.backButton.Min.Y - a.layout.sy(40)
		selectRect = image.Rect(a.layout.margin, selectY2-selectBtnH, a.layout.screen.X-a.layout.margin, selectY2)
		ink.DrawRect(selectRect, ink.Black)
		ink.DrawRect(selectRect.Inset(2), ink.Black)
		drawCenteredText(btnFont, selectRect, "Sync this folder", a.layout.fpx(44))
	}

	// Written on every pass, so a load or an error clears the row and
	// page targets of the folder the user came from instead of leaving
	// invisible ones behind. upRect is the exception: it is drawn on
	// every pass, so it is always live.
	a.dirPicker.mu.Lock()
	a.dirPicker.offset = list.offset
	a.dirPicker.pageSize = list.pageSize
	a.dirPicker.rowRects = list.rows
	a.dirPicker.upRect = upRect
	a.dirPicker.prevPageRect = list.prev
	a.dirPicker.nextPageRect = list.next
	a.dirPicker.selectRect = selectRect
	a.dirPicker.mu.Unlock()

	ink.DrawRect(a.layout.backButton, ink.Black)
	drawCenteredText(btnFont, a.layout.backButton, "Back", a.layout.fpx(44))
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
	a.dirPicker.mu.Lock()
	rects := a.dirPicker.rowRects
	dirs := a.dirPicker.dirs
	path := a.dirPicker.path
	selectRect := a.dirPicker.selectRect
	offset := a.dirPicker.offset
	prev := a.dirPicker.prevPageRect
	next := a.dirPicker.nextPageRect
	upRect := a.dirPicker.upRect
	a.dirPicker.mu.Unlock()

	if !upRect.Empty() && e.Point.In(upRect) {
		a.openDirPicker(parentDir(path))
		return true
	}
	if !selectRect.Empty() && e.Point.In(selectRect) {
		a.setPath(path)
		a.SetScreen(screenSettings)
		ink.Repaint()
		return true
	}
	if !prev.Empty() && e.Point.In(prev) {
		a.dirPickerPage(-1)
		return true
	}
	if !next.Empty() && e.Point.In(next) {
		a.dirPickerPage(+1)
		return true
	}

	for i, r := range rects {
		if !e.Point.In(r) {
			continue
		}
		abs := offset + i
		if abs >= 0 && abs < len(dirs) {
			a.openDirPicker(normaliseRoot(path) + "/" + dirs[abs])
			return true
		}
	}
	return false
}

// dirPickerPage scrolls the directory list by one page and repaints.
func (a *app) dirPickerPage(direction int) {
	a.dirPicker.mu.Lock()
	a.dirPicker.offset = pageStep(a.dirPicker.offset, a.dirPicker.pageSize, direction)
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
