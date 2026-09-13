// Server profiles: the paginated list and the per-profile detail panel
// with its make-active / delete actions.

package main

import (
	"fmt"
	"image"
	"log"
	"sync"

	ink "github.com/dennwc/inkview"
)

// profileListState holds the snapshot shown on the profile-list screen.
// Every row (including the active profile) is now a tap target that
// opens a per-profile detail panel; per-profile destructive actions
// live there, not on the list.
type profileListState struct {
	mu           sync.Mutex
	names        []string
	active       string
	err          error
	offset       int
	pageSize     int // rows per page; written during draw
	rowRects     []image.Rectangle
	prevPageRect image.Rectangle
	nextPageRect image.Rectangle
	addRect      image.Rectangle
}

// profileDetailState is the per-profile panel opened by tapping a row
// in the profile list. It shows the profile's backend + host and
// exposes Make-active and Delete actions. confirmDelete arms the
// delete button: the first tap changes the label, the second commits.
type profileDetailState struct {
	mu            sync.Mutex
	name          string
	backend       string
	host          string
	isActive      bool
	loadErr       error
	makeActiveBtn image.Rectangle
	deleteBtn     image.Rectangle
	confirmDelete bool
}

// openProfileList refreshes the snapshot and switches to the profile
// screen. Profile data is loaded synchronously because the on-disk file
// is tiny.
func (a *app) openProfileList() {
	names, active, err := ListProfiles(a.cfgPath)
	a.profileList.mu.Lock()
	a.profileList.names = names
	a.profileList.active = active
	a.profileList.err = err
	// Drop the previous visit's tap targets: the error path returns
	// before drawing new ones.
	a.profileList.rowRects = nil
	a.profileList.prevPageRect = image.Rectangle{}
	a.profileList.nextPageRect = image.Rectangle{}
	a.profileList.addRect = image.Rectangle{}
	a.profileList.offset = 0
	a.profileList.mu.Unlock()
	a.SetScreen(screenProfileList)
	ink.Repaint()
}

func (a *app) drawProfileList() {
	title := a.font(ink.DefaultFontBold, 64)
	title.SetActive(ink.Black)

	body := a.font(ink.DefaultFont, 32)
	rowTitleFont := a.font(ink.DefaultFontBold, 36)
	rowSubFont := a.font(ink.DefaultFont, 28)
	btnFont := a.font(ink.DefaultFontBold, 44)
	smallFont := a.font(ink.DefaultFont, 26)

	a.profileList.mu.Lock()
	names := append([]string(nil), a.profileList.names...)
	active := a.profileList.active
	perr := a.profileList.err
	offset := a.profileList.offset
	a.profileList.mu.Unlock()

	title.SetActive(ink.Black)
	ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(140)}, "Server profiles")

	if perr != nil {
		body.SetActive(ink.Black)
		ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(240)}, "Could not list profiles:")
		ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(290)}, truncate(perr.Error(), 60))
		ink.DrawRect(a.layout.backButton, ink.Black)
		drawCenteredText(btnFont, a.layout.backButton, "Back", a.layout.fpx(44))
		return
	}

	// One list row per profile, paginated like the pickers so a long
	// profile list cannot overrun the buttons below it. Tap anywhere on
	// a row to open the detail panel. The active profile shows "Active"
	// as its subtitle so users can see at a glance which one they're on.
	btnH := a.layout.sy(100)
	addY2 := a.layout.backButton.Min.Y - a.layout.sy(40)
	addY1 := addY2 - btnH

	rows := make([]listRow, 0, len(names))
	for _, n := range names {
		sub := ""
		if n == active {
			sub = "Active"
		}
		rows = append(rows, listRow{title: n, subtitle: sub})
	}
	list := a.drawPagedList(
		listFonts{rowTitle: rowTitleFont, rowSub: rowSubFont, button: btnFont, label: smallFont},
		a.layout.pickerAreaTop, addY1-a.layout.sy(40), rows, offset)

	// Primary action: Add new server, sized like the Sync buttons so it
	// reads as the same kind of commit action.
	addRect := image.Rect(a.layout.margin, addY1, a.layout.screen.X-a.layout.margin, addY2)
	ink.DrawRect(addRect, ink.Black)
	ink.DrawRect(addRect.Inset(2), ink.Black)
	drawCenteredText(btnFont, addRect, "Add new server", a.layout.fpx(44))

	a.profileList.mu.Lock()
	a.profileList.offset = list.offset
	a.profileList.pageSize = list.pageSize
	a.profileList.rowRects = list.rows
	a.profileList.prevPageRect = list.prev
	a.profileList.nextPageRect = list.next
	a.profileList.addRect = addRect
	a.profileList.mu.Unlock()

	ink.DrawRect(a.layout.backButton, ink.Black)
	drawCenteredText(btnFont, a.layout.backButton, "Back", a.layout.fpx(44))
}

func (a *app) profileListKey(e ink.KeyEvent) bool {
	if e.Key == ink.KeyBack {
		a.SetScreen(screenSettings)
		ink.Repaint()
		return true
	}
	return false
}

