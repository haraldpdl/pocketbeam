// OPDS feed picker: nested navigation through the catalog with a
// multi-select filter set.

package main

import (
	"context"
	"errors"
	"fmt"
	"image"
	"sync"

	ink "github.com/dennwc/inkview"
)

// feedPickerFrame is one level of the nested-navigation stack. Pushed when
// the user drills into a subsection, popped on "..".
type feedPickerFrame struct {
	Href  string
	Title string
}

// feedPickerState holds the current OPDS feed the user is browsing. The
// navigation stack lets the user walk back up to any ancestor. offset is
// the first-row-index in the current page; prevPageRect / nextPageRect
// are the navigation buttons for long lists that don't fit on screen.
type feedPickerState struct {
	mu           sync.Mutex
	loading      bool
	href         string
	title        string
	stack        []feedPickerFrame
	level        OPDSLevel
	err          error
	offset       int
	pageSize     int               // rows per page; written during draw so paging stays consistent when the last page is short
	rowRects     []image.Rectangle // per visible subsection row
	selectRect   image.Rectangle   // primary action ("Done" or "Add this level" depending on state)
	doneRect     image.Rectangle   // "Done" button when the selection set is non-empty
	upRect       image.Rectangle   // ".. (up)" button, empty when at root
	prevPageRect image.Rectangle   // "< Prev page" button, empty when at first page
	nextPageRect image.Rectangle   // "Next page >" button, empty when at last page
	// Selected is the accumulating set of filters chosen during this
	// picker session; seeded from cfg.FilterHrefs on open, written back
	// to the config when the user taps Done.
	selected []FilterOption
}

// openShelfPicker transitions to the picker screen and fetches the root
// OPDS feed. The name is historical; the picker now walks the full feed
// tree, not just shelves.
func (a *app) openShelfPicker() {
	// Seed the selection from the current config so the user sees
	// previously-picked filters and can add to / remove from them.
	seeded := make([]FilterOption, 0, len(a.cfg.FilterHrefs))
	for i, href := range a.cfg.FilterHrefs {
		name := href
		if i < len(a.cfg.FilterNames) && a.cfg.FilterNames[i] != "" {
			name = a.cfg.FilterNames[i]
		}
		seeded = append(seeded, FilterOption{Name: name, Href: href})
	}
	// The root is addressed by the empty href: FetchLevel resolves it
	// under the server's path prefix, and the picker never stores the
	// root as a filter (root means "sync everything"), so no literal
	// path is needed here.
	a.picker.mu.Lock()
	a.picker.stack = nil
	a.picker.href = ""
	a.picker.title = "All books"
	a.picker.loading = true
	a.picker.level = OPDSLevel{}
	a.picker.err = nil
	a.picker.rowRects = nil
	a.picker.offset = 0
	a.picker.selected = seeded
	a.picker.mu.Unlock()
	a.screen = screenShelfPicker
	ink.Repaint()
	go a.fetchFeedLevel("", "All books")
}

// drillInto pushes the current feed onto the stack and fetches the child
// feed at href. Title is the navigation entry's display name, used in the
// breadcrumb and in the stored filter_name when the user taps "Sync this
// level".
func (a *app) drillInto(href, title string) {
	a.picker.mu.Lock()
	a.picker.stack = append(a.picker.stack, feedPickerFrame{Href: a.picker.href, Title: a.picker.title})
	a.picker.href = href
	a.picker.title = title
	a.picker.loading = true
	a.picker.level = OPDSLevel{}
	a.picker.err = nil
	a.picker.rowRects = nil
	a.picker.offset = 0
	a.picker.mu.Unlock()
	ink.Repaint()
	go a.fetchFeedLevel(href, title)
}

// shelfPickerPage scrolls the list by one page. Called from the Prev /
// Next page buttons; the offset is clamped against the list length on
// the next draw.
//
// The step is pageSize (written during draw) rather than len(rowRects),
// because the last page can be shorter than a full window: stepping by
// the partial count would land on a non-page-aligned offset.
func (a *app) shelfPickerPage(direction int) {
	a.picker.mu.Lock()
	a.picker.offset = pageStep(a.picker.offset, a.picker.pageSize, direction)
	a.picker.mu.Unlock()
	ink.Repaint()
}

