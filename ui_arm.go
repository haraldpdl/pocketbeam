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
	screenShelfPicker
	screenDirPicker
)

// wizardStep tracks where the user is in the first-run flow.
type wizardStep int

const (
	stepWelcome wizardStep = iota
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
	err     error
	// Tap targets for the backend-choice step, captured during draw so the
	// pointer handler knows where the two buttons live.
	opdsBtn   image.Rectangle
	webdavBtn image.Rectangle
}

// syncState holds live progress from a running sync. Written by the progress
// callback (goroutine), read by Draw (event loop). Protected by mu.
type syncState struct {
	mu        sync.Mutex
	active    bool
	index     int
	total     int
	title     string
	author    string
	err       error
	bookStart time.Time // when the current book's download began
}

// feedPickerFrame is one level of the nested-navigation stack. Pushed when
// the user drills into a subsection, popped on "..".
type feedPickerFrame struct {
	Href  string
	Title string
}

// feedPickerState holds the current OPDS feed the user is browsing. The
// navigation stack lets the user walk back up to any ancestor.
type feedPickerState struct {
	mu         sync.Mutex
	loading    bool
	href       string
	title      string
	stack      []feedPickerFrame
	level      OPDSLevel
	err        error
	rowRects   []image.Rectangle // per visible subsection row
	selectRect image.Rectangle   // "Sync this level" button
	upRect     image.Rectangle   // ".. (up)" button, empty when at root
}

// dirPickerState holds the current WebDAV directory the user is drilling
// through. path is the currently-browsed directory (always absolute, always
// starts with "/"); dirs is the list of subdirectories to display.
type dirPickerState struct {
	mu         sync.Mutex
	loading    bool
	path       string
	dirs       []string
	err        error
	rowRects   []image.Rectangle // one per visible row (index 0 = "..", 1+ = dirs)
	selectRect image.Rectangle   // "Sync this folder" button
}

// layout holds screen-relative rectangles for every clickable element and for
// the progress strip. Computed once in Init from the actual ScreenSize so the
// UI adapts to whatever resolution the device reports.
type layout struct {
	screen image.Point
	margin int

	// main screen
	syncButton     image.Rectangle
	networkButton  image.Rectangle
	settingsButton image.Rectangle
	quitButton     image.Rectangle
	progressArea   image.Rectangle // the strip refreshed via PartialUpdate
	progressBar    image.Rectangle

	// settings screen
	changeButton image.Rectangle
	filterButton image.Rectangle
	backButton   image.Rectangle

	// shelf picker: per-row tap rects computed dynamically in draw.
	pickerAreaTop    int
	pickerAreaBottom int
}

// connectivity records the last known network state, derived from the outcome
// of the most recent probe or sync attempt.
type connectivity int

const (
	connUnknown connectivity = iota
	connOnline
	connOffline
)

// computeLayout lays out the UI relative to the given screen size. Positions
// are derived from the usable area after stripping generous safety margins
// that guard against a PocketBook status bar at top and nav bar at bottom
// (ScreenSize does not account for either).
func computeLayout(sz image.Point) layout {
	w, h := sz.X, sz.Y
	sideMargin := 60
	topSafe := 100
	bottomSafe := 180
	if w > 0 && w < 1200 {
		sideMargin = 40
	}
	contentW := w - 2*sideMargin

	// Main Sync Now button: centered, below the last-sync summary area.
	syncY1 := topSafe + 420
	syncBtn := image.Rect(sideMargin, syncY1, sideMargin+contentW, syncY1+200)

	// Progress strip: fixed position below the Sync button, above the bottom
	// row. Covers counter + bar + current-book line.
	progY1 := syncBtn.Max.Y + 60
	progArea := image.Rect(sideMargin, progY1, sideMargin+contentW, progY1+240)
	progBar := image.Rect(sideMargin, progY1+80, sideMargin+contentW, progY1+130)

	// Bottom button row: Network | Settings | Quit, anchored from the bottom.
	btnH := 100
	btnY2 := h - bottomSafe
	btnY1 := btnY2 - btnH
	btnW := (contentW - 2*40) / 3 // 3 buttons with two 40-px gaps between them
	networkBtn := image.Rect(sideMargin, btnY1, sideMargin+btnW, btnY2)
	settingsBtn := image.Rect(networkBtn.Max.X+40, btnY1, networkBtn.Max.X+40+btnW, btnY2)
	quitBtn := image.Rect(w-sideMargin-btnW, btnY1, w-sideMargin, btnY2)

	// Settings screen: two stacked big buttons (change server info, change
	// filter); Back in the bottom-left mirroring the main screen.
	changeY1 := topSafe + 320
	changeBtn := image.Rect(sideMargin, changeY1, sideMargin+contentW, changeY1+160)
	filterY1 := changeY1 + 200
	filterBtn := image.Rect(sideMargin, filterY1, sideMargin+contentW, filterY1+160)

	// Shelf picker: rows live between the header (below topSafe) and the
	// Back button (same position as bottom btnY1).
	pickerTop := topSafe + 220
	pickerBottom := btnY1 - 40

	return layout{
		screen:           sz,
		margin:           sideMargin,
		syncButton:       syncBtn,
		networkButton:    networkBtn,
		settingsButton:   settingsBtn,
		quitButton:       quitBtn,
		progressArea:     progArea,
		progressBar:      progBar,
		changeButton:     changeBtn,
		filterButton:     filterBtn,
		backButton:       networkBtn,
		pickerAreaTop:    pickerTop,
		pickerAreaBottom: pickerBottom,
	}
}

