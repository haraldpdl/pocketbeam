// OPDS feed picker: nested navigation through the catalog with a
// multi-select filter set.

package main

import (
	"context"
	"errors"
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
// the first-row index of the page on screen; where that page's rows land
// is geometry, derived from the layout when the screen is drawn and
// again when a tap is hit-tested.
type feedPickerState struct {
	mu      sync.Mutex
	loading bool
	href    string
	title   string
	stack   []feedPickerFrame
	level   OPDSLevel
	err     error
	offset  int
	// reqID identifies the level fetch currently being awaited. The
	// up-row is tappable while a fetch is in flight, so a slow response
	// can land after the user has already drilled elsewhere; a fetch
	// whose id no longer matches drops its result instead of showing one
	// level's subsections under another level's breadcrumb.
	reqID int
	// pickerFetch cancels the superseded fetch so a stale request stops
	// working instead of merely having its result dropped.
	pickerFetch
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
	cfg := a.Config()
	seeded := make([]FilterOption, 0, len(cfg.FilterHrefs))
	for i, href := range cfg.FilterHrefs {
		name := href
		if i < len(cfg.FilterNames) && cfg.FilterNames[i] != "" {
			name = cfg.FilterNames[i]
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
	a.picker.offset = 0
	a.picker.selected = seeded
	a.picker.reqID++
	req := a.picker.reqID
	ctx, cancel := a.picker.restart()
	a.picker.mu.Unlock()
	a.SetScreen(screenShelfPicker)
	ink.Repaint()
	go a.fetchFeedLevel(ctx, cancel, "", req)
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
	a.picker.offset = 0
	a.picker.reqID++
	req := a.picker.reqID
	ctx, cancel := a.picker.restart()
	a.picker.mu.Unlock()
	ink.Repaint()
	go a.fetchFeedLevel(ctx, cancel, href, req)
}

// shelfPickerPage scrolls the list by one page. Called from the Prev /
// Next page buttons with the geometry the tap was hit-tested against.
//
// The step is that page's size rather than the rows on screen, because
// the last page can be shorter than a full window: stepping by the
// partial count would land on a non-page-aligned offset. It steps from
// the clamped offset, so an overshoot cannot accumulate.
func (a *app) shelfPickerPage(list pagedListRects, direction int) {
	a.picker.mu.Lock()
	a.picker.offset = pageStep(list.offset, list.pageSize, direction)
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
	a.picker.offset = 0
	a.picker.reqID++
	req := a.picker.reqID
	ctx, cancel := a.picker.restart()
	a.picker.mu.Unlock()
	ink.Repaint()
	go a.fetchFeedLevel(ctx, cancel, top.Href, req)
}

// fetchFeedLevel loads one picker level and stores the result, unless the
// user has moved on and req is no longer the fetch the picker waits for.
// ctx comes from feedPickerState.restart, so the next navigation cancels
// this fetch; cancel is owned here and released when the fetch returns.
func (a *app) fetchFeedLevel(ctx context.Context, cancel context.CancelFunc, href string, req int) {
	defer cancel()
	if err := a.ensureConnected(ctx); err != nil {
		a.picker.mu.Lock()
		if a.picker.reqID != req {
			a.picker.mu.Unlock()
			return
		}
		a.picker.loading = false
		a.picker.err = err
		a.picker.mu.Unlock()
		ink.Repaint()
		return
	}
	lvl, err := a.Client().FetchLevel(ctx, href)
	if errors.Is(err, context.DeadlineExceeded) {
		err = errors.New("Server did not respond in time.")
	}
	a.picker.mu.Lock()
	if a.picker.reqID != req {
		a.picker.mu.Unlock()
		return
	}
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

// feedPickerSnapshot reads the picker state as one consistent set of
// values under the lock, so neither the draw nor a tap works from a
// half-updated level.
func (a *app) feedPickerSnapshot() feedPickerSnapshot {
	a.picker.mu.Lock()
	defer a.picker.mu.Unlock()
	titles := make([]string, 0, len(a.picker.stack)+1)
	for _, f := range a.picker.stack {
		titles = append(titles, f.Title)
	}
	titles = append(titles, a.picker.title)
	return feedPickerSnapshot{
		titles:   titles,
		href:     a.picker.href,
		loading:  a.picker.loading,
		level:    a.picker.level,
		err:      a.picker.err,
		offset:   a.picker.offset,
		selected: append([]FilterOption(nil), a.picker.selected...),
	}
}

// feedPickerView is the snapshot resolved into the screen the shared
// picker draw paints.
func (a *app) feedPickerView() pickerView {
	return feedPickerViewOf(a.feedPickerSnapshot())
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
	if e.Point.In(a.layout.backButton) {
		a.SetScreen(screenSettings)
		ink.Repaint()
		return true
	}
	// The tap is hit-tested against the geometry of the state as it is
	// now, which is the screen the user tapped unless a fetch landed in
	// between - and then the rows they see are the new level's too.
	snap := a.feedPickerSnapshot()
	g := a.layout.pickerGeometry(feedPickerViewOf(snap))

	// The up-row is a fixed row above the paginated window, so it is
	// reachable from page 2+ as well.
	if !g.up.Empty() && e.Point.In(g.up) {
		a.drillUp()
		return true
	}
	if !g.primary.Empty() && e.Point.In(g.primary) {
		if len(snap.selected) > 0 {
			a.toggleCurrentInSelection()
		} else {
			a.pickerConfirmCurrent()
		}
		return true
	}
	if !g.done.Empty() && e.Point.In(g.done) {
		a.pickerFinishMulti()
		return true
	}
	if !g.list.prev.Empty() && e.Point.In(g.list.prev) {
		a.shelfPickerPage(g.list, -1)
		return true
	}
	if !g.list.next.Empty() && e.Point.In(g.list.next) {
		a.shelfPickerPage(g.list, +1)
		return true
	}

	// The view's rows are the visible subsections in order, so row i on
	// screen is subs[offset+i].
	subs := visibleSubsections(snap.level)
	for i, r := range g.list.rows {
		if e.Point.In(r) {
			sub := subs[g.list.offset+i]
			a.drillInto(sub.Href, sub.Name)
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
	a.SetScreen(screenSettings)
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
	a.SetScreen(screenSettings)
	ink.Repaint()
}

// setFilters replaces the config's filter selection and persists it.
// Passing nil/empty slices clears the filter (sync everything).
func (a *app) setFilters(hrefs, names []string) {
	a.saveConfigChange(func(c *Config) {
		c.FilterHrefs = hrefs
		c.FilterNames = names
	})
}
