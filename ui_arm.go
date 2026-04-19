package main

import (
	"context"
	"fmt"
	"image"
	"log"
	"os"
	"path/filepath"
	"strings"
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
	screenDeleteConfirm
	screenProfileList
	screenUpdate
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
	// first-run setup. It controls where the wizard returns on success
	// and whether the profile-name step is shown.
	addProfile bool
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
	bookStart time.Time          // when the current book's download began
	cancel    context.CancelFunc // populated while active; nil otherwise
	cancelled bool               // true when the user tapped Cancel so the summary can say so
}

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

// updateState holds the state of the self-update flow: a pending
// release (if a check found one newer than the running version), the
// active download's progress, and any terminal message shown once the
// install succeeds or fails.
type updateState struct {
	mu           sync.Mutex
	checking     bool
	available    bool
	release      Release
	checkErr     error
	downloading  bool
	downloaded   int64
	installErr   error
	installed    bool // true once the new binary has been written to disk
	installBtn   image.Rectangle
	checkBtn     image.Rectangle
	toggleBtn    image.Rectangle // enable/disable automatic weekly checks
	backBtn      image.Rectangle
}

// profileListState holds the snapshot shown on the profile-list screen
// and the tap targets for each row plus the Add-new / Delete buttons.
type profileListState struct {
	mu        sync.Mutex
	names     []string
	active    string
	err       error
	rowRects  []image.Rectangle
	addRect   image.Rectangle
	delRect   image.Rectangle
}

// deleteConfirmState holds the deletion set awaiting user confirmation
// and the channel the sync goroutine blocks on. ch is non-nil only while
// a prompt is pending.
type deleteConfirmState struct {
	mu       sync.Mutex
	pending  []LocalBook
	ch       chan bool
	yesRect  image.Rectangle
	noRect   image.Rectangle
}

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
	rowRects     []image.Rectangle // one per visible row (index 0 = "..", 1+ = dirs)
	prevPageRect image.Rectangle
	nextPageRect image.Rectangle
	selectRect   image.Rectangle // "Sync this folder" button
}