// app implements ink.App for the bookbeam device UI.
type app struct {
	cfgPath     string
	cfg         *Config
	client      *Client
	store       *Store
	screen      screen
	wizard      wizardState
	sync        syncState
	picker      feedPickerState
	dirPicker   dirPickerState
	lastSync    SyncSummary
	hasLastSync bool
	bookCount   int
	netStop     func()
	layout      layout
	connState   connectivity
}

func newApp() *app {
	return &app{
		cfgPath: filepath.Join(ink.ConfigPath, "bookbeam.cfg"),
	}
}

// Init is called once when the app launches.
func (a *app) Init() error {
	_ = os.Chdir(filepath.Dir(os.Args[0]))

	a.layout = computeLayout(ink.ScreenSize())

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
	case screenShelfPicker:
		a.drawShelfPicker()
	case screenDirPicker:
		a.drawDirPicker()
	}
	ink.FullUpdate()
}

func (a *app) Key(e ink.KeyEvent) bool {
	// Back key behaviour: return to previous screen from settings/picker,
	// quit from first-run welcome/error or from main (if idle).
	if e.Key == ink.KeyBack && a.wizard.step != stepTesting && !a.syncActive() {
		switch a.screen {
		case screenSettings:
			a.screen = screenMain
			ink.Repaint()
			return true
		case screenShelfPicker, screenDirPicker:
			a.screen = screenSettings
			ink.Repaint()
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
		ink.DrawString(image.Point{X: 80, Y: 280}, "Wireless sync from a book server.")
		ink.DrawString(image.Point{X: 80, Y: 360}, "Supports:")
		ink.DrawString(image.Point{X: 120, Y: 420}, "- Calibre-Web / any OPDS server")
		ink.DrawString(image.Point{X: 120, Y: 470}, "- WebDAV (Nextcloud, Synology, ownCloud)")
		ink.DrawString(image.Point{X: 80, Y: 620}, "Press OK or tap the screen to begin.")

	case stepBackend:
		title.SetActive(ink.Black)
		ink.DrawString(image.Point{X: 80, Y: 200}, "Choose server type")
		body.SetActive(ink.Black)
		ink.DrawString(image.Point{X: 80, Y: 290}, "Tap the option that matches your server.")

		btnFont := ink.OpenFont(ink.DefaultFontBold, 44, true)
		defer btnFont.Close()
		btnFont.SetActive(ink.Black)

		w := a.layout.screen.X
		btnW := w - 2*a.layout.margin
		a.wizard.opdsBtn = image.Rect(a.layout.margin, 380, a.layout.margin+btnW, 540)
		a.wizard.webdavBtn = image.Rect(a.layout.margin, 580, a.layout.margin+btnW, 740)

		ink.DrawRect(a.wizard.opdsBtn, ink.Black)
		ink.DrawRect(a.wizard.opdsBtn.Inset(2), ink.Black)
		drawCenteredText(btnFont, a.wizard.opdsBtn, "Calibre-Web / OPDS", 44)

		ink.DrawRect(a.wizard.webdavBtn, ink.Black)
		ink.DrawRect(a.wizard.webdavBtn.Inset(2), ink.Black)
		drawCenteredText(btnFont, a.wizard.webdavBtn, "WebDAV / Nextcloud", 44)

	case stepURL, stepUser, stepPass:
		title.SetActive(ink.Black)
		ink.DrawString(image.Point{X: 80, Y: 200}, "bookbeam setup")
		body.SetActive(ink.Black)
		var prompt string
		switch a.wizard.step {
		case stepURL:
			prompt = "Step 1 of 3: enter your server URL"
		case stepUser:
			prompt = "Step 2 of 3: enter your username"
		case stepPass:
			prompt = "Step 3 of 3: enter your password"
		}
		ink.DrawString(image.Point{X: 80, Y: 300}, prompt)
		ink.DrawString(image.Point{X: 80, Y: 360}, "Tap the screen or press OK if the keyboard is not visible.")
		y := 500
		if a.wizard.url != "" {
			ink.DrawString(image.Point{X: 80, Y: y}, "Server: "+a.wizard.url)
			y += 50
		}
		if a.wizard.user != "" {
			ink.DrawString(image.Point{X: 80, Y: y}, "User: "+a.wizard.user)
		}

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
	case stepWelcome:
		if e.Key == ink.KeyOk || e.Key == ink.KeyNext {
			a.wizard.step = stepBackend
			ink.Repaint()
			return true
		}
	case stepBackend:
		// Two options; OK defaults to OPDS (the more common setup), Next
		// jumps to WebDAV. Users can also tap the button they want.
		if e.Key == ink.KeyOk {
			a.pickBackend(BackendOPDS)
			return true
		}
		if e.Key == ink.KeyNext {
			a.pickBackend(BackendWebDAV)
			return true
		}
	case stepError:
		if e.Key == ink.KeyOk || e.Key == ink.KeyNext {
			a.startURLEntry()
			return true
		}
	case stepURL, stepUser, stepPass:
		if e.Key == ink.KeyOk {
			a.reopenKeyboardForStep()
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
	case stepWelcome:
		a.wizard.step = stepBackend
		ink.Repaint()
		return true
	case stepBackend:
		if e.Point.In(a.wizard.opdsBtn) {
			a.pickBackend(BackendOPDS)
			return true
		}
		if e.Point.In(a.wizard.webdavBtn) {
			a.pickBackend(BackendWebDAV)
			return true
		}
	case stepError:
		a.startURLEntry()
		return true
	case stepURL, stepUser, stepPass:
		a.reopenKeyboardForStep()
		return true
	}
	return false
}

// pickBackend records the chosen backend on the wizard state and advances
// to the URL entry step. OPDS suggests a Calibre-Web URL; WebDAV suggests
// a Nextcloud-style URL to nudge the user into the right format.
func (a *app) pickBackend(b string) {
	a.wizard.backend = b
	a.startURLEntry()
}

// reopenKeyboardForStep pops the right keyboard for the current wizard step.
// Used when OpenKeyboard called directly from a keyboard handler races and
// the chained keyboard does not actually appear, so the user taps the screen
// or presses OK to request it explicitly.
func (a *app) reopenKeyboardForStep() {
	switch a.wizard.step {
	case stepURL:
		ink.OpenKeyboard(a.urlHint(), 512)
	case stepUser:
		ink.OpenKeyboard("Username", 128)
	case stepPass:
		ink.OpenKeyboard("Password", 128)
	}
}

func (a *app) startURLEntry() {
	a.wizard.step = stepURL
	a.wizard.err = nil
	ink.OpenKeyboard(a.urlHint(), 512)
}

func (a *app) urlHint() string {
	if a.wizard.backend == BackendWebDAV {
		return "https://nc.example.com/remote.php/dav/files/alice"
	}
	return "https://library.example.com:8083"
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
	backend := a.wizard.backend
	if backend == "" {
		backend = BackendOPDS
	}
	var err error
	switch backend {
	case BackendWebDAV:
		err = ProbeWebDAV(context.Background(), a.wizard.url, a.wizard.user, a.wizard.pass, "/")
	default:
		err = ProbeCWA(context.Background(), a.wizard.url, a.wizard.user, a.wizard.pass)
	}
	ink.HideHourglass()
	if err != nil {
		a.wizard.err = err
		a.wizard.step = stepError
		ink.Repaint()
		return
	}
	libraryDir := "CWA"
	if backend == BackendWebDAV {
		libraryDir = "WebDAV"
	}
	cfg := &Config{
		Backend: backend,
		Host:    a.wizard.url,
		User:    a.wizard.user,
		Pass:    a.wizard.pass,
		Library: filepath.Join(ink.FlashDir, "Books", libraryDir),
		StateDB: filepath.Join(ink.ConfigPath, "bookbeam.db"),
	}
	if backend == BackendWebDAV {
		cfg.Path = "/"
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
	ink.DrawString(image.Point{X: a.layout.margin, Y: 120}, "bookbeam")

	// Connection info + filter + status stacked tightly near the top
	body.SetActive(ink.Black)
	ink.DrawString(image.Point{X: a.layout.margin, Y: 210}, "Server: "+a.cfg.Host)

	filterText := "Filter: all books"
	if a.cfg.FilterHref != "" {
		name := a.cfg.FilterName
		if name == "" {
			name = a.cfg.FilterHref
		}
		filterText = "Filter: " + name
	}
	ink.DrawString(image.Point{X: a.layout.margin, Y: 255}, filterText)

	var connText string
	switch a.connState {
	case connOnline:
		connText = "Status: connected"
	case connOffline:
		connText = "Status: offline"
	default:
		connText = "Status: not yet tested"
	}
	ink.DrawString(image.Point{X: a.layout.margin, Y: 300}, connText)

	// Last-sync summary
	if a.hasLastSync {
		ink.DrawString(image.Point{X: a.layout.margin, Y: 360}, fmt.Sprintf("Last synced: %s", humanAgo(a.lastSync.At)))
		ink.DrawString(image.Point{X: a.layout.margin, Y: 405}, fmt.Sprintf("%d books in library", a.bookCount))
		if a.lastSync.Failed > 0 {
			ink.DrawString(image.Point{X: a.layout.margin, Y: 450}, fmt.Sprintf("%d failed (will retry next sync)", a.lastSync.Failed))
		}
	} else {
		ink.DrawString(image.Point{X: a.layout.margin, Y: 360}, "Not yet synced. Tap Sync Now to begin.")
	}

	// Sync Now button
	ink.DrawRect(a.layout.syncButton, ink.Black)
	ink.DrawRect(a.layout.syncButton.Inset(2), ink.Black)
	btnFont.SetActive(ink.Black)
	drawCenteredText(btnFont, a.layout.syncButton, "Sync Now", 44)

	// Live progress area (drawn fully here on idle-to-sync transition; during
	// the sync it is refreshed in-place via drawMainProgress + PartialUpdate).
	a.drawMainProgressContent(body)

	// Bottom buttons: Network, Settings, Quit
	ink.DrawRect(a.layout.networkButton, ink.Black)
	ink.DrawRect(a.layout.settingsButton, ink.Black)
	ink.DrawRect(a.layout.quitButton, ink.Black)
	btnFont.SetActive(ink.Black)
	drawCenteredText(btnFont, a.layout.networkButton, "Network", 44)
	drawCenteredText(btnFont, a.layout.settingsButton, "Settings", 44)
	drawCenteredText(btnFont, a.layout.quitButton, "Quit", 44)
}

// drawMainProgressContent renders the progress strip (counter, bar, current
// book line) without touching the rest of the main screen. Caller must have
// the appropriate fonts set up; we do not own them here.
func (a *app) drawMainProgressContent(body *ink.Font) {
	a.sync.mu.Lock()
	active := a.sync.active
	idx := a.sync.index
	total := a.sync.total
	curTitle := a.sync.title
	curAuthor := a.sync.author
	syncErr := a.sync.err
	bookStart := a.sync.bookStart
	a.sync.mu.Unlock()

	ink.FillArea(a.layout.progressArea, ink.White)

	if active {
		body.SetActive(ink.Black)
		counter := fmt.Sprintf("%d / %d", idx, total)
		if !bookStart.IsZero() {
			counter += "  (" + formatElapsed(time.Since(bookStart)) + ")"
		}
		ink.DrawString(image.Point{X: 80, Y: a.layout.progressArea.Min.Y + 30}, counter)
		ink.DrawRect(a.layout.progressBar, ink.Black)
		if total > 0 {
			fillW := (a.layout.progressBar.Dx() - 6) * idx / total
			ink.FillArea(image.Rect(
				a.layout.progressBar.Min.X+3,
				a.layout.progressBar.Min.Y+3,
				a.layout.progressBar.Min.X+3+fillW,
				a.layout.progressBar.Max.Y-3,
			), ink.DarkGray)
		}
		ink.DrawString(image.Point{X: 80, Y: a.layout.progressArea.Min.Y + 180}, truncate(curAuthor+": "+curTitle, 60))
	} else if syncErr != nil {
		body.SetActive(ink.Black)
		ink.DrawString(image.Point{X: 80, Y: a.layout.progressArea.Min.Y + 30}, "Last error:")
		ink.DrawString(image.Point{X: 80, Y: a.layout.progressArea.Min.Y + 80}, truncate(syncErr.Error(), 60))
	}
}

// refreshProgress redraws only the progress strip and pushes it with a
// partial e-ink update, so the rest of the main screen stays stable.
func (a *app) refreshProgress() {
	body := ink.OpenFont(ink.DefaultFont, 32, true)
	defer body.Close()
	a.drawMainProgressContent(body)
	ink.PartialUpdate(a.layout.progressArea)
}

// drawCenteredText writes s centered inside rect using the given font. fontPx
// is the font's pixel height (used for vertical centering since DrawString
// interprets Y as the top-left corner).
func drawCenteredText(f *ink.Font, rect image.Rectangle, s string, fontPx int) {
	w := ink.StringWidth(s)
	x := rect.Min.X + (rect.Dx()-w)/2
	y := rect.Min.Y + (rect.Dy()-fontPx)/2
	ink.DrawString(image.Point{X: x, Y: y}, s)
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
	case p.In(a.layout.syncButton):
		a.startSync()
		return true
	case p.In(a.layout.networkButton):
		ink.OpenNetworkInfo()
		return true
	case p.In(a.layout.settingsButton):
		a.screen = screenSettings
		ink.Repaint()
		return true
	case p.In(a.layout.quitButton):
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
	// Prevent the device from going to standby mid-sync. PocketBook's power
	// manager will otherwise suspend the CPU / drop Wi-Fi after the usual
	// idle timeout even though bookbeam is actively downloading. Restore
	// normal behaviour when the sync finishes (including on early return).
	ink.SetSleepMode(false)
	ink.SetAutoPowerOff(false)
	defer func() {
		ink.SetSleepMode(true)
		ink.SetAutoPowerOff(true)
	}()

	if err := a.ensureConnected(); err != nil {
		a.finishSyncWithError(err)
		ink.Repaint()
		return
	}

	// Tick the progress strip once a second so the elapsed-time counter
	// advances even while a single (large) book download is streaming.
	tickerDone := make(chan struct{})
	go a.progressTicker(tickerDone)
	defer close(tickerDone)

	progress := func(i, total int, b Book) {
		a.sync.mu.Lock()
		a.sync.index = i
		a.sync.total = total
		a.sync.title = b.Title
		a.sync.author = b.Author
		a.sync.bookStart = time.Now()
		a.sync.mu.Unlock()
		a.refreshProgress()
	}
	src, err := newSource(a.cfg)
	if err != nil {
		a.sync.mu.Lock()
		a.sync.active = false
		a.sync.err = err
		a.sync.mu.Unlock()
		a.refreshMainStats()
		ink.Repaint()
		return
	}
	dl, skip, fail, firstErr := Sync(src, a.store, a.cfg.Library, progress)

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

	// We deliberately do NOT auto-exec /mnt/ext1/system/bin/scanner.app here:
	// on PocketBook firmware 6.x it takes foreground focus, which makes
	// bookbeam appear frozen until the scan finishes. The Library app
	// rescans on its own the next time you open it, so new covers and
	// titles show up after a normal navigation back to the library.
}

// finishSyncWithError sets the sync state to inactive with the given error.
// Used when the sync bails before calling Sync() (no network, probe fail).
func (a *app) finishSyncWithError(err error) {
	a.sync.mu.Lock()
	a.sync.active = false
	a.sync.err = err
	a.sync.mu.Unlock()
}

// ensureConnected wakes the Wi-Fi and verifies the server is reachable with
// the configured credentials. Updates a.connState based on the outcome and
// refreshes the client's server-type detection so the next WalkAll picks the
// right path (CWA fast path vs generic recursive walk). Returns a user-facing
// error on failure; nil on success. All UI actions that issue HTTP requests
// to the server should call this first so the user gets a consistent,
// readable error instead of a raw Go transport dump.
func (a *app) ensureConnected() error {
	if err := ink.ConnectDefault(); err != nil {
		a.connState = connOffline
		return fmt.Errorf("No Wi-Fi connection. Open Network to configure.")
	}
	var err error
	switch a.cfg.Backend {
	case BackendWebDAV:
		err = ProbeWebDAV(context.Background(), a.cfg.Host, a.cfg.User, a.cfg.Pass, a.cfg.Path)
	default:
		err = ProbeCWA(context.Background(), a.cfg.Host, a.cfg.User, a.cfg.Pass)
	}
	if err != nil {
		a.connState = connOffline
		return err
	}
	if a.cfg.Backend != BackendWebDAV && a.client != nil {
		_ = a.client.DetectType() // non-fatal: falls back to generic walk
	}
	a.connState = connOnline
	return nil
}

// progressTicker refreshes the progress strip once a second while the sync
// is active, so the elapsed-time counter visibly advances even when the
// book counter / progress bar does not (e.g. a single large download).
func (a *app) progressTicker(done <-chan struct{}) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-done:
			return
		case <-t.C:
			if !a.syncActive() {
				return
			}
			a.refreshProgress()
		}
	}
}

