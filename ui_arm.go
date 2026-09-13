// App shell: screen enum, the app struct, and the InkView event
// dispatch (Draw / Key / Pointer) that routes to each screen.

package main

import (
	"fmt"
	"image"
	"log"
	"os"
	"path/filepath"
	"time"

	ink "github.com/dennwc/inkview"
)

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

// feedPickerTimeout caps one picker level fetch (probe + every page of the
// feed, or probe + directory listing for WebDAV). Without it a server that
// accepts the connection but never answers leaves the picker on
// "Loading..." for the client's 5-minute response-header timeout. Two
// minutes is enough to page through a large CWA "All books" feed over slow
// Wi-Fi while still bounding a hung server.
const feedPickerTimeout = 2 * time.Minute

// tapDebounce is the minimum gap between two pointer events that will
// both be treated as taps. PocketBook sometimes fires only a PointerDown
// or only a PointerUp for a glancing touch; the bottom-row Network
// button was the most visible victim. Reacting to both event states and
// dropping close duplicates gives every tap a chance to land without
// letting a genuine down+up pair fire the same action twice.
const tapDebounce = 250 * time.Millisecond

// app implements ink.App for the pocketbeam device UI.
type app struct {
	cfgPath       string
	cfg           *Config
	client        *Client
	store         *Store
	screen        screen
	wizard        wizardState
	sync          syncState
	picker        feedPickerState
	dirPicker     dirPickerState
	delConfirm    deleteConfirmState
	spaceWarn     spaceWarnState
	profileList   profileListState
	profileDetail profileDetailState
	update        updateState
	libRefresh    libRefreshState
	lastSync      SyncSummary
	hasLastSync   bool
	bookCount     int
	netStop       func()
	layout        layout
	lastTap       time.Time
	// hourglass tracks whether InkView is currently showing the busy
	// icon, so it is only taken down by the screen that raised it.
	// Touched from the event loop only (draw passes and Draw itself).
	hourglass bool
}

// showHourglassAt raises InkView's busy icon at p. Draw takes it down
// again on the next pass, so a screen that is still busy re-raises it
// every time it draws.
func (a *app) showHourglassAt(p image.Point) {
	a.hourglass = true
	ink.ShowHourglassAt(p)
}

// hideHourglass takes the busy icon down if one is up. InkView restores
// the pixels it saved when the icon went up, so this must run before the
// screen is redrawn, and must not run when nothing was shown.
func (a *app) hideHourglass() {
	if !a.hourglass {
		return
	}
	a.hourglass = false
	ink.HideHourglass()
}

// acceptTap returns true if the event should be treated as a tap. Accepts
// PointerDown OR PointerUp to maximise the chance a glancing touch
// registers, but drops any event within tapDebounce of the previous
// accepted one so a normal down+up pair fires the handler exactly once.
func (a *app) acceptTap(e ink.PointerEvent) bool {
	if e.State != ink.PointerDown && e.State != ink.PointerUp {
		return false
	}
	now := time.Now()
	if now.Sub(a.lastTap) < tapDebounce {
		return false
	}
	a.lastTap = now
	return true
}

func newApp() *app {
	return &app{
		cfgPath: filepath.Join(ink.ConfigPath, "pocketbeam.cfg"),
	}
}

// Init is called once when the app launches.
func (a *app) Init() error {
	_ = os.Chdir(filepath.Dir(os.Args[0]))

	sz := ink.ScreenSize()
	a.layout = computeLayout(sz)
	// Record the device context so any bug report carries the exact
	// hardware and firmware string; the scale factor shows which font
	// tier the layout picked for this resolution.
	log.Printf("pocketbeam %s on %s (hw=%s fw=%s screen=%dx%d scale=%.2f)",
		version, ink.DeviceModel(), ink.HardwareType(), ink.SoftwareVersion(),
		sz.X, sz.Y, a.layout.scale)

	// Install the device-aware User-Agent used by the updater. This
	// header goes only to the release endpoint (OPDS / WebDAV requests
	// still identify themselves as a plain pocketbeam/<version>, since
	// the user already knows which device their server is talking to).
	// Captured as a closure so each request reads the current values
	// rather than whatever was present at Init.
	UserAgentFn = func() string {
		sz := ink.ScreenSize()
		return fmt.Sprintf("pocketbeam/%s (%s; %s; fw %s; %dx%d)",
			version, ink.DeviceModel(), ink.HardwareType(), ink.SoftwareVersion(), sz.X, sz.Y)
	}

	if err := ink.InitCerts(); err != nil {
		log.Printf("InitCerts: %v", err)
	}
	stop, err := ink.KeepNetwork()
	if err != nil {
		log.Printf("KeepNetwork: %v", err)
	} else {
		a.netStop = stop
	}

	ink.SetKeyboardHandler(func(s string) { a.onKeyboardInput(s) })

	if cfg, err := LoadConfig(a.cfgPath); err == nil {
		if client, err := NewClient(cfg.Host, cfg.User, cfg.Pass); err == nil {
			if store, err := OpenStore(cfg.StateDB); err == nil {
				a.cfg = cfg
				a.client = client
				a.store = store
				a.refreshMainStats()
				if cfg.CheckUpdates {
					go a.backgroundUpdateCheck()
				}
				a.screen = screenMain
				return nil
			}
		}
	}
	a.screen = screenFirstRun
	a.wizard.step = stepWelcome
	return nil
}