// layout holds screen-relative rectangles for every clickable element and for
// the progress strip. Computed once in Init from the actual ScreenSize so the
// UI adapts to whatever resolution the device reports.
type layout struct {
	screen image.Point
	margin int
	// scale multiplies all font heights so the UI stays readable on
	// smaller PocketBook panels (Touch HD, Touch Lux 5) without
	// blowing up unnecessarily on the HD 6" devices we originally
	// designed against (Era / Era Color at 1264x1680).
	scale float64

	// main screen
	syncButton     image.Rectangle
	networkButton  image.Rectangle
	settingsButton image.Rectangle
	quitButton     image.Rectangle
	progressArea   image.Rectangle // the strip refreshed via PartialUpdate
	progressBar    image.Rectangle

	// settings screen
	changeButton    image.Rectangle
	filterButton    image.Rectangle
	deleteTglButton image.Rectangle
	profilesButton  image.Rectangle
	backButton      image.Rectangle

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
	scale := scaleFactor(sz)
	sideMargin := int(60 * scale)
	topSafe := int(100 * scale)
	bottomSafe := int(180 * scale)
	if w > 0 && w < 1200 {
		sideMargin = int(40 * scale)
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

	// Settings screen: four stacked big buttons (change server info,
	// change filter, toggle delete-missing, switch/add server profile);
	// Back in the bottom-left mirroring the main screen.
	changeY1 := topSafe + 320
	changeBtn := image.Rect(sideMargin, changeY1, sideMargin+contentW, changeY1+160)
	filterY1 := changeY1 + 200
	filterBtn := image.Rect(sideMargin, filterY1, sideMargin+contentW, filterY1+160)
	deleteTglY1 := filterY1 + 200
	deleteTglBtn := image.Rect(sideMargin, deleteTglY1, sideMargin+contentW, deleteTglY1+120)
	profilesY1 := deleteTglY1 + 160
	profilesBtn := image.Rect(sideMargin, profilesY1, sideMargin+contentW, profilesY1+120)

	// Shelf picker: rows live between the header (below topSafe) and the
	// Back button (same position as bottom btnY1).
	pickerTop := topSafe + 220
	pickerBottom := btnY1 - 40

	return layout{
		screen:           sz,
		margin:           sideMargin,
		scale:            scale,
		syncButton:       syncBtn,
		networkButton:    networkBtn,
		settingsButton:   settingsBtn,
		quitButton:       quitBtn,
		progressArea:     progArea,
		progressBar:      progBar,
		changeButton:     changeBtn,
		filterButton:     filterBtn,
		deleteTglButton:  deleteTglBtn,
		profilesButton:   profilesBtn,
		backButton:       networkBtn,
		pickerAreaTop:    pickerTop,
		pickerAreaBottom: pickerBottom,
	}
}

// scaleFactor returns a multiplier for font sizes and generous spacings
// so the UI stays readable on smaller PocketBook panels without looking
// bloated on HD 6" screens. Three tiers match the three resolution
// families we see across the PocketBook line:
//   - >= 1200 wide: Era / Era Color / InkPad Color / InkPad X (scale 1.0)
//   - >= 1000 wide: Touch HD, HD 3 (~1072 wide, scale 0.85)
//   - smaller: Touch Lux 5 and legacy 6" (scale 0.7)
//
// We key off pixel width rather than DPI because the only value InkView
// hands us for free is the framebuffer size; DPI-per-model tables live
// in KOReader and would be heavy to maintain.
func scaleFactor(sz image.Point) float64 {
	w := sz.X
	if sz.Y < w {
		w = sz.Y
	}
	switch {
	case w >= 1200:
		return 1.0
	case w >= 1000:
		return 0.85
	default:
		return 0.7
	}
}

// fpx scales a base font size in px by s.scale, rounded to nearest pixel.
// Centralised so every drawString that picks a font size scales the
// same way.
func (s layout) fpx(base int) int {
	return int(float64(base)*s.scale + 0.5)
}

// tapDebounce is the minimum gap between two pointer events that will
// both be treated as taps. PocketBook sometimes fires only a PointerDown
// or only a PointerUp for a glancing touch; the bottom-row Network
// button was the most visible victim. Reacting to both event states and
// dropping close duplicates gives every tap a chance to land without
// letting a genuine down+up pair fire the same action twice.
const tapDebounce = 250 * time.Millisecond

// app implements ink.App for the pocketbeam device UI.
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
	delConfirm  deleteConfirmState
	profileList profileListState
	update      updateState
	lastSync    SyncSummary
	hasLastSync bool
	bookCount   int
	netStop           func()
	layout            layout
	connState         connectivity
	lastTap           time.Time
	updateFooterRect  image.Rectangle
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

// migrateLegacyConfig renames pre-rename files into place for existing
// installs and rewrites the cfg's state_db key so it points at the
// renamed database. Runs before LoadConfig so the upgrade is invisible to
// the rest of the app.
// TODO: remove before public release.
func migrateLegacyConfig(cfgPath string) {
	dir := filepath.Dir(cfgPath)
	pairs := [][2]string{
		{filepath.Join(dir, "bookbeam.cfg"), cfgPath},
		{filepath.Join(dir, "bookbeam.db"), filepath.Join(dir, "pocketbeam.db")},
	}
	for _, p := range pairs {
		oldP, newP := p[0], p[1]
		if _, err := os.Stat(newP); err == nil {
			continue
		}
		if _, err := os.Stat(oldP); err != nil {
			continue
		}
		_ = os.Rename(oldP, newP)
	}
	// Rewrite state_db inside the cfg if it still points at the old
	// filename. os.ReadFile + os.WriteFile is fine for a tiny config.
	if data, err := os.ReadFile(cfgPath); err == nil {
		if patched := strings.ReplaceAll(string(data), "bookbeam.db", "pocketbeam.db"); patched != string(data) {
			_ = os.WriteFile(cfgPath, []byte(patched), 0o600)
		}
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

	// TODO: remove before public release. One-shot migration for the
	// bookbeam → pocketbeam rename: rename the old config and state DB
	// into place so existing installs upgrade seamlessly.
	migrateLegacyConfig(a.cfgPath)

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
	case screenProfileList:
		a.drawProfileList()
	case screenUpdate:
		a.drawUpdate()
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
		case screenShelfPicker, screenDirPicker, screenProfileList, screenUpdate:
			a.screen = screenSettings
			ink.Repaint()
			return true
		case screenDeleteConfirm:
			// Back key on the prompt is equivalent to "No": keep books,
			// release the sync goroutine.
			a.answerDelete(false)
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
	case screenProfileList:
		return a.profileListKey(e)
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
	case screenProfileList:
		return a.profileListPointer(e)
	case screenUpdate:
		return a.updatePointer(e)
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
	title := ink.OpenFont(ink.DefaultFontBold, a.layout.fpx(54), true)
	defer title.Close()
	title.SetActive(ink.Black)

	body := ink.OpenFont(ink.DefaultFont, a.layout.fpx(32), true)
	defer body.Close()
	body.SetActive(ink.Black)

	switch a.wizard.step {
	case stepWelcome:
		title.SetActive(ink.Black)
		ink.DrawString(image.Point{X: 80, Y: 200}, "pocketbeam")
		body.SetActive(ink.Black)
		ink.DrawString(image.Point{X: 80, Y: 280}, "Wireless sync from a book server.")
		ink.DrawString(image.Point{X: 80, Y: 360}, "Supports:")
		ink.DrawString(image.Point{X: 120, Y: 420}, "- Calibre-Web / any OPDS server")
		ink.DrawString(image.Point{X: 120, Y: 470}, "- WebDAV (Nextcloud, Synology, ownCloud)")
		ink.DrawString(image.Point{X: 80, Y: 620}, "Press OK or tap the screen to begin.")

	case stepProfileName:
		title.SetActive(ink.Black)
		ink.DrawString(image.Point{X: 80, Y: 200}, "Name this profile")
		body.SetActive(ink.Black)
		ink.DrawString(image.Point{X: 80, Y: 290}, "Short identifier for this server (no spaces).")
		ink.DrawString(image.Point{X: 80, Y: 360}, "Tap or press OK to re-open the keyboard.")
		if a.wizard.name != "" {
			ink.DrawString(image.Point{X: 80, Y: 480}, "Name: "+a.wizard.name)
		}

	case stepBackend:
		title.SetActive(ink.Black)
		ink.DrawString(image.Point{X: 80, Y: 200}, "Choose server type")
		body.SetActive(ink.Black)
		ink.DrawString(image.Point{X: 80, Y: 290}, "Tap the option that matches your server.")

		btnFont := ink.OpenFont(ink.DefaultFontBold, a.layout.fpx(44), true)
		defer btnFont.Close()
		btnFont.SetActive(ink.Black)

		w := a.layout.screen.X
		btnW := w - 2*a.layout.margin
		a.wizard.opdsBtn = image.Rect(a.layout.margin, 380, a.layout.margin+btnW, 540)
		a.wizard.webdavBtn = image.Rect(a.layout.margin, 580, a.layout.margin+btnW, 740)

		ink.DrawRect(a.wizard.opdsBtn, ink.Black)
		ink.DrawRect(a.wizard.opdsBtn.Inset(2), ink.Black)
		drawCenteredText(btnFont, a.wizard.opdsBtn, "Calibre-Web / OPDS", a.layout.fpx(44))

		ink.DrawRect(a.wizard.webdavBtn, ink.Black)
		ink.DrawRect(a.wizard.webdavBtn.Inset(2), ink.Black)
		drawCenteredText(btnFont, a.wizard.webdavBtn, "WebDAV / Nextcloud", a.layout.fpx(44))

	case stepURL, stepUser, stepPass:
		title.SetActive(ink.Black)
		ink.DrawString(image.Point{X: 80, Y: 200}, "pocketbeam setup")
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
	case stepProfileName:
		if e.Key == ink.KeyOk {
			ink.OpenKeyboard("new-profile-name", 40)
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
	case stepProfileName:
		ink.OpenKeyboard("new-profile-name", 40)
		return true
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
	case stepProfileName:
		name := sanitizeProfileName(text)
		if name == "" {
			ink.OpenKeyboard("new-profile-name", 40)
			return
		}
		a.wizard.name = name
		a.wizard.step = stepBackend
		ink.Repaint()
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

// sanitizeProfileName strips whitespace and characters that would confuse
// the config-file section parser (brackets, equals). Empty input returns
// "" so the wizard can re-ask.
func sanitizeProfileName(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, " ", "-")
	s = strings.Trim(s, "[]=")
	return s
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
	profile := a.wizard.name
	if profile == "" {
		profile = defaultProfileName
	}
	cfg := &Config{
		Profile:      profile,
		Backend:      backend,
		Host:         a.wizard.url,
		User:         a.wizard.user,
		Pass:         a.wizard.pass,
		Library:      filepath.Join(ink.FlashDir, "Books", libraryDir),
		StateDB:      filepath.Join(ink.ConfigPath, "pocketbeam.db"),
		CheckUpdates: true,
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
	// When the wizard is being used to add a profile, the new section is
	// written but the file's `active` marker still points at the existing
	// one. Flip it so the newly-created profile becomes the working one.
	if a.wizard.addProfile {
		if err := SetActiveProfile(a.cfgPath, profile); err != nil {
			a.wizard.err = err
			a.wizard.step = stepError
			ink.Repaint()
			return
		}
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
	title := ink.OpenFont(ink.DefaultFontBold, a.layout.fpx(64), true)
	defer title.Close()
	title.SetActive(ink.Black)

	body := ink.OpenFont(ink.DefaultFont, a.layout.fpx(32), true)
	defer body.Close()
	body.SetActive(ink.Black)

	btnFont := ink.OpenFont(ink.DefaultFontBold, a.layout.fpx(44), true)
	defer btnFont.Close()
	btnFont.SetActive(ink.Black)

	// Header
	title.SetActive(ink.Black)
	ink.DrawString(image.Point{X: a.layout.margin, Y: 120}, "pocketbeam")

	// Connection info + filter + status stacked tightly near the top
	body.SetActive(ink.Black)
	ink.DrawString(image.Point{X: a.layout.margin, Y: 210}, "Server: "+a.cfg.Host)

	ink.DrawString(image.Point{X: a.layout.margin, Y: 255}, "Filter: "+a.cfg.FilterLabel())

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

	// Update banner: drawn below the last-sync block when a newer
	// release has been detected. Non-clickable on the main screen to
	// keep the layout stable; the user opens Settings to install.
	a.update.mu.Lock()
	updateAvail := a.update.available
	updateVer := a.update.release.Version
	a.update.mu.Unlock()
	if updateAvail && updateVer != "" {
		ink.DrawString(image.Point{X: a.layout.margin, Y: 495},
			"Update available: "+updateVer+"  (Settings → Check for updates)")
	}

	// Sync Now / Cancel button. The rect is the same either way so the
	// user always finds the primary action in the same spot; only the
	// label + handler swap while sync is active.
	ink.DrawRect(a.layout.syncButton, ink.Black)
	ink.DrawRect(a.layout.syncButton.Inset(2), ink.Black)
	btnFont.SetActive(ink.Black)
	btnLabel := "Sync Now"
	if a.syncActive() {
		btnLabel = "Stop"
	}
	drawCenteredText(btnFont, a.layout.syncButton, btnLabel, a.layout.fpx(44))

	// Live progress area (drawn fully here on idle-to-sync transition; during
	// the sync it is refreshed in-place via drawMainProgress + PartialUpdate).
	a.drawMainProgressContent(body)

	// Bottom buttons: Network, Settings, Quit
	ink.DrawRect(a.layout.networkButton, ink.Black)
	ink.DrawRect(a.layout.settingsButton, ink.Black)
	ink.DrawRect(a.layout.quitButton, ink.Black)
	btnFont.SetActive(ink.Black)
	drawCenteredText(btnFont, a.layout.networkButton, "Network", a.layout.fpx(44))
	drawCenteredText(btnFont, a.layout.settingsButton, "Settings", a.layout.fpx(44))
	drawCenteredText(btnFont, a.layout.quitButton, "Quit", a.layout.fpx(44))
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
	body := ink.OpenFont(ink.DefaultFont, a.layout.fpx(32), true)
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
	if !a.acceptTap(e) {
		return false
	}
	p := e.Point
	// During an active sync the only tap target is the Sync button,
	// which doubles as Cancel.
	if a.syncActive() {
		if p.In(a.layout.syncButton) {
			a.cancelSync()
			return true
		}
		return false
	}
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

// cancelSync signals the in-flight Sync to abort. The sync goroutine
// will return shortly afterwards; the UI flips back to the idle screen
// at that point.
func (a *app) cancelSync() {
	a.sync.mu.Lock()
	cancel := a.sync.cancel
	a.sync.cancelled = true
	a.sync.mu.Unlock()
	if cancel != nil {
		cancel()
	}
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
	// idle timeout even though pocketbeam is actively downloading. Restore
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
	opts := SyncOptions{
		DeleteMissing: a.cfg.DeleteMissing,
		Scope:         ScopeFor(a.cfg),
	}
	if a.cfg.DeleteMissing {
		opts.Confirm = a.confirmDeletions
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.sync.mu.Lock()
	a.sync.cancel = cancel
	a.sync.cancelled = false
	a.sync.mu.Unlock()
	defer cancel()

	res := Sync(ctx, src, a.store, a.cfg.Library, progress, opts)

	a.sync.mu.Lock()
	a.sync.active = false
	a.sync.cancel = nil
	wasCancelled := a.sync.cancelled
	a.sync.cancelled = false
	if wasCancelled {
		a.sync.err = fmt.Errorf("Sync stopped.")
	} else {
		a.sync.err = res.FirstErr
	}
	a.sync.mu.Unlock()

	// Persist summary + refresh screen stats. Deleted is not currently
	// shown on the main screen; the count is visible through the log
	// channel once the PR wires that up.
	_ = a.store.SetLastSync(SyncSummary{
		At:         time.Now(),
		Downloaded: res.Downloaded,
		Skipped:    res.Skipped,
		Failed:     res.Failed,
	})
	a.refreshMainStats()
	// In case the confirm screen is still up (e.g. user closed the device
	// with the prompt showing), flip back to main.
	a.screen = screenMain
	ink.Repaint()

	// We deliberately do NOT auto-exec /mnt/ext1/system/bin/scanner.app here:
	// on PocketBook firmware 6.x it takes foreground focus, which makes
	// pocketbeam appear frozen until the scan finishes. The Library app
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
	title := ink.OpenFont(ink.DefaultFontBold, a.layout.fpx(64), true)
	defer title.Close()
	title.SetActive(ink.Black)

	body := ink.OpenFont(ink.DefaultFont, a.layout.fpx(32), true)
	defer body.Close()
	body.SetActive(ink.Black)

	btnFont := ink.OpenFont(ink.DefaultFontBold, a.layout.fpx(44), true)
	defer btnFont.Close()
	btnFont.SetActive(ink.Black)

	small := ink.OpenFont(ink.DefaultFont, a.layout.fpx(26), true)
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
		filterLabel = "Filter: " + a.cfg.FilterLabel()
	}
	ink.DrawString(image.Point{X: a.layout.margin, Y: 400}, filterLabel)

	// Change-info button
	ink.DrawRect(a.layout.changeButton, ink.Black)
	ink.DrawRect(a.layout.changeButton.Inset(2), ink.Black)
	btnFont.SetActive(ink.Black)
	drawCenteredText(btnFont, a.layout.changeButton, "Change server info", a.layout.fpx(44))

	// Change-filter / folder button (label varies by backend).
	filterBtnText := "Change sync filter"
	if a.cfg.Backend == BackendWebDAV {
		filterBtnText = "Change sync folder"
	}
	ink.DrawRect(a.layout.filterButton, ink.Black)
	ink.DrawRect(a.layout.filterButton.Inset(2), ink.Black)
	drawCenteredText(btnFont, a.layout.filterButton, filterBtnText, a.layout.fpx(44))

	// Delete-missing toggle: drawn as a single tap-to-cycle button whose
	// label reflects the current state. Single border (not inset) to read
	// as lighter-weight than the two primary actions.
	delLabel := "Delete missing: off  (tap to enable)"
	if a.cfg.DeleteMissing {
		delLabel = "Delete missing: on  (tap to disable)"
	}
	ink.DrawRect(a.layout.deleteTglButton, ink.Black)
	drawCenteredText(btnFont, a.layout.deleteTglButton, delLabel, a.layout.fpx(44))

	// Switch / add server profile.
	profileBtnText := fmt.Sprintf("Profile: %s  (switch or add)", a.cfg.Profile)
	ink.DrawRect(a.layout.profilesButton, ink.Black)
	drawCenteredText(btnFont, a.layout.profilesButton, profileBtnText, a.layout.fpx(44))

	// Footer: version line doubles as "Check for updates". The text
	// tells the user the current version; tapping it opens the update
	// screen. A pending update is advertised here in brackets so the
	// tap target is obvious.
	small.SetActive(ink.Black)
	versionLine := "pocketbeam " + version + "  (tap for updates)"
	a.update.mu.Lock()
	if a.update.available && a.update.release.Version != "" {
		versionLine = "pocketbeam " + version + "  →  " + a.update.release.Version + " available (tap to install)"
	}
	a.update.mu.Unlock()
	versionY := a.layout.backButton.Min.Y - 40
	ink.DrawString(image.Point{X: a.layout.margin, Y: versionY}, versionLine)
	// Remember the tappable strip for settingsPointer.
	a.updateFooterRect = image.Rect(a.layout.margin, versionY-20, a.layout.screen.X-a.layout.margin, versionY+30)

	ink.DrawRect(a.layout.backButton, ink.Black)
	btnFont.SetActive(ink.Black)
	drawCenteredText(btnFont, a.layout.backButton, "Back", a.layout.fpx(44))
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
	case p.In(a.layout.deleteTglButton):
		a.cfg.DeleteMissing = !a.cfg.DeleteMissing
		_ = SaveConfig(a.cfgPath, a.cfg)
		ink.Repaint()
		return true
	case p.In(a.layout.profilesButton):
		a.openProfileList()
		return true
	case p.In(a.updateFooterRect):
		a.openUpdateScreen()
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
	a.picker.mu.Lock()
	a.picker.stack = nil
	a.picker.href = "/opds"
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
	a.picker.offset = 0
	a.picker.mu.Unlock()
	ink.Repaint()
	go a.fetchFeedLevel(href, title)
}

// shelfPickerPage scrolls the list by one page. Called from the Prev /
// Next page buttons; the actual clamping is in drawShelfPicker so the
// offset can never point past the current list length.
func (a *app) shelfPickerPage(direction int) {
	a.picker.mu.Lock()
	// Page size is computed at draw time from the list's length. We use
	// the number of currently-visible rows as a good approximation; the
	// draw clamps any out-of-range result.
	step := len(a.picker.rowRects)
	if step <= 0 {
		step = 1
	}
	a.picker.offset += direction * step
	if a.picker.offset < 0 {
		a.picker.offset = 0
	}
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
	title := ink.OpenFont(ink.DefaultFontBold, a.layout.fpx(64), true)
	defer title.Close()
	title.SetActive(ink.Black)
	ink.DrawString(image.Point{X: a.layout.margin, Y: 140}, "Select filter")

	body := ink.OpenFont(ink.DefaultFont, a.layout.fpx(32), true)
	defer body.Close()
	body.SetActive(ink.Black)

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

	var prevPageRect, nextPageRect image.Rectangle

	if loading {
		ink.DrawString(image.Point{X: a.layout.margin, Y: 300}, "Loading...")
		ink.ShowHourglassAt(image.Point{X: a.layout.margin, Y: 360})
	} else if pickerErr != nil {
		ink.DrawString(image.Point{X: a.layout.margin, Y: 300}, "Could not load feed:")
		ink.DrawString(image.Point{X: a.layout.margin, Y: 350}, truncate(pickerErr.Error(), 60))
	} else {
		rowH := 90
		pageBtnH := 60
		atRoot := stackLen == 0
		// Suppress navigation rows whose server-advertised count is zero
		// (opds:count == 0). Servers that don't advertise counts leave
		// CountKnown=false and the row shows normally.
		visibleSubs := make([]FilterOption, 0, len(lvl.Subsections))
		for _, sub := range lvl.Subsections {
			if sub.CountKnown && sub.Count == 0 {
				continue
			}
			visibleSubs = append(visibleSubs, sub)
		}
		rows := make([]string, 0, len(visibleSubs)+1)
		if !atRoot {
			rows = append(rows, ".. (up)")
		}
		for _, sub := range visibleSubs {
			label := sub.Name
			if sub.CountKnown {
				label = fmt.Sprintf("%s  (%d)", sub.Name, sub.Count)
			}
			rows = append(rows, label)
		}

		// Paging: reserve a thin strip at the bottom of the list area for
		// page buttons only when the list overflows a single page.
		visibleArea := areaBottom - areaTop
		maxRows := (visibleArea - pageBtnH - 20) / rowH
		if maxRows < 1 {
			maxRows = 1
		}
		if maxRows >= len(rows) {
			maxRows = len(rows)
		}
		// Clamp offset so a shorter list after drilling / scrolling can't
		// leave a stale offset pointing past the end.
		if offset > len(rows)-maxRows {
			offset = len(rows) - maxRows
		}
		if offset < 0 {
			offset = 0
		}
		end := offset + maxRows
		if end > len(rows) {
			end = len(rows)
		}

		rects := make([]image.Rectangle, 0, end-offset)
		for i := offset; i < end; i++ {
			y1 := areaTop + (i-offset)*rowH
			rect := image.Rect(a.layout.margin, y1, a.layout.screen.X-a.layout.margin, y1+rowH-20)
			rects = append(rects, rect)
			ink.DrawRect(rect, ink.Black)
			drawCenteredText(btnFont, rect, truncate(rows[i], 40), a.layout.fpx(44))
		}

		// Page buttons appear only when more rows exist outside the window.
		if len(rows) > maxRows {
			btnY1 := areaTop + maxRows*rowH
			btnY2 := btnY1 + pageBtnH
			contentW := a.layout.screen.X - 2*a.layout.margin
			half := (contentW - 40) / 2
			if offset > 0 {
				prevPageRect = image.Rect(a.layout.margin, btnY1, a.layout.margin+half, btnY2)
				ink.DrawRect(prevPageRect, ink.Black)
				drawCenteredText(btnFont, prevPageRect, "< Prev", a.layout.fpx(44))
			}
			if end < len(rows) {
				nextPageRect = image.Rect(a.layout.screen.X-a.layout.margin-half, btnY1, a.layout.screen.X-a.layout.margin, btnY2)
				ink.DrawRect(nextPageRect, ink.Black)
				drawCenteredText(btnFont, nextPageRect, "Next >", a.layout.fpx(44))
			}
			// Page-of-pages indicator underneath.
			page := offset/maxRows + 1
			total := (len(rows) + maxRows - 1) / maxRows
			ink.DrawString(
				image.Point{X: a.layout.margin, Y: btnY2 + 30},
				fmt.Sprintf("Page %d of %d (%d items)", page, total, len(rows)),
			)
		}

		a.picker.mu.Lock()
		a.picker.rowRects = rects
		a.picker.prevPageRect = prevPageRect
		a.picker.nextPageRect = nextPageRect
		if atRoot {
			a.picker.upRect = image.Rectangle{}
		} else if offset == 0 && len(rects) > 0 {
			a.picker.upRect = rects[0]
		} else {
			a.picker.upRect = image.Rectangle{}
		}
		a.picker.mu.Unlock()
	}

	// Primary action button(s). With no selection the user sees a single
	// "Sync this level" (sync-all at root) that saves and closes. Once a
	// selection is in progress we show two stacked buttons: the top one
	// is "Add" / "Remove" for the current feed, and the bottom one is
	// "Done (N selected)" that persists and closes.
	a.picker.mu.Lock()
	selected := append([]FilterOption(nil), a.picker.selected...)
	curHref := a.picker.href
	curTitleSaved := a.picker.title
	a.picker.mu.Unlock()

	alreadyIn := pickerContains(selected, curHref)
	selectY2 := a.layout.backButton.Min.Y - 40
	selectY1 := selectY2 - selectBtnH
	selectRect := image.Rect(a.layout.margin, selectY1, a.layout.screen.X-a.layout.margin, selectY2)
	var doneRect image.Rectangle
	if len(selected) == 0 {
		ink.DrawRect(selectRect, ink.Black)
		ink.DrawRect(selectRect.Inset(2), ink.Black)
		label := "Sync this level"
		if stackLen == 0 {
			label = "Sync everything"
		} else if !loading && pickerErr == nil && lvl.BookCount > 0 {
			label = fmt.Sprintf("Sync this level (%d books)", lvl.BookCount)
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

	_ = curTitleSaved // retained so future iterations can show the breadcrumb in the Add label

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
	subsAll := a.picker.level.Subsections
	selectRect := a.picker.selectRect
	doneRect := a.picker.doneRect
	stackLen := len(a.picker.stack)
	offset := a.picker.offset
	prev := a.picker.prevPageRect
	next := a.picker.nextPageRect
	hasSelection := len(a.picker.selected) > 0
	a.picker.mu.Unlock()

	// Match the visible filter used in drawShelfPicker so row indices
	// line up with what the user sees.
	subs := make([]FilterOption, 0, len(subsAll))
	for _, s := range subsAll {
		if s.CountKnown && s.Count == 0 {
			continue
		}
		subs = append(subs, s)
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

	atRoot := stackLen == 0
	// Compute the absolute row index: rects[0] is at offset; adjust for the
	// leading ".. (up)" row that's only present on page 1.
	upOffset := 0
	if !atRoot && offset == 0 {
		upOffset = 1
	}
	for i, r := range rects {
		if !e.Point.In(r) {
			continue
		}
		if !atRoot && offset == 0 && i == 0 {
			a.drillUp()
			return true
		}
		abs := offset + i - upOffset
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
	ink.HideHourglass()
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
	a.dirPicker.offset = 0
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
	title := ink.OpenFont(ink.DefaultFontBold, a.layout.fpx(64), true)
	defer title.Close()
	title.SetActive(ink.Black)

	body := ink.OpenFont(ink.DefaultFont, a.layout.fpx(32), true)
	defer body.Close()
	body.SetActive(ink.Black)

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
	ink.DrawString(image.Point{X: a.layout.margin, Y: 140}, "Select folder")

	body.SetActive(ink.Black)
	ink.DrawString(image.Point{X: a.layout.margin, Y: 220}, "Currently in: "+truncate(path, 60))

	var prevPageRect, nextPageRect image.Rectangle

	if loading {
		ink.DrawString(image.Point{X: a.layout.margin, Y: 300}, "Loading...")
		ink.ShowHourglassAt(image.Point{X: a.layout.margin, Y: 360})
	} else if pickErr != nil {
		ink.DrawString(image.Point{X: a.layout.margin, Y: 300}, "Could not list folder:")
		ink.DrawString(image.Point{X: a.layout.margin, Y: 350}, truncate(pickErr.Error(), 60))
	} else {
		rowH := 90
		pageBtnH := 60
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

		visibleArea := areaBottom - areaTop
		maxRows := (visibleArea - pageBtnH - 20) / rowH
		if maxRows < 1 {
			maxRows = 1
		}
		if maxRows >= len(rows) {
			maxRows = len(rows)
		}
		if offset > len(rows)-maxRows {
			offset = len(rows) - maxRows
		}
		if offset < 0 {
			offset = 0
		}
		end := offset + maxRows
		if end > len(rows) {
			end = len(rows)
		}

		rects := make([]image.Rectangle, 0, end-offset)
		for i := offset; i < end; i++ {
			y1 := areaTop + (i-offset)*rowH
			rect := image.Rect(a.layout.margin, y1, a.layout.screen.X-a.layout.margin, y1+rowH-20)
			rects = append(rects, rect)
			ink.DrawRect(rect, ink.Black)
			drawCenteredText(btnFont, rect, truncate(rows[i], 40), a.layout.fpx(44))
		}
		if len(rows) > maxRows {
			btnY1 := areaTop + maxRows*rowH
			btnY2 := btnY1 + pageBtnH
			contentW := a.layout.screen.X - 2*a.layout.margin
			half := (contentW - 40) / 2
			if offset > 0 {
				prevPageRect = image.Rect(a.layout.margin, btnY1, a.layout.margin+half, btnY2)
				ink.DrawRect(prevPageRect, ink.Black)
				drawCenteredText(btnFont, prevPageRect, "< Prev", a.layout.fpx(44))
			}
			if end < len(rows) {
				nextPageRect = image.Rect(a.layout.screen.X-a.layout.margin-half, btnY1, a.layout.screen.X-a.layout.margin, btnY2)
				ink.DrawRect(nextPageRect, ink.Black)
				drawCenteredText(btnFont, nextPageRect, "Next >", a.layout.fpx(44))
			}
			page := offset/maxRows + 1
			total := (len(rows) + maxRows - 1) / maxRows
			ink.DrawString(
				image.Point{X: a.layout.margin, Y: btnY2 + 30},
				fmt.Sprintf("Page %d of %d (%d items)", page, total, len(rows)),
			)
		}

		a.dirPicker.mu.Lock()
		a.dirPicker.rowRects = rects
		a.dirPicker.prevPageRect = prevPageRect
		a.dirPicker.nextPageRect = nextPageRect
		a.dirPicker.mu.Unlock()

		// Select button sits just above the Back button.
		selectY2 := a.layout.backButton.Min.Y - 40
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
	a.dirPicker.mu.Unlock()

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

	atRoot := path == "/" || path == ""
	upOffset := 0
	if !atRoot && offset == 0 {
		upOffset = 1
	}
	for i, r := range rects {
		if !e.Point.In(r) {
			continue
		}
		if !atRoot && offset == 0 && i == 0 {
			a.openDirPicker(parentDir(path))
			return true
		}
		abs := offset + i - upOffset
		if abs >= 0 && abs < len(dirs) {
			child := dirs[abs]
			a.openDirPicker(normaliseRoot(path) + "/" + child)
			return true
		}
	}
	return false
}

// dirPickerPage scrolls the directory list by one page and repaints.
func (a *app) dirPickerPage(direction int) {
	a.dirPicker.mu.Lock()
	step := len(a.dirPicker.rowRects)
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

// setFilters replaces the config's filter selection and persists it.
// Passing nil/empty slices clears the filter (sync everything).
func (a *app) setFilters(hrefs, names []string) {
	a.cfg.FilterHrefs = hrefs
	a.cfg.FilterNames = names
	_ = SaveConfig(a.cfgPath, a.cfg)
}

// ---------- Delete-missing confirmation ----------

// confirmDeletions is invoked by the sync goroutine when delete-missing
// finds books absent from the remote. It parks the deletions on the UI,
// flips to the confirm screen, and blocks until the user answers.
func (a *app) confirmDeletions(deletions []LocalBook) bool {
	a.delConfirm.mu.Lock()
	a.delConfirm.pending = deletions
	a.delConfirm.ch = make(chan bool, 1)
	ch := a.delConfirm.ch
	a.delConfirm.mu.Unlock()
	a.screen = screenDeleteConfirm
	ink.Repaint()
	return <-ch
}

// answerDelete replies to an in-flight confirmation prompt. Safe to call
// when no prompt is pending; it is a no-op.
func (a *app) answerDelete(ok bool) {
	a.delConfirm.mu.Lock()
	ch := a.delConfirm.ch
	a.delConfirm.ch = nil
	a.delConfirm.pending = nil
	a.delConfirm.mu.Unlock()
	if ch == nil {
		return
	}
	ch <- ok
	// The sync goroutine will flip back to screenMain once Sync returns;
	// until then the screen stays on a transient "processing" visual.
	a.screen = screenMain
	ink.Repaint()
}

func (a *app) drawDeleteConfirm() {
	title := ink.OpenFont(ink.DefaultFontBold, a.layout.fpx(64), true)
	defer title.Close()
	title.SetActive(ink.Black)

	body := ink.OpenFont(ink.DefaultFont, a.layout.fpx(32), true)
	defer body.Close()
	body.SetActive(ink.Black)

	btnFont := ink.OpenFont(ink.DefaultFontBold, a.layout.fpx(44), true)
	defer btnFont.Close()
	btnFont.SetActive(ink.Black)

	a.delConfirm.mu.Lock()
	pending := append([]LocalBook(nil), a.delConfirm.pending...)
	a.delConfirm.mu.Unlock()

	ink.DrawString(image.Point{X: a.layout.margin, Y: 140}, "Confirm deletion")

	body.SetActive(ink.Black)
	ink.DrawString(image.Point{X: a.layout.margin, Y: 230},
		fmt.Sprintf("%d book(s) are no longer on the server.", len(pending)))
	ink.DrawString(image.Point{X: a.layout.margin, Y: 280}, "Delete them from this device?")

	// Preview up to 5 titles, indented.
	const preview = 5
	y := 360
	for i, b := range pending {
		if i == preview {
			ink.DrawString(image.Point{X: a.layout.margin + 40, Y: y},
				fmt.Sprintf("... and %d more", len(pending)-preview))
			break
		}
		label := b.Author + ": " + b.Title
		ink.DrawString(image.Point{X: a.layout.margin + 40, Y: y}, truncate(label, 60))
		y += 50
	}

	// Yes / No buttons side by side, above the bottom safe margin.
	btnH := 100
	btnY2 := a.layout.backButton.Max.Y
	btnY1 := btnY2 - btnH
	contentW := a.layout.screen.X - 2*a.layout.margin
	half := (contentW - 40) / 2
	yesRect := image.Rect(a.layout.margin, btnY1, a.layout.margin+half, btnY2)
	noRect := image.Rect(a.layout.screen.X-a.layout.margin-half, btnY1, a.layout.screen.X-a.layout.margin, btnY2)

	ink.DrawRect(yesRect, ink.Black)
	ink.DrawRect(yesRect.Inset(2), ink.Black)
	drawCenteredText(btnFont, yesRect, "Delete", a.layout.fpx(44))

	ink.DrawRect(noRect, ink.Black)
	ink.DrawRect(noRect.Inset(2), ink.Black)
	drawCenteredText(btnFont, noRect, "Keep", a.layout.fpx(44))

	a.delConfirm.mu.Lock()
	a.delConfirm.yesRect = yesRect
	a.delConfirm.noRect = noRect
	a.delConfirm.mu.Unlock()
}

func (a *app) deleteConfirmKey(e ink.KeyEvent) bool {
	switch e.Key {
	case ink.KeyOk:
		a.answerDelete(true)
		return true
	case ink.KeyBack:
		a.answerDelete(false)
		return true
	}
	return false
}

func (a *app) deleteConfirmPointer(e ink.PointerEvent) bool {
	if e.State != ink.PointerDown {
		return false
	}
	a.delConfirm.mu.Lock()
	yes := a.delConfirm.yesRect
	no := a.delConfirm.noRect
	a.delConfirm.mu.Unlock()
	if e.Point.In(yes) {
		a.answerDelete(true)
		return true
	}
	if e.Point.In(no) {
		a.answerDelete(false)
		return true
	}
	return false
}

// ---------- Profile list ----------

// openProfileList refreshes the snapshot and switches to the profile
// screen. Profile data is loaded synchronously because the on-disk file
// is tiny.
func (a *app) openProfileList() {
	names, active, err := ListProfiles(a.cfgPath)
	a.profileList.mu.Lock()
	a.profileList.names = names
	a.profileList.active = active
	a.profileList.err = err
	a.profileList.rowRects = nil
	a.profileList.mu.Unlock()
	a.screen = screenProfileList
	ink.Repaint()
}

func (a *app) drawProfileList() {
	title := ink.OpenFont(ink.DefaultFontBold, a.layout.fpx(64), true)
	defer title.Close()
	title.SetActive(ink.Black)

	body := ink.OpenFont(ink.DefaultFont, a.layout.fpx(32), true)
	defer body.Close()
	body.SetActive(ink.Black)

	btnFont := ink.OpenFont(ink.DefaultFontBold, a.layout.fpx(44), true)
	defer btnFont.Close()
	btnFont.SetActive(ink.Black)

	a.profileList.mu.Lock()
	names := append([]string(nil), a.profileList.names...)
	active := a.profileList.active
	perr := a.profileList.err
	a.profileList.mu.Unlock()

	ink.DrawString(image.Point{X: a.layout.margin, Y: 140}, "Server profiles")

	if perr != nil {
		body.SetActive(ink.Black)
		ink.DrawString(image.Point{X: a.layout.margin, Y: 240}, "Could not list profiles:")
		ink.DrawString(image.Point{X: a.layout.margin, Y: 290}, truncate(perr.Error(), 60))
		ink.DrawRect(a.layout.backButton, ink.Black)
		drawCenteredText(btnFont, a.layout.backButton, "Back", a.layout.fpx(44))
		return
	}

	// Rows: one per profile. Active profile gets a leading marker.
	rowH := 90
	areaTop := a.layout.pickerAreaTop
	rects := make([]image.Rectangle, 0, len(names))
	for i, n := range names {
		y1 := areaTop + i*rowH
		rect := image.Rect(a.layout.margin, y1, a.layout.screen.X-a.layout.margin, y1+rowH-20)
		rects = append(rects, rect)
		ink.DrawRect(rect, ink.Black)
		label := n
		if n == active {
			label = "* " + n + "  (active)"
		}
		drawCenteredText(btnFont, rect, truncate(label, 40), a.layout.fpx(44))
	}

	// Add new + Delete current stacked above Back. Delete only when there
	// is more than one profile on disk.
	btnH := 100
	backMin := a.layout.backButton.Min.Y
	delY2 := backMin - 40
	delY1 := delY2 - btnH
	addY2 := delY1 - 30
	addY1 := addY2 - btnH

	addRect := image.Rect(a.layout.margin, addY1, a.layout.screen.X-a.layout.margin, addY2)
	ink.DrawRect(addRect, ink.Black)
	ink.DrawRect(addRect.Inset(2), ink.Black)
	drawCenteredText(btnFont, addRect, "Add new server", a.layout.fpx(44))

	var delRect image.Rectangle
	if len(names) > 1 {
		delRect = image.Rect(a.layout.margin, delY1, a.layout.screen.X-a.layout.margin, delY2)
		ink.DrawRect(delRect, ink.Black)
		drawCenteredText(btnFont, delRect, "Delete active profile", a.layout.fpx(44))
	}

	a.profileList.mu.Lock()
	a.profileList.rowRects = rects
	a.profileList.addRect = addRect
	a.profileList.delRect = delRect
	a.profileList.mu.Unlock()

	ink.DrawRect(a.layout.backButton, ink.Black)
	drawCenteredText(btnFont, a.layout.backButton, "Back", a.layout.fpx(44))
}

func (a *app) profileListKey(e ink.KeyEvent) bool {
	if e.Key == ink.KeyBack {
		a.screen = screenSettings
		ink.Repaint()
		return true
	}
	return false
}

func (a *app) profileListPointer(e ink.PointerEvent) bool {
	if e.State != ink.PointerDown {
		return false
	}
	if e.Point.In(a.layout.backButton) {
		a.screen = screenSettings
		ink.Repaint()
		return true
	}
	a.profileList.mu.Lock()
	rects := a.profileList.rowRects
	names := append([]string(nil), a.profileList.names...)
	active := a.profileList.active
	addRect := a.profileList.addRect
	delRect := a.profileList.delRect
	a.profileList.mu.Unlock()

	if e.Point.In(addRect) {
		a.startAddProfile()
		return true
	}
	if !delRect.Empty() && e.Point.In(delRect) {
		a.deleteActiveProfile(active)
		return true
	}
	for i, r := range rects {
		if !e.Point.In(r) {
			continue
		}
		if i < len(names) && names[i] != active {
			a.switchProfile(names[i])
		}
		return true
	}
	return false
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
	a.screen = screenMain
	ink.Repaint()
}

// deleteActiveProfile removes the active profile after a switch. The
// fileDoc promotes another profile to active automatically; we reload to
// pick it up.
func (a *app) deleteActiveProfile(name string) {
	if err := DeleteProfile(a.cfgPath, name); err != nil {
		a.profileList.mu.Lock()
		a.profileList.err = err
		a.profileList.mu.Unlock()
		ink.Repaint()
		return
	}
	a.reloadActiveConfig()
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
	if a.store != nil {
		_ = a.store.Close()
	}
	a.cfg = cfg
	a.store = store
	a.client, _ = NewClient(cfg.Host, cfg.User, cfg.Pass)
	a.connState = connUnknown
	a.refreshMainStats()
}

// startAddProfile enters the wizard in "add-profile" mode: the first
// step asks for the new profile's name, then the normal URL / user /
// pass flow runs. Saving creates the new section and marks it active.
func (a *app) startAddProfile() {
	a.wizard = wizardState{step: stepProfileName, addProfile: true}
	a.screen = screenFirstRun
	ink.OpenKeyboard("new-profile-name", 40)
}

// ---------- Self-update ----------

// updateCheckMinInterval bounds how often the background check hits
// the release endpoint. Weekly matches how often a new release is
// realistically cut and keeps background traffic low on metered
// connections. The first launch after install bypasses this because
// meta.last_update_check has never been set, so a fresh sideload
// always sees the current latest immediately.
const updateCheckMinInterval = 7 * 24 * time.Hour

// metaLastUpdateCheck is the store meta key holding the RFC3339
// timestamp of the most recent successful or failed update check.
const metaLastUpdateCheck = "last_update_check"

// backgroundUpdateCheck runs at startup when check_updates is on and
// more than updateCheckMinInterval has passed since the previous check.
// A newer release is recorded on a.update; the main screen shows a
// one-line banner when that's set. Failures are swallowed so an
// offline device never reports a "could not check" banner to the user.
func (a *app) backgroundUpdateCheck() {
	if a.store == nil {
		return
	}
	if last, ok, _ := a.store.GetMeta(metaLastUpdateCheck); ok {
		if t, err := time.Parse(time.RFC3339, last); err == nil {
			if time.Since(t) < updateCheckMinInterval {
				return
			}
		}
	}
	a.runUpdateCheck()
}

// runUpdateCheck performs one check against the configured endpoint.
// Safe to call from any goroutine; repaint is triggered when state
// changes so the main-screen banner appears.
func (a *app) runUpdateCheck() {
	a.update.mu.Lock()
	if a.update.checking {
		a.update.mu.Unlock()
		return
	}
	a.update.checking = true
	a.update.checkErr = nil
	a.update.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	newer, rel, err := CheckLatest(ctx, a.cfg.EffectiveUpdateURL(), version)

	a.update.mu.Lock()
	a.update.checking = false
	a.update.release = rel
	a.update.available = newer
	a.update.checkErr = err
	a.update.mu.Unlock()
	if a.store != nil {
		_ = a.store.SetMeta(metaLastUpdateCheck, time.Now().Format(time.RFC3339))
	}
	ink.Repaint()
}

// openUpdateScreen transitions to the dedicated update screen. If no
// release is loaded yet (user tapped "Check for updates" from an idle
// state), trigger a fresh check in the background.
func (a *app) openUpdateScreen() {
	a.screen = screenUpdate
	ink.Repaint()
	a.update.mu.Lock()
	haveRelease := a.update.release.Version != ""
	a.update.mu.Unlock()
	if !haveRelease {
		go a.runUpdateCheck()
	}
}

func (a *app) drawUpdate() {
	title := ink.OpenFont(ink.DefaultFontBold, a.layout.fpx(64), true)
	defer title.Close()
	title.SetActive(ink.Black)
	ink.DrawString(image.Point{X: a.layout.margin, Y: 140}, "Updates")

	body := ink.OpenFont(ink.DefaultFont, a.layout.fpx(32), true)
	defer body.Close()
	body.SetActive(ink.Black)

	btnFont := ink.OpenFont(ink.DefaultFontBold, a.layout.fpx(44), true)
	defer btnFont.Close()
	btnFont.SetActive(ink.Black)

	a.update.mu.Lock()
	checking := a.update.checking
	rel := a.update.release
	checkErr := a.update.checkErr
	available := a.update.available
	downloading := a.update.downloading
	downloaded := a.update.downloaded
	installErr := a.update.installErr
	installed := a.update.installed
	a.update.mu.Unlock()

	body.SetActive(ink.Black)
	ink.DrawString(image.Point{X: a.layout.margin, Y: 220}, "Installed: "+version)

	y := 280
	switch {
	case installed:
		ink.DrawString(image.Point{X: a.layout.margin, Y: y}, "Update installed: "+rel.Version)
		ink.DrawString(image.Point{X: a.layout.margin, Y: y + 50}, "Quit and relaunch pocketbeam.")
	case installErr != nil:
		ink.DrawString(image.Point{X: a.layout.margin, Y: y}, "Install failed:")
		ink.DrawString(image.Point{X: a.layout.margin, Y: y + 50}, truncate(installErr.Error(), 60))
	case downloading:
		ink.DrawString(image.Point{X: a.layout.margin, Y: y}, fmt.Sprintf("Downloading %s...", rel.Version))
		ink.DrawString(image.Point{X: a.layout.margin, Y: y + 50}, fmt.Sprintf("%d KB received", downloaded/1024))
	case checking:
		ink.DrawString(image.Point{X: a.layout.margin, Y: y}, "Checking for updates...")
	case checkErr != nil:
		ink.DrawString(image.Point{X: a.layout.margin, Y: y}, "Could not check:")
		ink.DrawString(image.Point{X: a.layout.margin, Y: y + 50}, truncate(checkErr.Error(), 60))
	case available:
		ink.DrawString(image.Point{X: a.layout.margin, Y: y}, "New version: "+rel.Version)
		if rel.SHA256 != "" {
			ink.DrawString(image.Point{X: a.layout.margin, Y: y + 50}, "sha256: "+rel.SHA256[:12]+"...")
		}
	default:
		ink.DrawString(image.Point{X: a.layout.margin, Y: y}, "You are up to date.")
	}

	// Action buttons. The primary button is either "Install now" when
	// a newer release is ready or "Check for updates" for a manual
	// refresh. Below it sits the automatic-checks toggle, which is
	// always shown so users can disable weekly checks without editing
	// the config file by hand.
	btnH := 100
	toggleH := 90
	contentW := a.layout.screen.X - 2*a.layout.margin
	toggleY2 := a.layout.backButton.Min.Y - 40
	toggleY1 := toggleY2 - toggleH
	btnY2 := toggleY1 - 30
	btnY1 := btnY2 - btnH
	primary := image.Rect(a.layout.margin, btnY1, a.layout.margin+contentW, btnY2)

	var installBtn, checkBtn image.Rectangle
	if available && !installed && !downloading {
		installBtn = primary
		ink.DrawRect(installBtn, ink.Black)
		ink.DrawRect(installBtn.Inset(2), ink.Black)
		drawCenteredText(btnFont, installBtn, "Install now", a.layout.fpx(44))
	} else if !downloading && !installed {
		checkBtn = primary
		ink.DrawRect(checkBtn, ink.Black)
		drawCenteredText(btnFont, checkBtn, "Check for updates", a.layout.fpx(44))
	}

	toggleRect := image.Rect(a.layout.margin, toggleY1, a.layout.margin+contentW, toggleY2)
	toggleLabel := "Automatic weekly checks: off  (tap to enable)"
	if a.cfg.CheckUpdates {
		toggleLabel = "Automatic weekly checks: on  (tap to disable)"
	}
	ink.DrawRect(toggleRect, ink.Black)
	drawCenteredText(btnFont, toggleRect, toggleLabel, a.layout.fpx(44))

	a.update.mu.Lock()
	a.update.installBtn = installBtn
	a.update.checkBtn = checkBtn
	a.update.toggleBtn = toggleRect
	a.update.backBtn = a.layout.backButton
	a.update.mu.Unlock()

	ink.DrawRect(a.layout.backButton, ink.Black)
	drawCenteredText(btnFont, a.layout.backButton, "Back", a.layout.fpx(44))
}

func (a *app) updateKey(e ink.KeyEvent) bool {
	if e.Key == ink.KeyBack {
		a.screen = screenSettings
		ink.Repaint()
		return true
	}
	if e.Key == ink.KeyOk {
		a.update.mu.Lock()
		hasInstall := !a.update.installBtn.Empty()
		a.update.mu.Unlock()
		if hasInstall {
			go a.runUpdateInstall()
		} else {
			go a.runUpdateCheck()
		}
		return true
	}
	return false
}

func (a *app) updatePointer(e ink.PointerEvent) bool {
	if e.State != ink.PointerDown {
		return false
	}
	a.update.mu.Lock()
	installBtn := a.update.installBtn
	checkBtn := a.update.checkBtn
	toggleBtn := a.update.toggleBtn
	backBtn := a.update.backBtn
	a.update.mu.Unlock()
	if e.Point.In(backBtn) {
		a.screen = screenSettings
		ink.Repaint()
		return true
	}
	if !installBtn.Empty() && e.Point.In(installBtn) {
		go a.runUpdateInstall()
		return true
	}
	if !checkBtn.Empty() && e.Point.In(checkBtn) {
		go a.runUpdateCheck()
		return true
	}
	if !toggleBtn.Empty() && e.Point.In(toggleBtn) {
		a.cfg.CheckUpdates = !a.cfg.CheckUpdates
		_ = SaveConfig(a.cfgPath, a.cfg)
		ink.Repaint()
		return true
	}
	return false
}

// runUpdateInstall downloads the pending release to a .new file next
// to the running binary, verifies the SHA, and renames it into place.
// The user relaunches to pick up the new version; we do not call
// ink.Exit() automatically so they can read the success message.
func (a *app) runUpdateInstall() {
	a.update.mu.Lock()
	rel := a.update.release
	a.update.downloading = true
	a.update.downloaded = 0
	a.update.installErr = nil
	a.update.installed = false
	a.update.mu.Unlock()
	ink.Repaint()

	exe, err := os.Executable()
	if err != nil {
		a.failUpdate(fmt.Errorf("locate running binary: %w", err))
		return
	}
	staged := exe + ".new"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	err = Download(ctx, rel, staged, func(n int64) {
		a.update.mu.Lock()
		a.update.downloaded = n
		a.update.mu.Unlock()
		ink.Repaint()
	})
	if err != nil {
		a.failUpdate(fmt.Errorf("download: %w", err))
		return
	}
	if err := Install(staged, exe); err != nil {
		a.failUpdate(fmt.Errorf("install: %w", err))
		return
	}
	a.update.mu.Lock()
	a.update.downloading = false
	a.update.installed = true
	a.update.available = false
	a.update.mu.Unlock()
	ink.Repaint()
}

func (a *app) failUpdate(err error) {
	a.update.mu.Lock()
	a.update.downloading = false
	a.update.installErr = err
	a.update.mu.Unlock()
	ink.Repaint()
}
