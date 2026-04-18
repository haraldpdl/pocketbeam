package main

import (
	"context"
	"fmt"
	"image"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	ink "github.com/dennwc/inkview"
)

type screen int

const (
	screenFirstRun screen = iota
	screenMain
	screenSettings
)

// wizardStep tracks where the user is in the first-run flow.
type wizardStep int

const (
	stepWelcome wizardStep = iota
	stepURL
	stepUser
	stepPass
	stepTesting
	stepError
)

// wizardState holds the in-progress first-run input and result.
type wizardState struct {
	step wizardStep
	url  string
	user string
	pass string
	err  error
}

// syncState holds live progress from a running sync. Written by the progress
// callback (goroutine), read by Draw (event loop). Protected by mu.
type syncState struct {
	mu     sync.Mutex
	active bool
	index  int
	total  int
	title  string
	author string
	err    error
}

// Button rectangles on the main and settings screens. Package-level so
// hit-test uses the same values as the draw methods.
var (
	mainSyncButton       = image.Rect(202, 520, 1202, 720)
	mainSettingsButton   = image.Rect(100, 1720, 500, 1820)
	mainQuitButton       = image.Rect(900, 1720, 1300, 1820)
	settingsChangeButton = image.Rect(202, 480, 1202, 640)
	settingsBackButton   = image.Rect(100, 1720, 500, 1820)
)

// app implements ink.App for the bookbeam device UI.
type app struct {
	cfgPath     string
	cfg         *Config
	client      *Client
	store       *Store
	screen      screen
	wizard      wizardState
	sync        syncState
	lastSync    SyncSummary
	hasLastSync bool
	bookCount   int
	netStop     func()
}

func newApp() *app {
	return &app{
		cfgPath: filepath.Join(ink.ConfigPath, "bookbeam.cfg"),
	}
}

// Init is called once when the app launches.
func (a *app) Init() error {
	_ = os.Chdir(filepath.Dir(os.Args[0]))

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
	ink.ClearScreen()
	switch a.screen {
	case screenFirstRun:
		a.drawWizard()
	case screenMain:
		a.drawMain()
	case screenSettings:
		a.drawSettings()
	}
	ink.FullUpdate()
}

