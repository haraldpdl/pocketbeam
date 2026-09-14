// App shell: the app struct and the InkView event dispatch
// (Draw / Key / Pointer) that routes to each screen.

package main

import (
	"context"
	"fmt"
	"image"
	"log"
	"os"
	"path/filepath"
	"time"

	ink "github.com/dennwc/inkview"
)

// feedPickerTimeout caps one picker level fetch (probe + every page of the
// feed, or probe + directory listing for WebDAV). Without it a server that
// accepts the connection but never answers leaves the picker on
// "Loading..." for the client's 5-minute response-header timeout. Two
// minutes is enough to page through a large CWA "All books" feed over slow
// Wi-Fi while still bounding a hung server.
const feedPickerTimeout = 2 * time.Minute

// pickerFetch owns the cancel func of the picker level fetch currently in
// flight. Both pickers keep the up-row tappable while a fetch runs, so a
// user backing out of a hung server starts the next fetch before the
// previous one has returned. Dropping the stale result by request id is
// not enough on its own: the superseded goroutine would keep probing and
// walking the catalog for the rest of feedPickerTimeout, so several
// multi-minute walks pile up competing for the device's Wi-Fi and CPU,
// and their concurrent ensureConnected calls race on Client.IsCWA.
// Cancelling the superseded context tears that request down instead; the
// request id stays as the filter for a result that still lands first.
//
// Embedded in feedPickerState and dirPickerState, guarded by their mutex.
type pickerFetch struct {
	cancel context.CancelFunc
}

// restart cancels the fetch in flight and returns the context for its
// replacement. The caller must hold the picker's mutex, and the fetch
// goroutine must defer the returned cancel.
func (p *pickerFetch) restart() (context.Context, context.CancelFunc) {
	if p.cancel != nil {
		p.cancel()
	}
	ctx, cancel := context.WithTimeout(context.Background(), feedPickerTimeout)
	p.cancel = cancel
	return ctx, cancel
}

// app implements ink.App for the pocketbeam device UI. The state shared
// with background goroutines lives in the embedded appState; everything
// declared here is either fixed at startup or touched by the InkView
// event loop alone.
type app struct {
	appState

	cfgPath       string
	sync          syncState
	picker        feedPickerState
	dirPicker     dirPickerState
	delConfirm    deleteConfirmState
	spaceWarn     spaceWarnState
	profileList   profileListState
	profileDetail profileDetailState
	update        updateState
	libRefresh    libRefreshState
	netStop       func()
	layout        layout
	// taps gates the pointer stream for every screen; see taps.go.
	taps tapGate
	// hourglass tracks whether InkView is currently showing the busy
	// icon, so it is only taken down by the screen that raised it.
	// Touched from the event loop only (draw passes and Draw itself).
	hourglass bool
}

// showHourglassAt raises the busy icon at p. Draw takes it down again on
// the next pass, so a screen that is still busy re-raises it every time
// it draws.
func (a *app) showHourglassAt(c Canvas, p image.Point) {
	a.hourglass = true
	c.ShowHourglassAt(p)
}

// showHourglass raises the busy icon in the middle of the screen, for a
// wait that starts before a screen (and so a layout to place the icon
// against) exists. Taken down by the same draw pass as showHourglassAt.
func (a *app) showHourglass(c Canvas) {
	a.hourglass = true
	c.ShowHourglass()
}

// hideHourglass takes the busy icon down if one is up. The panel restores
// the pixels it saved when the icon went up, so this must run before the
// screen is redrawn, and must not run when nothing was shown.
func (a *app) hideHourglass(c Canvas) {
	if !a.hourglass {
		return
	}
	a.hourglass = false
	c.HideHourglass()
}

// acceptTap reports whether a pointer event should fire a tap handler.
// Called once per event in Pointer, ahead of the dispatch, so the gate
// sees whole touches even when one screen hands over to another
// mid-touch.
func (a *app) acceptTap(e ink.PointerEvent) bool {
	return a.taps.accept(pointerPhase(e.State), time.Now())
}

// pointerPhase maps an InkView pointer state onto the portable phase the
// gate reasons about. Move, long and hold are carried by tapOther: they
// belong to a touch that is already open, not to a new tap.
func pointerPhase(s ink.PointerState) tapPhase {
	switch s {
	case ink.PointerDown:
		return tapPress
	case ink.PointerUp:
		return tapRelease
	default:
		return tapOther
	}
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
			// Opening the store upgrades a state DB written by an older
			// install, which rewrites every tracked book. That is a
			// one-off, but on a device holding a large library it is long
			// enough that an unmarked screen reads as a hung app; the
			// first draw takes the icon down again.
			a.showHourglass(deviceCanvas)
			if store, err := OpenStore(cfg.StateDB); err == nil {
				a.SetSession(cfg, client, store)
				a.refreshMainStats()
				if cfg.CheckUpdates {
					go a.backgroundUpdateCheck()
				}
				a.SetScreen(screenMain)
				return nil
			}
		}
	}
	a.SetScreen(screenFirstRun)
	a.UpdateWizard(func(w *wizardState) { w.step = stepWelcome })
	return nil
}