// drillUp pops one frame off the navigation stack and re-fetches the
// parent feed. No-op at the root.
func (a *app) drillUp() {
	a.picker.mu.Lock()
	if len(a.picker.stack) == 0 {
		a.picker.mu.Unlock()
		return
	}
	top := a.picker.stack[len(a.picker.stack)-1]
	a.picker.stack = a.picker.stack[:len(a.picker.stack)-1]
	a.picker.href = top.Href
	a.picker.title = top.Title
	a.picker.loading = true
	a.picker.level = OPDSLevel{}
	a.picker.err = nil
	a.picker.rowRects = nil
	a.picker.offset = 0
	a.picker.mu.Unlock()
	ink.Repaint()
	go a.fetchFeedLevel(top.Href, top.Title)
}

func (a *app) fetchFeedLevel(href, title string) {
	ctx, cancel := context.WithTimeout(context.Background(), feedPickerTimeout)
	defer cancel()
	if err := a.ensureConnected(ctx); err != nil {
		a.picker.mu.Lock()
		a.picker.loading = false
		a.picker.err = err
		a.picker.mu.Unlock()
		ink.Repaint()
		return
	}
	lvl, err := a.client.FetchLevel(ctx, href)
	if errors.Is(err, context.DeadlineExceeded) {
		err = errors.New("Server did not respond in time.")
	}
	a.picker.mu.Lock()
	a.picker.loading = false
	a.picker.level = lvl
	a.picker.err = err
	if err == nil && lvl.FeedTitle != "" && len(a.picker.stack) == 0 {
		// Root feed: adopt the server's advertised title for the breadcrumb
		// so the user sees their server's label instead of the placeholder.
		a.picker.title = lvl.FeedTitle
	}
	a.picker.mu.Unlock()
	ink.Repaint()
}

