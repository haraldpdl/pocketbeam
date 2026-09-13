// WebDAV directory picker: drill through the share to pick a sync folder.

package main

import (
	"context"
	"fmt"
	"image"
	"strings"
	"sync"

	ink "github.com/dennwc/inkview"
)

// dirPickerState holds the current WebDAV directory the user is drilling
// through. path is the currently-browsed directory (always absolute, always
// starts with "/"); dirs is the list of subdirectories to display.
type dirPickerState struct {
	mu           sync.Mutex
	loading      bool
	path         string
	dirs         []string
	err          error
	offset       int
	pageSize     int
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
	a.dirPicker.mu.Unlock()
	a.screen = screenDirPicker
	ink.Repaint()
	go a.fetchDirEntries(p)
}

// fetchDirEntries lists subdirectories of path on the configured WebDAV
// server and stores the result for the picker to render.
func (a *app) fetchDirEntries(p string) {
	ctx, cancel := context.WithTimeout(context.Background(), feedPickerTimeout)
	defer cancel()
	if err := a.ensureConnected(ctx); err != nil {
		a.dirPicker.mu.Lock()
		a.dirPicker.loading = false
		a.dirPicker.err = err
		a.dirPicker.mu.Unlock()
		ink.Repaint()
		return
	}
	src := NewWebDAVSource(a.cfg.Host, a.cfg.User, a.cfg.Pass, p)
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
	a.dirPicker.loading = false
	a.dirPicker.dirs = dirs
	a.dirPicker.err = err
	a.dirPicker.mu.Unlock()
	ink.Repaint()
}