func (a *app) Close() error {
	// Cached faces are deliberately not closed here; see faceCache in
	// canvas.go.
	if store := a.Store(); store != nil {
		_ = store.Close()
	}
	if a.netStop != nil {
		a.netStop()
	}
	return nil
}

// Draw paints the current screen. InkView runs it from the event loop
// for the repaints ink.Repaint queues, including the ones background
// goroutines queue, so it runs under DrawFull: ClearScreen, the screen
// and FullUpdate have to land as one pass, or a progress tick's partial
// draw interleaves with it and paints in the wrong face or over a
// freshly cleared screen.
func (a *app) Draw() {
	a.DrawFull(func() { a.drawCurrentScreen(deviceCanvas) })
}

func (a *app) drawCurrentScreen(c Canvas) {
	// Every repaint starts without the busy icon; a screen that is still
	// loading raises it again below. This is what takes the hourglass
	// down when a picker fetch fails or the user leaves mid-load.
	a.hideHourglass(c)
	c.Clear()
	switch a.Screen() {
	case screenFirstRun:
		a.drawWizard(c)
	case screenMain:
		drawMain(c, a.layout, a.mainView())
	case screenSettings:
		a.drawSettings(c)
	case screenShelfPicker:
		a.drawShelfPicker(c)
	case screenDirPicker:
		a.drawDirPicker(c)
	case screenDeleteConfirm:
		a.drawDeleteConfirm(c)
	case screenSpaceWarn:
		a.drawSpaceWarn(c)
	case screenProfileList:
		a.drawProfileList(c)
	case screenProfileDetail:
		a.drawProfileDetail(c)
	case screenUpdate:
		a.drawUpdate(c)
	case screenLibraryRefresh:
		a.drawLibraryRefresh(c)
	}
	c.FullUpdate()
}

func (a *app) Key(e ink.KeyEvent) bool {
	// Library refresh is a modal auto-closing screen. Scanner has
	// foreground for the duration, so keys can only arrive in the brief
	// window before scanner takes over; swallow them either way.
	cur := a.Screen()
	if cur == screenLibraryRefresh {
		return true
	}
	wiz := a.Wizard()
	// Back key behaviour: answer the confirmation prompts, otherwise walk
	// back up the screen graph, and quit only where there is nowhere left
	// to go (main screen, genuine first-run wizard).
	if e.Key == ink.KeyBack && wiz.step != stepTesting && !a.syncActive() {
		switch cur {
		case screenDeleteConfirm:
			// Back key on the prompt is equivalent to "No": keep books,
			// release the sync goroutine.
			a.answerDelete(false)
			return true
		case screenSpaceWarn:
			// Back key on the oversize prompt = cancel the sync.
			a.answerSpace(false)
			return true
		}
		to, ok := backTarget(cur, wiz)
		if !ok {
			ink.Exit()
			return true
		}
		if cur == screenFirstRun {
			// Leaving the reused wizard discards the half-entered server
			// details, so the next visit starts clean and the password
			// typed for an abandoned attempt does not linger.
			a.UpdateWizard(func(w *wizardState) { *w = wizardState{} })
		}
		a.SetScreen(to)
		ink.Repaint()
		return true
	}
	switch cur {
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
	cur := a.Screen()
	// One gate for the whole app: the screen handlers below see taps,
	// never raw pointer events. Library refresh is modal and hands the
	// foreground to the PocketBook scanner, so it swallows everything
	// either way, but its events still go through the gate so the
	// press/release pairing survives into the screen that follows.
	tap := a.acceptTap(e)
	if cur == screenLibraryRefresh {
		return true
	}
	if !tap {
		return false
	}
	switch cur {
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
	}
	return false
}

func (a *app) Touch(e ink.TouchEvent) bool        { return false }
func (a *app) Orientation(o ink.Orientation) bool { return false }

// refreshMainStats pulls fresh last-sync + book-count from the store. Called
// on screen entry and after a completed sync, i.e. from the sync goroutine
// as well as the event loop. A query that fails leaves its previous value
// in place rather than blanking the screen.
func (a *app) refreshMainStats() {
	store := a.Store()
	if store == nil {
		return
	}
	sum, haveSum, sumErr := store.LastSync()
	library := ""
	if cfg := a.Config(); cfg != nil {
		library = cfg.Library
	}
	count, countErr := store.BookCount(library)
	a.UpdateStats(func(st *mainStats) {
		if sumErr == nil && haveSum {
			st.lastSync = sum
			st.hasLastSync = true
		}
		if countErr == nil {
			st.bookCount = count
		}
	})
}

// saveConfigChange applies fn to the active config and persists the
// result. The edit lands on a copy that then replaces the published
// pointer, so a goroutine holding the old config keeps a consistent view
// instead of seeing a half-applied change. The new config is returned
// for callers that redraw the setting they just changed, and is nil when
// no profile is active.
func (a *app) saveConfigChange(fn func(*Config)) *Config {
	cfg := a.UpdateConfig(fn)
	if cfg == nil {
		return nil
	}
	if err := SaveConfig(a.cfgPath, cfg); err != nil {
		log.Printf("save config: %v", err)
	}
	return cfg
}

func (a *app) syncActive() bool {
	a.sync.mu.Lock()
	defer a.sync.mu.Unlock()
	return a.sync.active
}