func (a *app) drawShelfPicker() {
	title := ink.OpenFont(ink.DefaultFontBold, a.layout.fpx(64), true)
	defer title.Close()
	title.SetActive(ink.Black)
	ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(140)}, "Select filter")

	body := ink.OpenFont(ink.DefaultFont, a.layout.fpx(32), true)
	defer body.Close()
	body.SetActive(ink.Black)

	rowTitleFont := ink.OpenFont(ink.DefaultFontBold, a.layout.fpx(36), true)
	defer rowTitleFont.Close()
	rowSubFont := ink.OpenFont(ink.DefaultFont, a.layout.fpx(28), true)
	defer rowSubFont.Close()
	smallFont := ink.OpenFont(ink.DefaultFont, a.layout.fpx(26), true)
	defer smallFont.Close()

	btnFont := ink.OpenFont(ink.DefaultFontBold, a.layout.fpx(44), true)
	defer btnFont.Close()
	btnFont.SetActive(ink.Black)

	a.picker.mu.Lock()
	loading := a.picker.loading
	lvl := a.picker.level
	pickerErr := a.picker.err
	stackLen := len(a.picker.stack)
	curTitle := a.picker.title
	offset := a.picker.offset
	a.picker.mu.Unlock()

	// Breadcrumb path: every ancestor title joined by " / ", current
	// level last. Collapses middle segments when the full path is too
	// wide for the header.
	body.SetActive(ink.Black)
	a.picker.mu.Lock()
	stackTitles := make([]string, 0, stackLen)
	for _, f := range a.picker.stack {
		stackTitles = append(stackTitles, f.Title)
	}
	a.picker.mu.Unlock()
	crumb := breadcrumbPath(append(stackTitles, curTitle), 55)
	smallFont.SetActive(ink.DarkGray)
	ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(210)}, crumb)
	a.drawHairline(a.layout.margin, a.layout.screen.X-a.layout.margin, a.layout.sy(240))

	// Reserve space at bottom for "Sync this level" + Back.
	selectBtnH := a.layout.sy(100)
	areaTop := a.layout.pickerAreaTop
	areaBottom := a.layout.pickerAreaBottom - selectBtnH - a.layout.sy(40)

	var list pagedListRects
	var upRect image.Rectangle

	if loading {
		ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(300)}, "Loading...")
		a.showHourglassAt(image.Point{X: a.layout.margin, Y: a.layout.sy(360)})
	} else if pickerErr != nil {
		ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(300)}, "Could not load feed:")
		ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(350)}, truncate(pickerErr.Error(), 60))
	} else {
		subs := visibleSubsections(lvl)

		// Up-row: fixed above the paginated window so the user can go
		// up from any page. Rendered as a full-width list row with a
		// left-aligned "< Back" label so it shares the app-wide row
		// idiom; still visually distinct from the subsection chevron
		// rows because its chevron points the other way.
		listTop := areaTop
		if stackLen > 0 {
			rowH := a.layout.rowH()
			upRect = image.Rect(a.layout.margin, listTop, a.layout.screen.X-a.layout.margin, listTop+rowH-a.layout.sy(20))
			a.drawHairline(upRect.Min.X, upRect.Max.X, upRect.Min.Y)
			rowTitleFont.SetActive(ink.Black)
			titleY := upRect.Min.Y + (upRect.Dy()-a.layout.fpx(36))/2
			ink.DrawString(image.Point{X: upRect.Min.X + a.layout.sx(40), Y: titleY},
				"< Back to "+truncate(stackTitles[stackLen-1], 32))
			listTop += rowH
		}

		rows := make([]listRow, 0, len(subs))
		for _, sub := range subs {
			var subtitle string
			if sub.CountKnown {
				subtitle = fmt.Sprintf("%d books", sub.Count)
			}
			rows = append(rows, listRow{title: sub.Name, subtitle: subtitle})
		}
		list = a.drawPagedList(
			listFonts{rowTitle: rowTitleFont, rowSub: rowSubFont, button: btnFont, label: smallFont},
			listTop, areaBottom, rows, offset)
	}

	// Written on every pass, so a load or an error clears the tap
	// targets of the level the user came from instead of leaving
	// invisible ones behind.
	a.picker.mu.Lock()
	a.picker.offset = list.offset
	a.picker.pageSize = list.pageSize
	a.picker.rowRects = list.rows
	a.picker.prevPageRect = list.prev
	a.picker.nextPageRect = list.next
	a.picker.upRect = upRect
	a.picker.mu.Unlock()

	// Primary action button(s). With no selection the user sees a single
	// "Sync this level" (sync-all at root) that saves and closes. Once a
	// selection is in progress we show two stacked buttons: the top one
	// is "Add" / "Remove" for the current feed, and the bottom one is
	// "Done (N selected)" that persists and closes.
	a.picker.mu.Lock()
	selected := append([]FilterOption(nil), a.picker.selected...)
	curHref := a.picker.href
	a.picker.mu.Unlock()

	alreadyIn := pickerContains(selected, curHref)
	// Row and page-label drawing above leaves their own faces active.
	btnFont.SetActive(ink.Black)
	selectY2 := a.layout.backButton.Min.Y - a.layout.sy(40)
	selectY1 := selectY2 - selectBtnH
	selectRect := image.Rect(a.layout.margin, selectY1, a.layout.screen.X-a.layout.margin, selectY2)
	var doneRect image.Rectangle
	if len(selected) == 0 {
		ink.DrawRect(selectRect, ink.Black)
		ink.DrawRect(selectRect.Inset(2), ink.Black)
		label := "Sync this level"
		if stackLen == 0 {
			label = "Sync everything"
		} else if !loading && pickerErr == nil {
			count, approx := levelBookCount(lvl)
			switch {
			case count > 0 && approx:
				label = fmt.Sprintf("Sync this level (~%d books)", count)
			case count > 0:
				label = fmt.Sprintf("Sync this level (%d books)", count)
			}
		}
		drawCenteredText(btnFont, selectRect, truncate(label, 40), a.layout.fpx(44))
	} else {
		// Two buttons stacked: Add/Remove on top, Done below.
		half := (selectBtnH - 20) / 2
		addRect := image.Rect(selectRect.Min.X, selectY1, selectRect.Max.X, selectY1+half+20)
		doneRect = image.Rect(selectRect.Min.X, selectY1+half+30, selectRect.Max.X, selectY2)
		ink.DrawRect(addRect, ink.Black)
		addLabel := "Add this level"
		if alreadyIn {
			addLabel = "Remove this level"
		}
		// At root with no filter picked yet, adding the root feed is
		// identical to "sync all"; offering the button there makes no
		// sense, so hide it by using an empty rect.
		if stackLen == 0 {
			addRect = image.Rectangle{}
			ink.FillArea(image.Rect(selectRect.Min.X, selectY1, selectRect.Max.X, selectY1+half+20), ink.White)
		} else {
			drawCenteredText(btnFont, addRect, truncate(addLabel, 40), a.layout.fpx(44))
		}
		ink.DrawRect(doneRect, ink.Black)
		ink.DrawRect(doneRect.Inset(2), ink.Black)
		drawCenteredText(btnFont, doneRect, fmt.Sprintf("Done (%d selected)", len(selected)), a.layout.fpx(44))
		selectRect = addRect
	}
	a.picker.mu.Lock()
	a.picker.selectRect = selectRect
	a.picker.doneRect = doneRect
	a.picker.mu.Unlock()

	ink.DrawRect(a.layout.backButton, ink.Black)
	drawCenteredText(btnFont, a.layout.backButton, "Back", a.layout.fpx(44))
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