func (a *app) Close() error {
	if a.store != nil {
		_ = a.store.Close()
	}
	if a.netStop != nil {
		a.netStop()
	}
	return nil
}

func (a *app) Draw() {
	// Every repaint starts without the busy icon; a screen that is still
	// loading raises it again below. This is what takes the hourglass
	// down when a picker fetch fails or the user leaves mid-load.
	a.hideHourglass()
	ink.ClearScreen()
	switch a.screen {
	case screenFirstRun:
		a.drawWizard()
	case screenMain:
		a.drawMain()
	case screenSettings:
		a.drawSettings()
	case screenShelfPicker:
		a.drawShelfPicker()
	case screenDirPicker:
		a.drawDirPicker()
	case screenDeleteConfirm:
		a.drawDeleteConfirm()
	case screenSpaceWarn:
		a.drawSpaceWarn()
	case screenProfileList:
		a.drawProfileList()
	case screenProfileDetail:
		a.drawProfileDetail()
	case screenUpdate:
		a.drawUpdate()
	case screenLibraryRefresh:
		a.drawLibraryRefresh()
	}
	ink.FullUpdate()
}

func (a *app) Key(e ink.KeyEvent) bool {
	// Library refresh is a modal auto-closing screen. Scanner has
	// foreground for the duration, so keys can only arrive in the brief
	// window before scanner takes over; swallow them either way.
	if a.screen == screenLibraryRefresh {
		return true
	}
	// Back key behaviour: return to previous screen from settings/picker,
	// quit from first-run welcome/error or from main (if idle).
	if e.Key == ink.KeyBack && a.wizard.step != stepTesting && !a.syncActive() {
		switch a.screen {
		case screenSettings:
			a.screen = screenMain
			ink.Repaint()
			return true
		case screenShelfPicker, screenDirPicker, screenProfileList, screenUpdate:
			a.screen = screenSettings
			ink.Repaint()
			return true
		case screenProfileDetail:
			a.screen = screenProfileList
			ink.Repaint()
			return true
		case screenDeleteConfirm:
			// Back key on the prompt is equivalent to "No": keep books,
			// release the sync goroutine.
			a.answerDelete(false)
			return true
		case screenSpaceWarn:
			// Back key on the oversize prompt = cancel the sync.
			a.answerSpace(false)
			return true
		default:
			ink.Exit()
			return true
		}
	}
	switch a.screen {
	case screenFirstRun:
		return a.wizardKey(e)
	case screenMain:
		return a.mainKey(e)
	case screenSettings:
		return a.settingsKey(e)
	case screenShelfPicker:
		return a.shelfPickerKey(e)
	case screenDirPicker:
		return a.dirPickerKey(e)
	case screenDeleteConfirm:
		return a.deleteConfirmKey(e)
	case screenSpaceWarn:
		return a.spaceWarnKey(e)
	case screenProfileList:
		return a.profileListKey(e)
	case screenProfileDetail:
		return a.profileDetailKey(e)
	case screenUpdate:
		return a.updateKey(e)
	}
	return false
}

func (a *app) Pointer(e ink.PointerEvent) bool {
	switch a.screen {
	case screenFirstRun:
		return a.wizardPointer(e)
	case screenMain:
		return a.mainPointer(e)
	case screenSettings:
		return a.settingsPointer(e)
	case screenShelfPicker:
		return a.shelfPickerPointer(e)
	case screenDirPicker:
		return a.dirPickerPointer(e)
	case screenDeleteConfirm:
		return a.deleteConfirmPointer(e)
	case screenSpaceWarn:
		return a.spaceWarnPointer(e)
	case screenProfileList:
		return a.profileListPointer(e)
	case screenProfileDetail:
		return a.profileDetailPointer(e)
	case screenUpdate:
		return a.updatePointer(e)
	case screenLibraryRefresh:
		return true
	}
	return false
}

func (a *app) Touch(e ink.TouchEvent) bool        { return false }
func (a *app) Orientation(o ink.Orientation) bool { return false }

// refreshMainStats pulls fresh last-sync + book-count from the store. Called
// on screen entry and after a completed sync.
func (a *app) refreshMainStats() {
	if a.store == nil {
		return
	}
	if sum, ok, err := a.store.LastSync(); err == nil && ok {
		a.lastSync = sum
		a.hasLastSync = true
	}
	if n, err := a.store.BookCount(); err == nil {
		a.bookCount = n
	}
}

func (a *app) syncActive() bool {
	a.sync.mu.Lock()
	defer a.sync.mu.Unlock()
	return a.sync.active
}