func (a *app) profileListPointer(e ink.PointerEvent) bool {
	if e.Point.In(a.layout.backButton) {
		a.SetScreen(screenSettings)
		ink.Repaint()
		return true
	}
	a.profileList.mu.Lock()
	rects := a.profileList.rowRects
	names := append([]string(nil), a.profileList.names...)
	addRect := a.profileList.addRect
	offset := a.profileList.offset
	prev := a.profileList.prevPageRect
	next := a.profileList.nextPageRect
	a.profileList.mu.Unlock()

	if e.Point.In(addRect) {
		a.startAddProfile()
		return true
	}
	if !prev.Empty() && e.Point.In(prev) {
		a.profileListPage(-1)
		return true
	}
	if !next.Empty() && e.Point.In(next) {
		a.profileListPage(+1)
		return true
	}
	for i, r := range rects {
		if !e.Point.In(r) {
			continue
		}
		if abs := offset + i; abs < len(names) {
			a.openProfileDetail(names[abs])
			return true
		}
	}
	return false
}

// profileListPage scrolls the profile list by one page and repaints.
func (a *app) profileListPage(direction int) {
	a.profileList.mu.Lock()
	a.profileList.offset = pageStep(a.profileList.offset, a.profileList.pageSize, direction)
	a.profileList.mu.Unlock()
	ink.Repaint()
}

// switchProfile marks the chosen profile active, reloads the config,
// swaps the underlying store, and flips back to the main screen.
func (a *app) switchProfile(name string) {
	if err := SetActiveProfile(a.cfgPath, name); err != nil {
		a.profileList.mu.Lock()
		a.profileList.err = err
		a.profileList.mu.Unlock()
		ink.Repaint()
		return
	}
	a.reloadActiveConfig()
	a.SetScreen(screenMain)
	ink.Repaint()
}

// deleteProfileByName removes the named profile. When the active
// profile disappears (either by direct deletion or because it was
// renamed / wasn't the target), fileDoc promotes another profile to
// active; reloadActiveConfig picks that up. Deleting the last profile
// drops to the first-run wizard.
func (a *app) deleteProfileByName(name string) {
	cfg := a.Config()
	wasActive := cfg != nil && cfg.Profile == name
	if err := DeleteProfile(a.cfgPath, name); err != nil {
		a.profileDetail.mu.Lock()
		a.profileDetail.loadErr = err
		a.profileDetail.mu.Unlock()
		ink.Repaint()
		return
	}
	names, _, _ := ListProfiles(a.cfgPath)
	if len(names) == 0 {
		// No profiles left; tear down in-memory state and hand control
		// to the wizard. The store stays on disk until the user sets up
		// a new profile pointing at a (possibly new) StateDB path.
		a.ClearSession()
		a.UpdateWizard(func(w *wizardState) { *w = wizardState{step: stepWelcome} })
		a.SetScreen(screenFirstRun)
		ink.Repaint()
		return
	}
	if wasActive {
		a.reloadActiveConfig()
	}
	a.openProfileList()
}

// reloadActiveConfig rereads the config file, closes any existing store
// handle, and opens a fresh one for the now-active profile. Called
// whenever the active profile changes.
func (a *app) reloadActiveConfig() {
	cfg, err := LoadConfig(a.cfgPath)
	if err != nil {
		log.Printf("reload config: %v", err)
		return
	}
	store, err := OpenStore(cfg.StateDB)
	if err != nil {
		log.Printf("open store: %v", err)
		return
	}
	client, _ := NewClient(cfg.Host, cfg.User, cfg.Pass)
	// SetSession closes the store handle it replaces.
	a.SetSession(cfg, client, store)
	a.refreshMainStats()
}

// startAddProfile enters the wizard in "add-profile" mode: the first
// step asks for the new profile's name, then the normal URL / user /
// pass flow runs. Saving creates the new section and marks it active;
// the recorded returnTo sends the Back key back to the profile list
// instead of out of the app.
func (a *app) startAddProfile() {
	a.UpdateWizard(func(w *wizardState) {
		*w = wizardState{step: stepProfileName, addProfile: true, returnTo: screenProfileList}
	})
	a.SetScreen(screenFirstRun)
	ink.OpenKeyboard("new-profile-name", 40)
}

// ---------- Profile detail ----------

// openProfileDetail loads the named profile's fields and switches to
// its detail panel. Falls back to a name-only display if the profile
// can't be read (e.g. mid-edit file).
func (a *app) openProfileDetail(name string) {
	p, err := LoadProfileByName(a.cfgPath, name)
	active := ""
	if cfg := a.Config(); cfg != nil {
		active = cfg.Profile
	}
	a.profileDetail.mu.Lock()
	a.profileDetail.name = name
	a.profileDetail.loadErr = err
	a.profileDetail.confirmDelete = false
	a.profileDetail.isActive = (name == active)
	if err == nil {
		a.profileDetail.backend = p.Backend
		a.profileDetail.host = p.Host
	} else {
		a.profileDetail.backend = ""
		a.profileDetail.host = ""
	}
	a.profileDetail.mu.Unlock()
	a.SetScreen(screenProfileDetail)
	ink.Repaint()
}

