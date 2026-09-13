// State both the InkView event loop and the background goroutines touch:
// which screen is up, the active profile's config / client / store, the
// first-run wizard's in-progress input, and the main-screen stats. The
// probe, sync, library-scan and update-check goroutines all write some of
// it while Draw / Key / Pointer read it, so it lives behind one mutex and
// is only reached through the accessors below.

package main

import (
	"image"
	"sync"
)

// screen identifies which UI screen is currently up.
type screen int

const (
	screenFirstRun screen = iota
	screenMain
	screenSettings
	screenShelfPicker
	screenDirPicker
	screenDeleteConfirm
	screenSpaceWarn
	screenProfileList
	screenProfileDetail
	screenUpdate
	screenLibraryRefresh
)

// wizardStep tracks where the user is in the first-run flow.
type wizardStep int

const (
	stepWelcome wizardStep = iota
	stepProfileName
	stepBackend
	stepURL
	stepUser
	stepPass
	stepTesting
	stepError
)

// wizardState holds the in-progress first-run input and result.
type wizardState struct {
	step    wizardStep
	backend string // "opds" or "webdav"
	url     string
	user    string
	pass    string
	name    string // profile name; "default" on the initial first run, user-entered when adding another
	err     error
	// addProfile is true when the wizard is being used to create another
	// profile from the profile-list screen rather than the initial
	// first-run setup. It makes a successful probe flip the config's
	// active marker to the profile just created.
	addProfile bool
	// returnTo is the screen the Back key leaves the wizard for. The
	// wizard is reused to edit the active server (Settings -> Server) and
	// to add a profile (profile list -> Add), and those entries record
	// where the user came from. The zero value screenFirstRun means the
	// wizard owns the app because no profile is set up yet, so there is
	// nothing to go back to.
	returnTo screen
	// Tap targets for the backend-choice step, captured during draw so the
	// pointer handler knows where the two buttons live.
	opdsBtn   image.Rectangle
	webdavBtn image.Rectangle
}

// mainStats is the last-sync summary and library size the main screen
// shows. Recomputed from the store after every sync (on the sync
// goroutine) and read during draw.
type mainStats struct {
	lastSync    SyncSummary
	hasLastSync bool
	bookCount   int
}

// appState is embedded in app. Callers never touch its fields directly:
// each accessor takes mu for the whole read or write, so a transition
// that changes several fields is never observed half-applied.
type appState struct {
	mu sync.Mutex

	// drawMu serializes every repaint - the event loop's full passes
	// (DrawFull) and the partial ones the background goroutines push
	// (DrawIfOn) - against each other and against screen transitions.
	// Testing the screen and drawing are two steps: without this lock a
	// goroutine can pass the test, the event loop can switch screens and
	// repaint, and the goroutine then paints its strip over the new
	// screen with nothing queued to clean it up. It is not mu because
	// the draw callbacks reach back into the accessors, which take mu
	// themselves.
	drawMu sync.Mutex

	screen screen
	// cfg is immutable once published. Edits go through UpdateConfig,
	// which mutates a copy and swaps the pointer in, so a goroutine that
	// already read the pointer keeps a consistent snapshot.
	cfg    *Config
	client *Client
	store  *Store
	wizard wizardState
	stats  mainStats
}

// Screen reports which screen the UI is on.
func (s *appState) Screen() screen {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.screen
}

// SetScreen records a screen transition. Repainting stays with the
// caller: some transitions arm a spinner or open a keyboard first. It
// waits for any in-flight DrawIfOn, so a transition never lands in the
// middle of a background goroutine's draw.
func (s *appState) SetScreen(to screen) {
	s.drawMu.Lock()
	defer s.drawMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.screen = to
}

// DrawIfOn runs draw only while the app is on want, and blocks screen
// transitions for its duration. Every draw a goroutine outside the
// InkView event loop pushes goes through here: the goroutine owns those
// pixels only as long as the screen it drew for is still up.
func (s *appState) DrawIfOn(want screen, draw func()) {
	s.drawMu.Lock()
	defer s.drawMu.Unlock()
	s.mu.Lock()
	cur := s.screen
	s.mu.Unlock()
	if cur != want {
		return
	}
	draw()
}

