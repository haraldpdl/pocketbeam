// Server profiles: the state behind the list and the detail panel, the
// taps each answers, and the make-active / delete actions. The drawing
// lives in ui_profiles.go.

package main

import (
	"log"
	"sync"

	ink "github.com/dennwc/inkview"
)

// profileListState holds the snapshot shown on the profile-list screen.
// Every row (including the active profile) is a tap target that opens a
// per-profile detail panel; per-profile destructive actions live there,
// not on the list.
type profileListState struct {
	mu     sync.Mutex
	names  []string
	active string
	err    error
	offset int
}

// profileDetailState is the per-profile panel opened by tapping a row in
// the profile list. It holds the profile's backend + host as read when
// the panel was opened, and whether the delete button is armed: the
// first tap changes the label, the second commits.
type profileDetailState struct {
	mu            sync.Mutex
	name          string
	backend       string
	host          string
	isActive      bool
	lastProfile   bool
	loadErr       error
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
	a.profileList.offset = 0
	a.profileList.mu.Unlock()
	a.SetScreen(screenProfileList)
	ink.Repaint()
}

// profileListView is one consistent read of the list state for the draw
// and for the pointer handler, which lays the page out from the same
// values the draw did.
func (a *app) profileListView() profileListView {
	a.profileList.mu.Lock()
	defer a.profileList.mu.Unlock()
	return profileListView{
		names:  append([]string(nil), a.profileList.names...),
		active: a.profileList.active,
		err:    a.profileList.err,
		offset: a.profileList.offset,
	}
}

// profileListRects is where the visible page's rows sit. Computed from
// the layout and the view rather than recorded during the draw, so the
// tap targets exist even before the first repaint has landed.
func (a *app) profileListRects(v profileListView) pagedListRects {
	return a.layout.layoutPagedList(a.layout.pickerAreaTop, a.layout.profileListBottom, len(v.names), v.offset)
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
	v := a.profileListView()
	if v.err != nil {
		// The error screen draws nothing but the message and Back.
		return false
	}
	if e.Point.In(a.layout.profileAction) {
		a.startAddProfile()
		return true
	}
	list := a.profileListRects(v)
	if !list.prev.Empty() && e.Point.In(list.prev) {
		a.profileListPage(list.pageSize, -1)
		return true
	}
	if !list.next.Empty() && e.Point.In(list.next) {
		a.profileListPage(list.pageSize, +1)
		return true
	}
	for i, r := range list.rows {
		if !e.Point.In(r) {
			continue
		}
		if abs := list.offset + i; abs < len(v.names) {
			a.openProfileDetail(v.names[abs], len(v.names))
			return true
		}
	}
	return false
}

// profileListPage scrolls the profile list by one page and repaints.
// The stored offset is clamped onto a real page start here because
// nothing else writes it back any more: the draw only reads it.
func (a *app) profileListPage(pageSize, direction int) {
	a.profileList.mu.Lock()
	next := pageStep(a.profileList.offset, pageSize, direction)
	a.profileList.offset = paginate(len(a.profileList.names), pageSize, next).offset
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
	// The first open after an upgrade rewrites every tracked book; both
	// callers repaint afterwards, which takes the icon down again.
	a.showHourglass(deviceCanvas)
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

// openProfileDetail loads the named profile's fields and switches to its
// detail panel. Falls back to a name-only display if the profile can't
// be read (e.g. mid-edit file). profileCount is how many profiles the
// list it was opened from holds, which decides whether deleting this one
// resets the app.
func (a *app) openProfileDetail(name string, profileCount int) {
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
	a.profileDetail.lastProfile = profileCount == 1
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

// profileDetailView is one consistent read of the panel's state, for the
// draw and for the pointer handler deciding which buttons are on screen.
func (a *app) profileDetailView() profileDetailView {
	a.profileDetail.mu.Lock()
	defer a.profileDetail.mu.Unlock()
	return profileDetailView{
		name:        a.profileDetail.name,
		backend:     a.profileDetail.backend,
		host:        a.profileDetail.host,
		isActive:    a.profileDetail.isActive,
		loadErr:     a.profileDetail.loadErr,
		confirming:  a.profileDetail.confirmDelete,
		lastProfile: a.profileDetail.lastProfile,
	}
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
	v := a.profileDetailView()
	if v.canMakeActive() && e.Point.In(a.layout.profileUpperAction) {
		a.switchProfile(v.name)
		return true
	}
	if e.Point.In(a.layout.profileAction) {
		a.armOrDeleteProfile(v)
		return true
	}
	// Tap elsewhere disarms the delete.
	if v.confirming {
		a.setDeleteArmed(false)
		ink.Repaint()
	}
	return false
}

// armOrDeleteProfile answers a tap on the delete button: the first one
// arms it, the second deletes the profile.
func (a *app) armOrDeleteProfile(v profileDetailView) {
	if !v.confirming {
		a.setDeleteArmed(true)
		ink.Repaint()
		return
	}
	a.setDeleteArmed(false)
	a.deleteProfileByName(v.name)
}

func (a *app) setDeleteArmed(armed bool) {
	a.profileDetail.mu.Lock()
	a.profileDetail.confirmDelete = armed
	a.profileDetail.mu.Unlock()
}