func (a *app) drawDirPicker() {
	title := ink.OpenFont(ink.DefaultFontBold, a.layout.fpx(64), true)
	defer title.Close()
	title.SetActive(ink.Black)

	body := ink.OpenFont(ink.DefaultFont, a.layout.fpx(32), true)
	defer body.Close()

	rowTitleFont := ink.OpenFont(ink.DefaultFontBold, a.layout.fpx(36), true)
	defer rowTitleFont.Close()
	rowSubFont := ink.OpenFont(ink.DefaultFont, a.layout.fpx(28), true)
	defer rowSubFont.Close()
	smallFont := ink.OpenFont(ink.DefaultFont, a.layout.fpx(26), true)
	defer smallFont.Close()

	btnFont := ink.OpenFont(ink.DefaultFontBold, a.layout.fpx(44), true)
	defer btnFont.Close()
	btnFont.SetActive(ink.Black)

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

	var prevPageRect, nextPageRect image.Rectangle

	if loading {
		body.SetActive(ink.Black)
		ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(300)}, "Loading...")
		ink.ShowHourglassAt(image.Point{X: a.layout.margin, Y: a.layout.sy(360)})
	} else if pickErr != nil {
		body.SetActive(ink.Black)
		ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(300)}, "Could not list folder:")
		ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(350)}, truncate(pickErr.Error(), 60))
	} else {
		rowH := a.layout.sy(110)
		pageBtnH := a.layout.sy(60)
		areaTop := a.layout.pickerAreaTop
		// Reserve space at the bottom for the "Sync this folder" button.
		selectBtnH := a.layout.sy(100)
		areaBottom := a.layout.pickerAreaBottom - selectBtnH - a.layout.sy(40)

		atRoot := path == "/" || path == ""

		// Up-row: full-width list-row styled, "<" on the left.
		listTop := areaTop
		var upRect image.Rectangle
		if !atRoot {
			upRect = image.Rect(a.layout.margin, listTop, a.layout.screen.X-a.layout.margin, listTop+rowH-a.layout.sy(20))
			a.drawHairline(upRect.Min.X, upRect.Max.X, upRect.Min.Y)
			rowTitleFont.SetActive(ink.Black)
			titleY := upRect.Min.Y + (upRect.Dy()-a.layout.fpx(36))/2
			parent := dirParent(path)
			ink.DrawString(image.Point{X: upRect.Min.X + a.layout.sx(40), Y: titleY},
				"< Back to "+truncate(parent, 32))
			listTop += rowH
		}

		visibleArea := areaBottom - listTop
		pageSize := (visibleArea - pageBtnH - a.layout.sy(20)) / rowH
		if pageSize < 1 {
			pageSize = 1
		}
		p := paginate(len(dirs), pageSize, offset)
		offset = p.offset
		end := p.end

		rects := make([]image.Rectangle, 0, end-offset)
		for i := offset; i < end; i++ {
			y1 := listTop + (i-offset)*rowH
			rect := image.Rect(a.layout.margin, y1, a.layout.screen.X-a.layout.margin, y1+rowH)
			rects = append(rects, rect)
			a.drawListRow(rowTitleFont, rowSubFont, rect, dirs[i], "", true)
		}
		if n := len(rects); n > 0 {
			last := rects[n-1]
			a.drawHairline(last.Min.X, last.Max.X, last.Max.Y)
		}
		if len(dirs) > pageSize {
			btnY1 := listTop + pageSize*rowH + a.layout.sy(20)
			btnY2 := btnY1 + pageBtnH
			contentW := a.layout.screen.X - 2*a.layout.margin
			navBtnW := contentW / 4
			if offset > 0 {
				prevPageRect = image.Rect(a.layout.margin, btnY1, a.layout.margin+navBtnW, btnY2)
				ink.DrawRect(prevPageRect, ink.Black)
				drawCenteredText(btnFont, prevPageRect, "< Prev", a.layout.fpx(44))
			}
			if end < len(dirs) {
				nextPageRect = image.Rect(a.layout.screen.X-a.layout.margin-navBtnW, btnY1, a.layout.screen.X-a.layout.margin, btnY2)
				ink.DrawRect(nextPageRect, ink.Black)
				drawCenteredText(btnFont, nextPageRect, "Next >", a.layout.fpx(44))
			}
			page := offset/pageSize + 1
			total := (len(dirs) + pageSize - 1) / pageSize
			pageLabel := fmt.Sprintf("Page %d of %d  ·  %d items", page, total, len(dirs))
			smallFont.SetActive(ink.DarkGray)
			pageLabelW := ink.StringWidth(pageLabel)
			ink.DrawString(
				image.Point{X: (a.layout.screen.X - pageLabelW) / 2, Y: btnY1 + (btnY2-btnY1-a.layout.fpx(26))/2},
				pageLabel,
			)
		}

		a.dirPicker.mu.Lock()
		a.dirPicker.offset = offset
		a.dirPicker.pageSize = pageSize
		a.dirPicker.rowRects = rects
		a.dirPicker.upRect = upRect
		a.dirPicker.prevPageRect = prevPageRect
		a.dirPicker.nextPageRect = nextPageRect
		a.dirPicker.mu.Unlock()

		// Select button sits just above the Back button.
		selectY2 := a.layout.backButton.Min.Y - a.layout.sy(40)
		selectY1 := selectY2 - selectBtnH
		selectRect := image.Rect(a.layout.margin, selectY1, a.layout.screen.X-a.layout.margin, selectY2)
		ink.DrawRect(selectRect, ink.Black)
		ink.DrawRect(selectRect.Inset(2), ink.Black)
		drawCenteredText(btnFont, selectRect, "Sync this folder", a.layout.fpx(44))
		a.dirPicker.mu.Lock()
		a.dirPicker.selectRect = selectRect
		a.dirPicker.mu.Unlock()
	}

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
		a.screen = screenSettings
		ink.Repaint()
		return true
	}
	return false
}

func (a *app) dirPickerPointer(e ink.PointerEvent) bool {
	if e.State != ink.PointerDown {
		return false
	}
	if e.Point.In(a.layout.backButton) {
		a.screen = screenSettings
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
	if e.Point.In(selectRect) {
		a.setPath(path)
		a.screen = screenSettings
		ink.HideHourglass()
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
	step := a.dirPicker.pageSize
	if step <= 0 {
		step = 1
	}
	a.dirPicker.offset += direction * step
	if a.dirPicker.offset < 0 {
		a.dirPicker.offset = 0
	}
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
	a.cfg.Path = normaliseRoot(p)
	_ = SaveConfig(a.cfgPath, a.cfg)
}