func (a *app) Key(e ink.KeyEvent) bool {
	// Back key quits, except during a probe or sync.
	if e.Key == ink.KeyBack && a.wizard.step != stepTesting && !a.syncActive() {
		ink.Exit()
		return true
	}
	switch a.screen {
	case screenFirstRun:
		return a.wizardKey(e)
	case screenMain:
		return a.mainKey(e)
	case screenSettings:
		return a.settingsKey(e)
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

// ---------- First-run wizard ----------

func (a *app) drawWizard() {
	title := ink.OpenFont(ink.DefaultFontBold, 54, true)
	defer title.Close()
	title.SetActive(ink.Black)

	body := ink.OpenFont(ink.DefaultFont, 32, true)
	defer body.Close()
	body.SetActive(ink.Black)

	switch a.wizard.step {
	case stepWelcome:
		title.SetActive(ink.Black)
		ink.DrawString(image.Point{X: 80, Y: 200}, "bookbeam")
		body.SetActive(ink.Black)
		ink.DrawString(image.Point{X: 80, Y: 280}, "Wireless sync for your Calibre-Web library.")
		ink.DrawString(image.Point{X: 80, Y: 360}, "You will be asked for:")
		ink.DrawString(image.Point{X: 120, Y: 420}, "- Your server URL")
		ink.DrawString(image.Point{X: 120, Y: 470}, "- Username")
		ink.DrawString(image.Point{X: 120, Y: 520}, "- Password")
		ink.DrawString(image.Point{X: 80, Y: 620}, "Press OK or tap the screen to begin.")

	case stepTesting:
		title.SetActive(ink.Black)
		ink.DrawString(image.Point{X: 80, Y: 300}, "Testing connection...")
		body.SetActive(ink.Black)
		ink.DrawString(image.Point{X: 80, Y: 380}, a.wizard.url)
		ink.ShowHourglassAt(image.Point{X: 80, Y: 480})

	case stepError:
		title.SetActive(ink.Black)
		ink.DrawString(image.Point{X: 80, Y: 200}, "Connection failed")
		body.SetActive(ink.Black)
		msg := "Unknown error"
		if a.wizard.err != nil {
			msg = a.wizard.err.Error()
		}
		ink.DrawString(image.Point{X: 80, Y: 300}, msg)
		ink.DrawString(image.Point{X: 80, Y: 620}, "Press OK or tap to try again.")
	}
}

func (a *app) wizardKey(e ink.KeyEvent) bool {
	switch a.wizard.step {
	case stepWelcome, stepError:
		if e.Key == ink.KeyOk || e.Key == ink.KeyNext {
			a.startURLEntry()
			return true
		}
	}
	return false
}

func (a *app) wizardPointer(e ink.PointerEvent) bool {
	if e.State != ink.PointerDown {
		return false
	}
	switch a.wizard.step {
	case stepWelcome, stepError:
		a.startURLEntry()
		return true
	}
	return false
}

func (a *app) startURLEntry() {
	a.wizard.step = stepURL
	a.wizard.err = nil
	ink.OpenKeyboard("https://cwa.example.com:8083", 512)
}

// onKeyboardInput routes based on current wizard step.
func (a *app) onKeyboardInput(text string) {
	switch a.wizard.step {
	case stepURL:
		a.wizard.url = text
		a.wizard.step = stepUser
		ink.OpenKeyboard("Username", 128)
	case stepUser:
		a.wizard.user = text
		a.wizard.step = stepPass
		ink.OpenKeyboard("Password", 128)
	case stepPass:
		a.wizard.pass = text
		a.wizard.step = stepTesting
		ink.Repaint()
		go a.runProbe()
	}
}

// runProbe probes the server and transitions the UI on success or failure.
func (a *app) runProbe() {
	err := ProbeCWA(context.Background(), a.wizard.url, a.wizard.user, a.wizard.pass)
	ink.HideHourglass()
	if err != nil {
		a.wizard.err = err
		a.wizard.step = stepError
		ink.Repaint()
		return
	}
	cfg := &Config{
		Host:    a.wizard.url,
		User:    a.wizard.user,
		Pass:    a.wizard.pass,
		Library: filepath.Join(ink.FlashDir, "Books", "CWA"),
		StateDB: filepath.Join(ink.ConfigPath, "bookbeam.db"),
	}
	if err := SaveConfig(a.cfgPath, cfg); err != nil {
		a.wizard.err = err
		a.wizard.step = stepError
		ink.Repaint()
		return
	}
	client, err := NewClient(cfg.Host, cfg.User, cfg.Pass)
	if err != nil {
		a.wizard.err = err
		a.wizard.step = stepError
		ink.Repaint()
		return
	}
	store, err := OpenStore(cfg.StateDB)
	if err != nil {
		a.wizard.err = err
		a.wizard.step = stepError
		ink.Repaint()
		return
	}
	// Close any existing store handle before swapping (happens on settings → edit).
	if a.store != nil {
		_ = a.store.Close()
	}
	a.cfg = cfg
	a.client = client
	a.store = store
	a.refreshMainStats()
	a.screen = screenMain
	a.wizard = wizardState{}
	ink.Repaint()
}

// ---------- Main sync screen ----------

func (a *app) drawMain() {
	title := ink.OpenFont(ink.DefaultFontBold, 64, true)
	defer title.Close()
	title.SetActive(ink.Black)

	body := ink.OpenFont(ink.DefaultFont, 32, true)
	defer body.Close()
	body.SetActive(ink.Black)

	btnFont := ink.OpenFont(ink.DefaultFontBold, 44, true)
	defer btnFont.Close()
	btnFont.SetActive(ink.Black)

	// Header
	title.SetActive(ink.Black)
	ink.DrawString(image.Point{X: 80, Y: 140}, "bookbeam")

	// Connection info
	body.SetActive(ink.Black)
	ink.DrawString(image.Point{X: 80, Y: 240}, a.cfg.Host)

	// Last-sync summary
	body.SetActive(ink.Black)
	if a.hasLastSync {
		ink.DrawString(image.Point{X: 80, Y: 340}, fmt.Sprintf("Last synced: %s", humanAgo(a.lastSync.At)))
		ink.DrawString(image.Point{X: 80, Y: 390}, fmt.Sprintf("%d books in library", a.bookCount))
		if a.lastSync.Failed > 0 {
			ink.DrawString(image.Point{X: 80, Y: 440}, fmt.Sprintf("%d failed (will retry next sync)", a.lastSync.Failed))
		}
	} else {
		ink.DrawString(image.Point{X: 80, Y: 340}, "Not yet synced. Tap Sync Now to begin.")
	}

	// Sync Now button
	ink.DrawRect(mainSyncButton, ink.Black)
	ink.DrawRect(mainSyncButton.Inset(2), ink.Black)
	btnFont.SetActive(ink.Black)
	ink.DrawString(image.Point{X: 560, Y: 640}, "Sync Now")

	// Live progress (during sync)
	a.sync.mu.Lock()
	active := a.sync.active
	idx := a.sync.index
	total := a.sync.total
	curTitle := a.sync.title
	curAuthor := a.sync.author
	syncErr := a.sync.err
	a.sync.mu.Unlock()

	if active {
		body.SetActive(ink.Black)
		ink.DrawString(image.Point{X: 80, Y: 820}, fmt.Sprintf("%d / %d", idx, total))
		// Progress bar
		barOuter := image.Rect(80, 870, 1320, 920)
		ink.DrawRect(barOuter, ink.Black)
		if total > 0 {
			fillW := (barOuter.Dx() - 6) * idx / total
			ink.FillArea(image.Rect(barOuter.Min.X+3, barOuter.Min.Y+3, barOuter.Min.X+3+fillW, barOuter.Max.Y-3), ink.DarkGray)
		}
		ink.DrawString(image.Point{X: 80, Y: 970}, truncate(curAuthor+": "+curTitle, 60))
	} else if syncErr != nil {
		body.SetActive(ink.Black)
		ink.DrawString(image.Point{X: 80, Y: 820}, "Last error:")
		ink.DrawString(image.Point{X: 80, Y: 870}, truncate(syncErr.Error(), 60))
	}

	// Bottom buttons
	ink.DrawRect(mainSettingsButton, ink.Black)
	ink.DrawRect(mainQuitButton, ink.Black)
	btnFont.SetActive(ink.Black)
	ink.DrawString(image.Point{X: 180, Y: 1785}, "Settings")
	ink.DrawString(image.Point{X: 1050, Y: 1785}, "Quit")
}

func (a *app) mainKey(e ink.KeyEvent) bool {
	if a.syncActive() {
		return false // ignore during sync
	}
	switch e.Key {
	case ink.KeyOk:
		a.startSync()
		return true
	case ink.KeyMenu:
		a.screen = screenSettings
		ink.Repaint()
		return true
	}
	return false
}

func (a *app) mainPointer(e ink.PointerEvent) bool {
	if e.State != ink.PointerDown {
		return false
	}
	if a.syncActive() {
		return false
	}
	p := e.Point
	switch {
	case p.In(mainSyncButton):
		a.startSync()
		return true
	case p.In(mainSettingsButton):
		a.screen = screenSettings
		ink.Repaint()
		return true
	case p.In(mainQuitButton):
		ink.Exit()
		return true
	}
	return false
}

// startSync kicks off a sync in a goroutine and wires progress + completion
// back to the UI via ink.Repaint.
func (a *app) startSync() {
	a.sync.mu.Lock()
	if a.sync.active {
		a.sync.mu.Unlock()
		return
	}
	a.sync.active = true
	a.sync.index = 0
	a.sync.total = 0
	a.sync.title = ""
	a.sync.author = ""
	a.sync.err = nil
	a.sync.mu.Unlock()
	ink.Repaint()

	go a.runSync()
}

func (a *app) runSync() {
	progress := func(i, total int, b Book) {
		a.sync.mu.Lock()
		a.sync.index = i
		a.sync.total = total
		a.sync.title = b.Title
		a.sync.author = b.Author
		a.sync.mu.Unlock()
		ink.Repaint()
	}
	dl, skip, fail, firstErr := Sync(a.client, a.store, a.cfg.Library, progress)

	a.sync.mu.Lock()
	a.sync.active = false
	a.sync.err = firstErr
	a.sync.mu.Unlock()

	// Persist summary + refresh screen stats.
	_ = a.store.SetLastSync(SyncSummary{
		At:         time.Now(),
		Downloaded: dl,
		Skipped:    skip,
		Failed:     fail,
	})
	a.refreshMainStats()
	ink.Repaint()
}

// truncate shortens s to at most n runes, adding "..." if cut.
func truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 3 {
		return string(r[:n])
	}
	return string(r[:n-3]) + "..."
}

// ---------- Settings screen ----------

func (a *app) drawSettings() {
	title := ink.OpenFont(ink.DefaultFontBold, 64, true)
	defer title.Close()
	title.SetActive(ink.Black)

	body := ink.OpenFont(ink.DefaultFont, 32, true)
	defer body.Close()
	body.SetActive(ink.Black)

	btnFont := ink.OpenFont(ink.DefaultFontBold, 44, true)
	defer btnFont.Close()
	btnFont.SetActive(ink.Black)

	small := ink.OpenFont(ink.DefaultFont, 26, true)
	defer small.Close()
	small.SetActive(ink.Black)

	// Header
	title.SetActive(ink.Black)
	ink.DrawString(image.Point{X: 80, Y: 140}, "Settings")

	// Current config
	body.SetActive(ink.Black)
	ink.DrawString(image.Point{X: 80, Y: 260}, "Server: "+a.cfg.Host)
	ink.DrawString(image.Point{X: 80, Y: 320}, "User:   "+a.cfg.User)

	// Change-info button
	ink.DrawRect(settingsChangeButton, ink.Black)
	ink.DrawRect(settingsChangeButton.Inset(2), ink.Black)
	btnFont.SetActive(ink.Black)
	ink.DrawString(image.Point{X: 450, Y: 580}, "Change server info")

	// Footer: version + Back
	small.SetActive(ink.Black)
	ink.DrawString(image.Point{X: 80, Y: 1700}, "bookbeam "+version)

	ink.DrawRect(settingsBackButton, ink.Black)
	btnFont.SetActive(ink.Black)
	ink.DrawString(image.Point{X: 230, Y: 1785}, "Back")
}

func (a *app) settingsKey(e ink.KeyEvent) bool {
	switch e.Key {
	case ink.KeyBack:
		a.screen = screenMain
		ink.Repaint()
		return true
	case ink.KeyOk:
		a.startChangeInfo()
		return true
	}
	return false
}

func (a *app) settingsPointer(e ink.PointerEvent) bool {
	if e.State != ink.PointerDown {
		return false
	}
	p := e.Point
	switch {
	case p.In(settingsChangeButton):
		a.startChangeInfo()
		return true
	case p.In(settingsBackButton):
		a.screen = screenMain
		ink.Repaint()
		return true
	}
	return false
}

// startChangeInfo re-enters the wizard to replace server URL, username, and
// password. On success, the existing store and client are closed and swapped
// for new ones against the updated config.
func (a *app) startChangeInfo() {
	a.wizard.step = stepURL
	a.wizard.err = nil
	a.wizard.url = ""
	a.wizard.user = ""
	a.wizard.pass = ""
	a.screen = screenFirstRun
	ink.OpenKeyboard("https://cwa.example.com:8083", 512)
}