// formatElapsed renders a duration as "m:ss" or "h:mm:ss" for display next
// to the in-progress book counter.
func formatElapsed(d time.Duration) string {
	s := int(d.Seconds())
	if s < 3600 {
		return fmt.Sprintf("%d:%02d", s/60, s%60)
	}
	return fmt.Sprintf("%d:%02d:%02d", s/3600, (s%3600)/60, s%60)
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

	// Filter / folder status line above the buttons
	body.SetActive(ink.Black)
	var filterLabel string
	if a.cfg.Backend == BackendWebDAV {
		p := a.cfg.Path
		if p == "" {
			p = "/"
		}
		filterLabel = "Folder: " + p
	} else {
		filterLabel = "Filter: All books"
		if a.cfg.FilterHref != "" {
			name := a.cfg.FilterName
			if name == "" {
				name = a.cfg.FilterHref
			}
			filterLabel = "Filter: " + name
		}
	}
	ink.DrawString(image.Point{X: a.layout.margin, Y: 400}, filterLabel)

	// Change-info button
	ink.DrawRect(a.layout.changeButton, ink.Black)
	ink.DrawRect(a.layout.changeButton.Inset(2), ink.Black)
	btnFont.SetActive(ink.Black)
	drawCenteredText(btnFont, a.layout.changeButton, "Change server info", 44)

	// Change-filter / folder button (label varies by backend).
	filterBtnText := "Change sync filter"
	if a.cfg.Backend == BackendWebDAV {
		filterBtnText = "Change sync folder"
	}
	ink.DrawRect(a.layout.filterButton, ink.Black)
	ink.DrawRect(a.layout.filterButton.Inset(2), ink.Black)
	drawCenteredText(btnFont, a.layout.filterButton, filterBtnText, 44)

	// Footer: version + Back
	small.SetActive(ink.Black)
	ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.backButton.Min.Y - 40}, "bookbeam "+version)

	ink.DrawRect(a.layout.backButton, ink.Black)
	btnFont.SetActive(ink.Black)
	drawCenteredText(btnFont, a.layout.backButton, "Back", 44)
}