func (a *app) shelfPickerKey(e ink.KeyEvent) bool {
	if e.Key == ink.KeyOk {
		a.picker.mu.Lock()
		hasSelection := len(a.picker.selected) > 0
		a.picker.mu.Unlock()
		if hasSelection {
			a.pickerFinishMulti()
		} else {
			a.pickerConfirmCurrent()
		}
		return true
	}
	if e.Key == ink.KeyBack {
		a.drillUp()
		return true
	}
	return false
}

func (a *app) shelfPickerPointer(e ink.PointerEvent) bool {
	if e.State != ink.PointerDown {
		return false
	}
	if e.Point.In(a.layout.backButton) {
		a.screen = screenSettings
		ink.Repaint()
		return true
	}
	a.picker.mu.Lock()
	rects := a.picker.rowRects
	// Same visibility rule as drawShelfPicker, so row indices line up
	// with what the user sees.
	subs := visibleSubsections(a.picker.level)
	selectRect := a.picker.selectRect
	doneRect := a.picker.doneRect
	offset := a.picker.offset
	prev := a.picker.prevPageRect
	next := a.picker.nextPageRect
	upRect := a.picker.upRect
	hasSelection := len(a.picker.selected) > 0
	a.picker.mu.Unlock()

	// ".. (up)" is a fixed top row and always accessible, including
	// from page 2+ where it sits above the paginated window.
	if !upRect.Empty() && e.Point.In(upRect) {
		a.drillUp()
		return true
	}
	if !selectRect.Empty() && e.Point.In(selectRect) {
		if hasSelection {
			a.toggleCurrentInSelection()
		} else {
			a.pickerConfirmCurrent()
		}
		return true
	}
	if !doneRect.Empty() && e.Point.In(doneRect) {
		a.pickerFinishMulti()
		return true
	}
	if !prev.Empty() && e.Point.In(prev) {
		a.shelfPickerPage(-1)
		return true
	}
	if !next.Empty() && e.Point.In(next) {
		a.shelfPickerPage(+1)
		return true
	}

	// rects[i] corresponds to subs[offset+i] directly now that ".."
	// lives outside the paginated window.
	for i, r := range rects {
		if !e.Point.In(r) {
			continue
		}
		abs := offset + i
		if abs >= 0 && abs < len(subs) {
			s := subs[abs]
			a.drillInto(s.Href, s.Name)
			return true
		}
	}
	return false
}

// pickerConfirmCurrent saves the currently-displayed feed as the sole
// sync filter (or clears the filter when at root) and returns to the
// settings screen. Used when no multi-selection is in progress.
func (a *app) pickerConfirmCurrent() {
	a.picker.mu.Lock()
	href := a.picker.href
	title := a.picker.title
	atRoot := len(a.picker.stack) == 0
	a.picker.mu.Unlock()
	if atRoot {
		a.setFilters(nil, nil)
	} else {
		a.setFilters([]string{href}, []string{title})
	}
	a.screen = screenSettings
	ink.Repaint()
}

// toggleCurrentInSelection adds the current feed to the selection set,
// or removes it if already present. No-op at the root (which is always
// "sync everything" and covered by the bottom Done button instead).
func (a *app) toggleCurrentInSelection() {
	a.picker.mu.Lock()
	defer a.picker.mu.Unlock()
	if len(a.picker.stack) == 0 {
		return
	}
	href := a.picker.href
	title := a.picker.title
	for i, s := range a.picker.selected {
		if s.Href == href {
			a.picker.selected = append(a.picker.selected[:i], a.picker.selected[i+1:]...)
			ink.Repaint()
			return
		}
	}
	a.picker.selected = append(a.picker.selected, FilterOption{Name: title, Href: href})
	ink.Repaint()
}

// pickerFinishMulti writes the accumulated selection into the config and
// closes the picker. An empty selection is the same as "sync everything".
func (a *app) pickerFinishMulti() {
	a.picker.mu.Lock()
	sel := append([]FilterOption(nil), a.picker.selected...)
	a.picker.mu.Unlock()
	hrefs := make([]string, 0, len(sel))
	names := make([]string, 0, len(sel))
	for _, s := range sel {
		hrefs = append(hrefs, s.Href)
		names = append(names, s.Name)
	}
	a.setFilters(hrefs, names)
	a.screen = screenSettings
	ink.Repaint()
}

// setFilters replaces the config's filter selection and persists it.
// Passing nil/empty slices clears the filter (sync everything).
func (a *app) setFilters(hrefs, names []string) {
	a.cfg.FilterHrefs = hrefs
	a.cfg.FilterNames = names
	_ = SaveConfig(a.cfgPath, a.cfg)
}