// DrawFull runs a repaint raised on the InkView event loop - a whole
// screen or a single row - under the same lock DrawIfOn takes, so it
// cannot interleave with a goroutine's partial one. They share InkView's
// single active face and colour, and a full pass clears the screen
// before it draws, so an unserialized overlap renders labels in the
// wrong face or leaves a blank band where the partial draw landed
// between the clear and the redraw. No screen test: the event loop is
// what changes the screen, so whatever it is drawing is what is up.
func (s *appState) DrawFull(draw func()) {
	s.drawMu.Lock()
	defer s.drawMu.Unlock()
	draw()
}

// Config returns the active profile's config, or nil when no profile is
// set up yet. The returned config must not be mutated; use UpdateConfig.
func (s *appState) Config() *Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg
}

// Client returns the OPDS client for the active profile, or nil when no
// profile is set up yet.
func (s *appState) Client() *Client {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.client
}

// Store returns the state database for the active profile, or nil when
// no profile is set up yet.
func (s *appState) Store() *Store {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.store
}

// Session returns config, client and store as one matching set, for the
// callers (the sync goroutine) that need all three to belong to the same
// profile for the whole run.
func (s *appState) Session() (*Config, *Client, *Store) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg, s.client, s.store
}

// SetSession publishes a freshly-opened profile and closes the store it
// replaces, so switching profiles cannot leak the old sqlite handle.
func (s *appState) SetSession(cfg *Config, client *Client, store *Store) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.store != nil && s.store != store {
		_ = s.store.Close()
	}
	s.cfg, s.client, s.store = cfg, client, store
}

// ClearSession drops the active profile, closing its store. Used when the
// last profile is deleted and the app falls back to the wizard.
func (s *appState) ClearSession() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.store != nil {
		_ = s.store.Close()
	}
	s.cfg, s.client, s.store = nil, nil, nil
}

// UpdateConfig applies fn to a copy of the active config and publishes
// the copy, returning it so the caller can persist it. Returns nil when
// no profile is active. fn must replace slice fields wholesale rather
// than editing them in place: the copy shares their backing arrays.
func (s *appState) UpdateConfig(fn func(*Config)) *Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cfg == nil {
		return nil
	}
	next := *s.cfg
	fn(&next)
	s.cfg = &next
	return &next
}

// Wizard returns a snapshot of the wizard state for drawing and for the
// probe goroutine, which reads the entered server details in one piece.
func (s *appState) Wizard() wizardState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.wizard
}

// UpdateWizard applies fn to the wizard state. Steps that carry a result
// (an error plus the error step, say) are set in one call so a repaint
// never catches the pair half-applied.
func (s *appState) UpdateWizard(fn func(*wizardState)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(&s.wizard)
}

// Stats returns a snapshot of the main-screen stats.
func (s *appState) Stats() mainStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stats
}

// UpdateStats applies fn to the main-screen stats. fn decides which
// fields a partly-failed refresh replaces.
func (s *appState) UpdateStats(fn func(*mainStats)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(&s.stats)
}

// backTarget reports the screen the hardware Back key returns to from
// cur, and whether there is one at all. Nothing here changes state, so
// the navigation graph can be tested off-device; the ARM key handler
// applies the answer and quits the app when ok is false.
//
// The confirmation screens (delete, space warning) are not in the graph:
// Back answers their question instead of navigating.
func backTarget(cur screen, wiz wizardState) (screen, bool) {
	switch cur {
	case screenSettings:
		return screenMain, true
	case screenShelfPicker, screenDirPicker, screenProfileList, screenUpdate:
		return screenSettings, true
	case screenProfileDetail:
		return screenProfileList, true
	case screenFirstRun:
		if wiz.returnTo != screenFirstRun {
			return wiz.returnTo, true
		}
	}
	return cur, false
}