func (a *app) settingsKey(e ink.KeyEvent) bool {
	if e.Key == ink.KeyOk {
		a.startChangeInfo()
		return true
	}
	if e.Key == ink.KeyNext {
		if a.cfg.Backend == BackendWebDAV {
			a.openDirPicker(a.cfg.Path)
		} else {
			a.openShelfPicker()
		}
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
	case p.In(a.layout.changeButton):
		a.startChangeInfo()
		return true
	case p.In(a.layout.filterButton):
		if a.cfg.Backend == BackendWebDAV {
			a.openDirPicker(a.cfg.Path)
		} else {
			a.openShelfPicker()
		}
		return true
	case p.In(a.layout.backButton):
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
	ink.OpenKeyboard("https://library.example.com:8083", 512)
}

// ---------- OPDS feed picker (nested) ----------

// openShelfPicker transitions to the picker screen and fetches the root
// OPDS feed. The name is historical; the picker now walks the full feed
// tree, not just shelves.
func (a *app) openShelfPicker() {
	a.picker.mu.Lock()
	a.picker.stack = nil
	a.picker.href = "/opds"
	a.picker.title = "All books"
	a.picker.loading = true
	a.picker.level = OPDSLevel{}
	a.picker.err = nil
	a.picker.rowRects = nil
	a.picker.mu.Unlock()
	a.screen = screenShelfPicker
	ink.Repaint()
	go a.fetchFeedLevel("/opds", "All books")
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
	a.picker.mu.Unlock()
	ink.Repaint()
	go a.fetchFeedLevel(href, title)
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
	a.picker.mu.Unlock()
	ink.Repaint()
	go a.fetchFeedLevel(top.Href, top.Title)
}

func (a *app) fetchFeedLevel(href, title string) {
	if err := a.ensureConnected(); err != nil {
		a.picker.mu.Lock()
		a.picker.loading = false
		a.picker.err = err
		a.picker.mu.Unlock()
		ink.Repaint()
		return
	}
	lvl, err := a.client.FetchLevel(href)
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
	title := ink.OpenFont(ink.DefaultFontBold, 64, true)
	defer title.Close()
	title.SetActive(ink.Black)
	ink.DrawString(image.Point{X: a.layout.margin, Y: 140}, "Select filter")

	body := ink.OpenFont(ink.DefaultFont, 32, true)
	defer body.Close()
	body.SetActive(ink.Black)

	btnFont := ink.OpenFont(ink.DefaultFontBold, 44, true)
	defer btnFont.Close()
	btnFont.SetActive(ink.Black)

	a.picker.mu.Lock()
	loading := a.picker.loading
	lvl := a.picker.level
	pickerErr := a.picker.err
	stackLen := len(a.picker.stack)
	curTitle := a.picker.title
	a.picker.mu.Unlock()

	// Breadcrumb / current-level label.
	body.SetActive(ink.Black)
	crumb := "Currently in: " + truncate(curTitle, 50)
	if stackLen > 0 {
		crumb += fmt.Sprintf("  (%d up)", stackLen)
	}
	ink.DrawString(image.Point{X: a.layout.margin, Y: 220}, crumb)

	// Reserve space at bottom for "Sync this level" + Back.
	selectBtnH := 100
	areaTop := a.layout.pickerAreaTop
	areaBottom := a.layout.pickerAreaBottom - selectBtnH - 40

	if loading {
		ink.DrawString(image.Point{X: a.layout.margin, Y: 300}, "Loading...")
		ink.ShowHourglassAt(image.Point{X: a.layout.margin, Y: 360})
	} else if pickerErr != nil {
		ink.DrawString(image.Point{X: a.layout.margin, Y: 300}, "Could not load feed:")
		ink.DrawString(image.Point{X: a.layout.margin, Y: 350}, truncate(pickerErr.Error(), 60))
	} else {
		rowH := 90
		atRoot := stackLen == 0
		rows := make([]string, 0, len(lvl.Subsections)+1)
		if !atRoot {
			rows = append(rows, ".. (up)")
		}
		for _, sub := range lvl.Subsections {
			rows = append(rows, sub.Name)
		}

		availableH := areaBottom - areaTop
		maxRows := availableH / rowH
		visible := len(rows)
		if visible > maxRows {
			visible = maxRows
		}

		rects := make([]image.Rectangle, 0, visible)
		for i := 0; i < visible; i++ {
			y1 := areaTop + i*rowH
			rect := image.Rect(a.layout.margin, y1, a.layout.screen.X-a.layout.margin, y1+rowH-20)
			rects = append(rects, rect)
			ink.DrawRect(rect, ink.Black)
			drawCenteredText(btnFont, rect, truncate(rows[i], 40), 44)
		}
		if len(rows) > visible {
			ink.DrawString(
				image.Point{X: a.layout.margin, Y: areaBottom - 30},
				fmt.Sprintf("Showing %d of %d.", visible, len(rows)),
			)
		}

		a.picker.mu.Lock()
		a.picker.rowRects = rects
		if atRoot {
			a.picker.upRect = image.Rectangle{}
		} else if len(rects) > 0 {
			a.picker.upRect = rects[0]
		}
		a.picker.mu.Unlock()
	}

	// "Sync this level" button, always present (syncing at root = sync all).
	selectY2 := a.layout.backButton.Min.Y - 40
	selectY1 := selectY2 - selectBtnH
	selectRect := image.Rect(a.layout.margin, selectY1, a.layout.screen.X-a.layout.margin, selectY2)
	ink.DrawRect(selectRect, ink.Black)
	ink.DrawRect(selectRect.Inset(2), ink.Black)
	label := "Sync this level"
	if stackLen == 0 {
		label = "Sync everything"
	} else if !loading && pickerErr == nil && lvl.BookCount > 0 {
		label = fmt.Sprintf("Sync this level (%d books)", lvl.BookCount)
	}
	drawCenteredText(btnFont, selectRect, truncate(label, 40), 44)
	a.picker.mu.Lock()
	a.picker.selectRect = selectRect
	a.picker.mu.Unlock()

	ink.DrawRect(a.layout.backButton, ink.Black)
	drawCenteredText(btnFont, a.layout.backButton, "Back", 44)
}

func (a *app) shelfPickerKey(e ink.KeyEvent) bool {
	if e.Key == ink.KeyOk {
		a.pickerConfirmCurrent()
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
	subs := a.picker.level.Subsections
	selectRect := a.picker.selectRect
	stackLen := len(a.picker.stack)
	a.picker.mu.Unlock()

	if e.Point.In(selectRect) {
		a.pickerConfirmCurrent()
		return true
	}

	atRoot := stackLen == 0
	for i, r := range rects {
		if !e.Point.In(r) {
			continue
		}
		if !atRoot && i == 0 {
			a.drillUp()
			return true
		}
		offset := 0
		if !atRoot {
			offset = 1
		}
		idx := i - offset
		if idx >= 0 && idx < len(subs) {
			s := subs[idx]
			a.drillInto(s.Href, s.Name)
			return true
		}
	}
	return false
}

// pickerConfirmCurrent saves the currently-displayed feed as the sync
// filter and returns to the settings screen. At the root (empty stack) we
// store an empty filter to mean "sync everything" so the rest of the app
// keeps the simple "no filter" semantics.
func (a *app) pickerConfirmCurrent() {
	a.picker.mu.Lock()
	href := a.picker.href
	title := a.picker.title
	atRoot := len(a.picker.stack) == 0
	a.picker.mu.Unlock()
	if atRoot {
		a.setFilter("", "")
	} else {
		a.setFilter(href, title)
	}
	a.screen = screenSettings
	ink.HideHourglass()
	ink.Repaint()
}

// ---------- Directory picker (WebDAV) ----------

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
	a.dirPicker.mu.Unlock()
	a.screen = screenDirPicker
	ink.Repaint()
	go a.fetchDirEntries(p)
}

// fetchDirEntries lists subdirectories of path on the configured WebDAV
// server and stores the result for the picker to render.
func (a *app) fetchDirEntries(p string) {
	if err := a.ensureConnected(); err != nil {
		a.dirPicker.mu.Lock()
		a.dirPicker.loading = false
		a.dirPicker.err = err
		a.dirPicker.mu.Unlock()
		ink.Repaint()
		return
	}
	src := NewWebDAVSource(a.cfg.Host, a.cfg.User, a.cfg.Pass, p)
	entries, err := src.Client.ReadDir(normaliseRoot(p))
	var dirs []string
	if err == nil {
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
	title := ink.OpenFont(ink.DefaultFontBold, 64, true)
	defer title.Close()
	title.SetActive(ink.Black)

	body := ink.OpenFont(ink.DefaultFont, 32, true)
	defer body.Close()
	body.SetActive(ink.Black)

	btnFont := ink.OpenFont(ink.DefaultFontBold, 44, true)
	defer btnFont.Close()
	btnFont.SetActive(ink.Black)

	a.dirPicker.mu.Lock()
	loading := a.dirPicker.loading
	path := a.dirPicker.path
	dirs := append([]string(nil), a.dirPicker.dirs...)
	pickErr := a.dirPicker.err
	a.dirPicker.mu.Unlock()

	title.SetActive(ink.Black)
	ink.DrawString(image.Point{X: a.layout.margin, Y: 140}, "Select folder")

	body.SetActive(ink.Black)
	ink.DrawString(image.Point{X: a.layout.margin, Y: 220}, "Currently in: "+truncate(path, 60))

	if loading {
		ink.DrawString(image.Point{X: a.layout.margin, Y: 300}, "Loading...")
		ink.ShowHourglassAt(image.Point{X: a.layout.margin, Y: 360})
	} else if pickErr != nil {
		ink.DrawString(image.Point{X: a.layout.margin, Y: 300}, "Could not list folder:")
		ink.DrawString(image.Point{X: a.layout.margin, Y: 350}, truncate(pickErr.Error(), 60))
	} else {
		rowH := 90
		areaTop := a.layout.pickerAreaTop
		// Reserve space at the bottom for the "Sync this folder" button.
		selectBtnH := 100
		areaBottom := a.layout.pickerAreaBottom - selectBtnH - 40

		rows := make([]string, 0, 1+len(dirs))
		atRoot := path == "/" || path == ""
		if !atRoot {
			rows = append(rows, ".. (up)")
		}
		for _, d := range dirs {
			rows = append(rows, d+"/")
		}

		availableH := areaBottom - areaTop
		maxRows := availableH / rowH
		visible := len(rows)
		if visible > maxRows {
			visible = maxRows
		}

		rects := make([]image.Rectangle, 0, visible)
		for i := 0; i < visible; i++ {
			y1 := areaTop + i*rowH
			rect := image.Rect(a.layout.margin, y1, a.layout.screen.X-a.layout.margin, y1+rowH-20)
			rects = append(rects, rect)
			ink.DrawRect(rect, ink.Black)
			drawCenteredText(btnFont, rect, truncate(rows[i], 40), 44)
		}
		if len(rows) > visible {
			ink.DrawString(
				image.Point{X: a.layout.margin, Y: areaBottom - 30},
				fmt.Sprintf("Showing %d of %d.", visible, len(rows)),
			)
		}

		a.dirPicker.mu.Lock()
		a.dirPicker.rowRects = rects
		a.dirPicker.mu.Unlock()

		// Select button sits just above the Back button.
		selectY2 := a.layout.backButton.Min.Y - 40
		selectY1 := selectY2 - selectBtnH
		selectRect := image.Rect(a.layout.margin, selectY1, a.layout.screen.X-a.layout.margin, selectY2)
		ink.DrawRect(selectRect, ink.Black)
		ink.DrawRect(selectRect.Inset(2), ink.Black)
		drawCenteredText(btnFont, selectRect, "Sync this folder", 44)
		a.dirPicker.mu.Lock()
		a.dirPicker.selectRect = selectRect
		a.dirPicker.mu.Unlock()
	}

	ink.DrawRect(a.layout.backButton, ink.Black)
	drawCenteredText(btnFont, a.layout.backButton, "Back", 44)
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
	a.dirPicker.mu.Unlock()

	if e.Point.In(selectRect) {
		a.setPath(path)
		a.screen = screenSettings
		ink.HideHourglass()
		ink.Repaint()
		return true
	}

	atRoot := path == "/" || path == ""
	for i, r := range rects {
		if !e.Point.In(r) {
			continue
		}
		if !atRoot && i == 0 {
			a.openDirPicker(parentDir(path))
			return true
		}
		offset := 0
		if !atRoot {
			offset = 1
		}
		idx := i - offset
		if idx >= 0 && idx < len(dirs) {
			child := dirs[idx]
			next := normaliseRoot(path) + "/" + child
			a.openDirPicker(next)
			return true
		}
	}
	return false
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

// setFilter updates the in-memory config and persists it.
func (a *app) setFilter(href, name string) {
	a.cfg.FilterHref = href
	a.cfg.FilterName = name
	_ = SaveConfig(a.cfgPath, a.cfg)
}