func (a *app) drawProfileDetail() {
	title := a.font(ink.DefaultFontBold, 64)
	title.SetActive(ink.Black)

	body := a.font(ink.DefaultFont, 32)
	body.SetActive(ink.Black)

	btnFont := a.font(ink.DefaultFontBold, 44)

	a.profileDetail.mu.Lock()
	name := a.profileDetail.name
	backend := a.profileDetail.backend
	host := a.profileDetail.host
	isActive := a.profileDetail.isActive
	loadErr := a.profileDetail.loadErr
	confirming := a.profileDetail.confirmDelete
	a.profileDetail.mu.Unlock()

	title.SetActive(ink.Black)
	ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(140)}, truncate(name, 40))
	body.SetActive(ink.DarkGray)
	status := "Profile"
	if isActive {
		status = "Profile · Active"
	}
	ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(200)}, status)
	a.drawHairline(a.layout.margin, a.layout.screen.X-a.layout.margin, a.layout.sy(240))

	body.SetActive(ink.Black)
	if loadErr != nil {
		ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(310)}, "Could not load profile:")
		body.SetActive(ink.DarkGray)
		ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(360)}, truncate(loadErr.Error(), 60))
	} else {
		ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(310)}, backend)
		body.SetActive(ink.DarkGray)
		ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(360)}, truncate(host, 55))
	}

	// Action stack anchored above Back: Delete (always) + Make active
	// (only when this profile isn't the active one).
	btnH := a.layout.sy(100)
	backMin := a.layout.backButton.Min.Y
	delY2 := backMin - a.layout.sy(40)
	delY1 := delY2 - btnH
	makeActiveY2 := delY1 - a.layout.sy(30)
	makeActiveY1 := makeActiveY2 - btnH
	contentW := a.layout.screen.X - 2*a.layout.margin

	var makeActiveBtn image.Rectangle
	if !isActive && loadErr == nil {
		makeActiveBtn = image.Rect(a.layout.margin, makeActiveY1, a.layout.margin+contentW, makeActiveY2)
		ink.DrawRect(makeActiveBtn, ink.Black)
		ink.DrawRect(makeActiveBtn.Inset(2), ink.Black)
		drawCenteredText(btnFont, makeActiveBtn, "Make active", a.layout.fpx(44))
	}

	deleteBtn := image.Rect(a.layout.margin, delY1, a.layout.margin+contentW, delY2)
	ink.DrawRect(deleteBtn, ink.Black)
	delLabel := "Delete this profile"
	if confirming {
		if isActive {
			names, _, _ := ListProfiles(a.cfgPath)
			if len(names) == 1 {
				delLabel = "Tap again to reset pocketbeam"
			} else {
				delLabel = fmt.Sprintf("Tap again to delete \"%s\"", name)
			}
		} else {
			delLabel = fmt.Sprintf("Tap again to delete \"%s\"", name)
		}
		ink.DrawRect(deleteBtn.Inset(2), ink.Black)
	}
	drawCenteredText(btnFont, deleteBtn, truncate(delLabel, 40), a.layout.fpx(44))

	a.profileDetail.mu.Lock()
	a.profileDetail.makeActiveBtn = makeActiveBtn
	a.profileDetail.deleteBtn = deleteBtn
	a.profileDetail.mu.Unlock()

	ink.DrawRect(a.layout.backButton, ink.Black)
	drawCenteredText(btnFont, a.layout.backButton, "Back", a.layout.fpx(44))
}

func (a *app) profileDetailKey(e ink.KeyEvent) bool {
	if e.Key == ink.KeyBack {
		a.SetScreen(screenProfileList)
		ink.Repaint()
		return true
	}
	return false
}

func (a *app) profileDetailPointer(e ink.PointerEvent) bool {
	if e.Point.In(a.layout.backButton) {
		a.SetScreen(screenProfileList)
		ink.Repaint()
		return true
	}
	a.profileDetail.mu.Lock()
	makeActiveBtn := a.profileDetail.makeActiveBtn
	deleteBtn := a.profileDetail.deleteBtn
	confirming := a.profileDetail.confirmDelete
	name := a.profileDetail.name
	a.profileDetail.mu.Unlock()

	if !makeActiveBtn.Empty() && e.Point.In(makeActiveBtn) {
		a.switchProfile(name)
		return true
	}
	if e.Point.In(deleteBtn) {
		if !confirming {
			a.profileDetail.mu.Lock()
			a.profileDetail.confirmDelete = true
			a.profileDetail.mu.Unlock()
			ink.Repaint()
			return true
		}
		a.profileDetail.mu.Lock()
		a.profileDetail.confirmDelete = false
		a.profileDetail.mu.Unlock()
		a.deleteProfileByName(name)
		return true
	}
	// Tap elsewhere disarms the delete.
	if confirming {
		a.profileDetail.mu.Lock()
		a.profileDetail.confirmDelete = false
		a.profileDetail.mu.Unlock()
		ink.Repaint()
	}
	return false
}
