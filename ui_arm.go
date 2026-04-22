package main

import (
	"context"
	"fmt"
	"image"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
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

// libraryScannerTimeout caps how long we wait for the PocketBook scanner to
// finish indexing new books before we force the refresh dialog closed and
// return to the main screen. Scanner normally exits in a few seconds; the
// upper bound guards against a stuck scanner leaving pocketbeam modal.
const libraryScannerTimeout = 60 * time.Second

// userScannerPath is where firmware 6.x installs the PocketBook library
// scanner. The systemScannerPath fallback covers variants that only ship the
// scanner under /ebrmain.
const (
	userScannerPath   = "/mnt/ext1/system/bin/scanner.app"
	systemScannerPath = "/ebrmain/bin/scanner.app"
)

// libRefreshState drives the animated trailing-dots spinner shown on
// screenLibraryRefresh. A goroutine bumps dots on a ticker and pushes a
// partial e-ink update for just the body line, so the title and the "This
// closes on its own." line stay stable.
type libRefreshState struct {
	mu   sync.Mutex
	dots int
	stop chan struct{}
	rect image.Rectangle
}

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
	planning  bool // true while Plan() is running before downloads start
	index     int
	total     int
	title     string
	author    string
	err       error
	// unknownSizes is the count of new/updated books whose size the
	// pre-flight plan couldn't determine. Non-zero means the post-sync
	// summary tacks on a "lower-bound" note so the user knows the size
	// estimate wasn't exact.
	unknownSizes int
	bookStart    time.Time          // when the current book's download began
	cancel       context.CancelFunc // populated while active; nil otherwise
	cancelled    bool               // true when the user tapped Cancel so the summary can say so
}

// spaceWarnState parks a pre-flight plan while the oversize-confirm screen
// is up and routes the user's answer back to the sync goroutine over ch.
// ch is non-nil only while a prompt is pending.
type spaceWarnState struct {
	mu        sync.Mutex
	plan      SyncPlan
	ch        chan bool
	yesRect   image.Rectangle
	noRect    image.Rectangle
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
	total        int64 // -1 until the HTTP response's Content-Length is known
	installErr   error
	installed    bool // true once the new binary has been written to disk
	installBtn   image.Rectangle
	checkBtn     image.Rectangle
	toggleBtn    image.Rectangle // enable/disable automatic weekly checks
	backBtn      image.Rectangle
}

// profileListState holds the snapshot shown on the profile-list screen.
// Every row (including the active profile) is now a tap target that
// opens a per-profile detail panel; per-profile destructive actions
// live there, not on the list.
type profileListState struct {
	mu       sync.Mutex
	names    []string
	active   string
	err      error
	rowRects []image.Rectangle
	addRect  image.Rectangle
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
	pageSize     int
	rowRects     []image.Rectangle // paginated directory rows
	upRect       image.Rectangle   // fixed ".. (up)" row, empty when at root
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

	// settings screen: a stack of tappable rows grouped into three
	// sections. Row rects are full-width tap targets; each has a bold
	// title and a muted value subtitle. deleteToggle is a smaller pill
	// drawn inside deleteRow, left in place for the pointer handler
	// only because the whole row is tappable anyway.
	serverRow       image.Rectangle
	profileRow      image.Rectangle
	filterRow       image.Rectangle
	deleteRow       image.Rectangle
	deleteToggle    image.Rectangle
	updateRow       image.Rectangle
	serverLabelY    int
	libraryLabelY   int
	aboutLabelY     int
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
	// sc scales a reference-device (1264x1680) pixel length to the current
	// screen. Keeps every spacing in this function proportionate when the
	// layout is computed for a smaller PocketBook panel.
	sc := func(px int) int { return int(float64(px)*scale + 0.5) }
	sideMargin := sc(60)
	topSafe := sc(100)
	bottomSafe := sc(180)
	if w > 0 && w < 1200 {
		sideMargin = sc(40)
	}
	contentW := w - 2*sideMargin

	// Main Sync Now button: centered, below the last-sync summary area.
	syncY1 := topSafe + sc(420)
	syncBtn := image.Rect(sideMargin, syncY1, sideMargin+contentW, syncY1+sc(200))

	// Progress strip: fixed position below the Sync button, above the bottom
	// row. Covers counter + bar + current-book line.
	progY1 := syncBtn.Max.Y + sc(60)
	progArea := image.Rect(sideMargin, progY1, sideMargin+contentW, progY1+sc(240))
	progBar := image.Rect(sideMargin, progY1+sc(80), sideMargin+contentW, progY1+sc(130))

	// Bottom button row: Network | Settings | Quit, anchored from the bottom.
	btnH := sc(100)
	btnGap := sc(40)
	btnY2 := h - bottomSafe
	btnY1 := btnY2 - btnH
	btnW := (contentW - 2*btnGap) / 3 // 3 buttons with two gaps between them
	networkBtn := image.Rect(sideMargin, btnY1, sideMargin+btnW, btnY2)
	settingsBtn := image.Rect(networkBtn.Max.X+btnGap, btnY1, networkBtn.Max.X+btnGap+btnW, btnY2)
	quitBtn := image.Rect(w-sideMargin-btnW, btnY1, w-sideMargin, btnY2)

	// Settings screen: list of full-width tappable rows grouped into
	// SERVER / LIBRARY / ABOUT sections. Each section gets a small
	// label; rows stretch the content width and are sized to divide the
	// remaining vertical space evenly so the layout fits any panel.
	settingsTop := topSafe + sc(220)
	settingsBottom := btnY1 - sc(40)
	sectionLabelH := sc(60)
	settingsRowCount := 5
	settingsSectionCount := 3
	rowsH := (settingsBottom - settingsTop) - settingsSectionCount*sectionLabelH
	settingsRowH := rowsH / settingsRowCount

	sy := settingsTop
	serverLabelY := sy + sc(44)
	sy += sectionLabelH
	serverRow := image.Rect(sideMargin, sy, sideMargin+contentW, sy+settingsRowH)
	sy += settingsRowH
	profileRow := image.Rect(sideMargin, sy, sideMargin+contentW, sy+settingsRowH)
	sy += settingsRowH
	libraryLabelY := sy + sc(44)
	sy += sectionLabelH
	filterRow := image.Rect(sideMargin, sy, sideMargin+contentW, sy+settingsRowH)
	sy += settingsRowH
	deleteRow := image.Rect(sideMargin, sy, sideMargin+contentW, sy+settingsRowH)
	sy += settingsRowH
	aboutLabelY := sy + sc(44)
	sy += sectionLabelH
	updateRow := image.Rect(sideMargin, sy, sideMargin+contentW, sy+settingsRowH)

	// Delete-missing toggle pill: right-aligned inside deleteRow, sized
	// to read clearly as a binary control.
	toggleH := sc(60)
	toggleW := sc(120)
	toggleCY := (deleteRow.Min.Y + deleteRow.Max.Y) / 2
	toggleX2 := deleteRow.Max.X - sc(20)
	deleteToggle := image.Rect(toggleX2-toggleW, toggleCY-toggleH/2, toggleX2, toggleCY+toggleH/2)

	// Shelf picker: rows live between the header (below topSafe) and the
	// Back button (same position as bottom btnY1).
	pickerTop := topSafe + sc(220)
	pickerBottom := btnY1 - sc(40)

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
		serverRow:        serverRow,
		profileRow:       profileRow,
		filterRow:        filterRow,
		deleteRow:        deleteRow,
		deleteToggle:     deleteToggle,
		updateRow:        updateRow,
		serverLabelY:     serverLabelY,
		libraryLabelY:    libraryLabelY,
		aboutLabelY:      aboutLabelY,
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

// sy scales a Y coordinate authored for the 1264x1680 reference device to
// the current screen. Every hard-coded Y in a draw function should flow
// through this so smaller PocketBook panels (Touch HD, Touch Lux 5) stay
// proportionate instead of pushing content off the bottom.
func (s layout) sy(base int) int {
	return int(float64(base)*s.scale + 0.5)
}

// sx scales an X coordinate the same way. Used for sub-indents (e.g.,
// bullet-list content) where the offset must scale with the page margin.
func (s layout) sx(base int) int {
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
	delConfirm    deleteConfirmState
	spaceWarn     spaceWarnState
	profileList   profileListState
	profileDetail profileDetailState
	update        updateState
	libRefresh    libRefreshState
	lastSync    SyncSummary
	hasLastSync bool
	bookCount   int
	netStop           func()
	layout            layout
	connState         connectivity
	lastTap           time.Time
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
		ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(200)}, "pocketbeam")
		body.SetActive(ink.DarkGray)
		ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(260)}, "Wireless sync from a book server.")
		a.drawHairline(a.layout.margin, a.layout.screen.X-a.layout.margin, a.layout.sy(300))

		body.SetActive(ink.Black)
		ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(370)}, "Supported servers")
		body.SetActive(ink.DarkGray)
		ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(430)}, "· Calibre-Web and any OPDS catalog")
		ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(480)}, "· Nextcloud, Synology, ownCloud, WebDAV")

		body.SetActive(ink.Black)
		ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(620)}, "Press OK or tap to begin.")

	case stepProfileName:
		title.SetActive(ink.Black)
		ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(200)}, "Name this profile")
		body.SetActive(ink.DarkGray)
		ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(260)}, "Short identifier for this server (no spaces).")
		a.drawHairline(a.layout.margin, a.layout.screen.X-a.layout.margin, a.layout.sy(300))
		body.SetActive(ink.Black)
		ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(370)}, "Tap or press OK to re-open the keyboard.")
		if a.wizard.name != "" {
			body.SetActive(ink.DarkGray)
			ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(480)}, "Name: "+a.wizard.name)
		}

	case stepBackend:
		title.SetActive(ink.Black)
		ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(200)}, "Choose server type")
		body.SetActive(ink.DarkGray)
		ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(260)}, "Tap the option that matches your server.")

		rowTitleFont := ink.OpenFont(ink.DefaultFontBold, a.layout.fpx(36), true)
		defer rowTitleFont.Close()
		rowSubFont := ink.OpenFont(ink.DefaultFont, a.layout.fpx(28), true)
		defer rowSubFont.Close()

		w := a.layout.screen.X
		btnW := w - 2*a.layout.margin
		a.wizard.opdsBtn = image.Rect(a.layout.margin, a.layout.sy(360), a.layout.margin+btnW, a.layout.sy(500))
		a.wizard.webdavBtn = image.Rect(a.layout.margin, a.layout.sy(500), a.layout.margin+btnW, a.layout.sy(640))

		a.drawListRow(rowTitleFont, rowSubFont, a.wizard.opdsBtn,
			"Calibre-Web / OPDS", "Calibre-Web, COPS, and other OPDS catalogs", true)
		a.drawListRow(rowTitleFont, rowSubFont, a.wizard.webdavBtn,
			"WebDAV / Nextcloud", "Nextcloud, Synology, ownCloud, generic WebDAV", true)
		a.drawHairline(a.wizard.webdavBtn.Min.X, a.wizard.webdavBtn.Max.X, a.wizard.webdavBtn.Max.Y)

	case stepURL, stepUser, stepPass:
		title.SetActive(ink.Black)
		ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(200)}, "pocketbeam setup")
		body.SetActive(ink.DarkGray)
		var step, what string
		switch a.wizard.step {
		case stepURL:
			step, what = "Step 1 of 3", "Server URL"
		case stepUser:
			step, what = "Step 2 of 3", "Username"
		case stepPass:
			step, what = "Step 3 of 3", "Password"
		}
		ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(260)}, step+"  ·  "+what)
		a.drawHairline(a.layout.margin, a.layout.screen.X-a.layout.margin, a.layout.sy(300))
		body.SetActive(ink.Black)
		ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(370)}, "Tap or press OK to open the keyboard.")
		body.SetActive(ink.DarkGray)
		y := a.layout.sy(490)
		if a.wizard.url != "" {
			ink.DrawString(image.Point{X: a.layout.margin, Y: y}, "Server: "+truncate(a.wizard.url, 48))
			y += a.layout.sy(50)
		}
		if a.wizard.user != "" {
			ink.DrawString(image.Point{X: a.layout.margin, Y: y}, "User: "+truncate(a.wizard.user, 48))
		}

	case stepTesting:
		title.SetActive(ink.Black)
		ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(300)}, "Testing connection")
		body.SetActive(ink.DarkGray)
		ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(370)}, truncate(a.wizard.url, 55))
		ink.ShowHourglassAt(image.Point{X: a.layout.margin, Y: a.layout.sy(480)})

	case stepError:
		title.SetActive(ink.Black)
		ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(200)}, "Connection failed")
		body.SetActive(ink.DarkGray)
		ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(260)}, "Check that the server URL and credentials are correct.")
		a.drawHairline(a.layout.margin, a.layout.screen.X-a.layout.margin, a.layout.sy(300))
		msg := "Unknown error"
		if a.wizard.err != nil {
			msg = a.wizard.err.Error()
		}
		body.SetActive(ink.Black)
		ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(370)}, truncate(msg, 60))
		body.SetActive(ink.DarkGray)
		ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(620)}, "Press OK or tap to try again.")
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

	hero := ink.OpenFont(ink.DefaultFontBold, a.layout.fpx(48), true)
	defer hero.Close()

	body := ink.OpenFont(ink.DefaultFont, a.layout.fpx(32), true)
	defer body.Close()

	small := ink.OpenFont(ink.DefaultFont, a.layout.fpx(26), true)
	defer small.Close()

	btnFont := ink.OpenFont(ink.DefaultFontBold, a.layout.fpx(44), true)
	defer btnFont.Close()

	// Title + host subtitle (host in muted gray so the filter/host context
	// is present but secondary to the action area).
	title.SetActive(ink.Black)
	ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(140)}, "pocketbeam")
	small.SetActive(ink.DarkGray)
	subtitle := a.cfg.Host
	if a.cfg.Backend == BackendWebDAV {
		p := a.cfg.Path
		if p == "" {
			p = "/"
		}
		subtitle += "  ·  " + p
	} else {
		subtitle += "  ·  " + a.cfg.FilterLabel()
	}
	ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(190)}, truncate(subtitle, 60))

	// Hairline under the header.
	a.drawHairline(a.layout.margin, a.layout.screen.X-a.layout.margin, a.layout.sy(220))

	// Status hero: emphatic primary line + muted supporting details.
	statusY := a.layout.sy(300)
	hero.SetActive(ink.Black)
	body.SetActive(ink.DarkGray)
	if a.hasLastSync {
		ink.DrawString(image.Point{X: a.layout.margin, Y: statusY},
			"Last synced "+humanAgo(a.lastSync.At))
		ink.DrawString(image.Point{X: a.layout.margin, Y: statusY + a.layout.sy(60)},
			fmt.Sprintf("%d books in library", a.bookCount))
		if a.lastSync.Failed > 0 {
			ink.DrawString(image.Point{X: a.layout.margin, Y: statusY + a.layout.sy(110)},
				fmt.Sprintf("%d failed — will retry next sync", a.lastSync.Failed))
		}
	} else {
		ink.DrawString(image.Point{X: a.layout.margin, Y: statusY}, "Not yet synced")
		ink.DrawString(image.Point{X: a.layout.margin, Y: statusY + a.layout.sy(60)},
			"Tap Sync Now to begin")
	}

	// Update badge: dark-gray filled strip sitting above the sync button
	// when a newer release is waiting. Informational only; the install
	// lives in Settings → About.
	a.update.mu.Lock()
	updateAvail := a.update.available
	updateVer := a.update.release.Version
	a.update.mu.Unlock()
	if updateAvail && updateVer != "" {
		badge := image.Rect(
			a.layout.margin,
			a.layout.syncButton.Min.Y-a.layout.sy(80),
			a.layout.screen.X-a.layout.margin,
			a.layout.syncButton.Min.Y-a.layout.sy(20),
		)
		ink.FillArea(badge, ink.LightGray)
		small.SetActive(ink.Black)
		drawCenteredText(small, badge,
			"Update "+updateVer+" available in Settings",
			a.layout.fpx(26))
	}

	// Primary action: Sync Now (or Stop during an active sync). Kept as
	// the screen's most prominent element — double border signals
	// primary.
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

	// Bottom action row: secondary actions styled with a single border so
	// they read as lighter than the primary Sync button.
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
	planning := a.sync.planning
	idx := a.sync.index
	total := a.sync.total
	curTitle := a.sync.title
	curAuthor := a.sync.author
	syncErr := a.sync.err
	bookStart := a.sync.bookStart
	unknown := a.sync.unknownSizes
	a.sync.mu.Unlock()

	ink.FillArea(a.layout.progressArea, ink.White)

	switch {
	case planning:
		body.SetActive(ink.Black)
		ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.progressArea.Min.Y + a.layout.sy(30)},
			"Checking remote catalog and free space...")
	case active:
		body.SetActive(ink.Black)
		counter := fmt.Sprintf("%d / %d", idx, total)
		if !bookStart.IsZero() {
			if elapsed := time.Since(bookStart); elapsed >= time.Second {
				counter += "  (" + formatElapsed(elapsed) + ")"
			}
		}
		ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.progressArea.Min.Y + a.layout.sy(30)}, counter)
		ink.DrawRect(a.layout.progressBar, ink.Black)
		if total > 0 {
			fillW := (a.layout.progressBar.Dx() - 6) * idx / total
			ink.FillArea(image.Rect(
				a.layout.progressBar.Min.X + a.layout.sx(3),
				a.layout.progressBar.Min.Y + a.layout.sy(3),
				a.layout.progressBar.Min.X + a.layout.sx(3)+fillW,
				a.layout.progressBar.Max.Y - a.layout.sy(3),
			), ink.DarkGray)
		}
		ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.progressArea.Min.Y + a.layout.sy(180)}, truncate(curAuthor+": "+curTitle, 60))
	case syncErr != nil:
		body.SetActive(ink.Black)
		ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.progressArea.Min.Y + a.layout.sy(30)}, "Last error:")
		ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.progressArea.Min.Y + a.layout.sy(80)}, truncate(syncErr.Error(), 60))
	case unknown > 0:
		body.SetActive(ink.Black)
		ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.progressArea.Min.Y + a.layout.sy(30)},
			fmt.Sprintf("%d book(s) had unknown size; space estimate was a lower bound.", unknown))
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
	a.sync.unknownSizes = 0
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
	a.sync.planning = true
	a.sync.mu.Unlock()
	defer cancel()
	a.refreshProgress()

	plan, books, err := Plan(ctx, src, a.store, a.cfg.Library, opts)
	a.sync.mu.Lock()
	a.sync.planning = false
	a.sync.mu.Unlock()
	if err != nil {
		a.finishSyncWithError(err)
		a.refreshMainStats()
		ink.Repaint()
		return
	}
	if ok, _ := plan.Fits(); !ok {
		if !a.confirmSpace(plan) {
			a.sync.mu.Lock()
			a.sync.active = false
			a.sync.cancel = nil
			wasCancelled := a.sync.cancelled
			a.sync.cancelled = false
			if wasCancelled {
				a.sync.err = fmt.Errorf("Sync stopped.")
			} else {
				a.sync.err = fmt.Errorf("Not enough free space; sync cancelled.")
			}
			a.sync.mu.Unlock()
			a.refreshMainStats()
			a.screen = screenMain
			ink.Repaint()
			return
		}
	}
	a.sync.mu.Lock()
	a.sync.unknownSizes = plan.UnknownSizes
	a.sync.mu.Unlock()

	opts.PrefetchedBooks = books
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
	// If the sync actually changed the library, hand off to the stock
	// PocketBook scanner so new covers/titles show up without the user
	// having to navigate into Library first. Scanner takes foreground
	// focus while it runs; show an auto-closing "Refreshing library"
	// dialog so the hand-off is explained rather than looking like a
	// freeze. If nothing changed (or the sync failed), skip straight to
	// main as before.
	if !wasCancelled && (res.Downloaded > 0 || res.Deleted > 0) {
		a.screen = screenLibraryRefresh
		a.startLibRefreshSpinner()
		ink.Repaint()
		go a.runLibraryScanner()
		return
	}
	// In case the confirm screen is still up (e.g. user closed the device
	// with the prompt showing), flip back to main.
	a.screen = screenMain
	ink.Repaint()
}

// runLibraryScanner launches the PocketBook scanner to re-index
// /mnt/ext1/Books, then returns to the main screen. Called from a goroutine
// after a sync that downloaded or deleted books. The scanner takes
// foreground focus for the duration; we only use its exit to drive our own
// auto-close transition. Any failure (missing binary, exec error, timeout)
// falls through to screenMain without surfacing to the user - the worst
// case is the old "open Library to see new books" behaviour.
func (a *app) runLibraryScanner() {
	defer func() {
		a.stopLibRefreshSpinner()
		a.screen = screenMain
		ink.Repaint()
	}()

	path := userScannerPath
	if _, err := os.Stat(path); err != nil {
		path = systemScannerPath
		if _, err := os.Stat(path); err != nil {
			log.Printf("library scanner: no scanner.app found at %s or %s", userScannerPath, systemScannerPath)
			return
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), libraryScannerTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path)
	if err := cmd.Run(); err != nil {
		log.Printf("library scanner: %v", err)
	}
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

// breadcrumbPath + truncate live in breadcrumb.go / util.go so both
// the InkView UI and the amd64 tests can reach them.

// ---------- Settings screen ----------

func (a *app) drawSettings() {
	title := ink.OpenFont(ink.DefaultFontBold, a.layout.fpx(64), true)
	defer title.Close()

	rowTitleFont := ink.OpenFont(ink.DefaultFontBold, a.layout.fpx(36), true)
	defer rowTitleFont.Close()

	rowSubFont := ink.OpenFont(ink.DefaultFont, a.layout.fpx(28), true)
	defer rowSubFont.Close()

	sectionFont := ink.OpenFont(ink.DefaultFontBold, a.layout.fpx(24), true)
	defer sectionFont.Close()

	btnFont := ink.OpenFont(ink.DefaultFontBold, a.layout.fpx(44), true)
	defer btnFont.Close()

	// Header
	title.SetActive(ink.Black)
	ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(140)}, "Settings")

	// SERVER section
	a.drawSectionLabel(sectionFont, "SERVER", a.layout.serverLabelY)
	a.drawListRow(rowTitleFont, rowSubFont, a.layout.serverRow,
		"Server", truncate(a.cfg.Host, 40), true)
	a.drawListRow(rowTitleFont, rowSubFont, a.layout.profileRow,
		"Profile", a.cfg.Profile, true)
	a.drawHairline(a.layout.profileRow.Min.X, a.layout.profileRow.Max.X, a.layout.profileRow.Max.Y)

	// LIBRARY section
	a.drawSectionLabel(sectionFont, "LIBRARY", a.layout.libraryLabelY)
	filterTitle := "Sync filter"
	var filterValue string
	if a.cfg.Backend == BackendWebDAV {
		filterTitle = "Sync folder"
		filterValue = a.cfg.Path
		if filterValue == "" {
			filterValue = "/"
		}
	} else {
		filterValue = a.cfg.FilterLabel()
	}
	a.drawListRow(rowTitleFont, rowSubFont, a.layout.filterRow,
		filterTitle, filterValue, true)

	deleteSub := "Keep books removed on server"
	if a.cfg.DeleteMissing {
		deleteSub = "Remove books deleted on server"
	}
	a.drawListRow(rowTitleFont, rowSubFont, a.layout.deleteRow,
		"Delete missing", deleteSub, false)
	a.drawToggle(a.layout.deleteToggle, a.cfg.DeleteMissing)
	a.drawHairline(a.layout.deleteRow.Min.X, a.layout.deleteRow.Max.X, a.layout.deleteRow.Max.Y)

	// ABOUT section
	a.drawSectionLabel(sectionFont, "ABOUT", a.layout.aboutLabelY)
	updateTitle := "Check for updates"
	updateSub := "Current version " + version
	a.update.mu.Lock()
	if a.update.available && a.update.release.Version != "" {
		updateTitle = "Install update " + a.update.release.Version
		updateSub = "Current version " + version
	}
	a.update.mu.Unlock()
	a.drawListRow(rowTitleFont, rowSubFont, a.layout.updateRow,
		updateTitle, updateSub, true)
	a.drawHairline(a.layout.updateRow.Min.X, a.layout.updateRow.Max.X, a.layout.updateRow.Max.Y)

	// Back button in the bottom-left, matching other screens.
	ink.DrawRect(a.layout.backButton, ink.Black)
	btnFont.SetActive(ink.Black)
	drawCenteredText(btnFont, a.layout.backButton, "Back", a.layout.fpx(44))
}

// drawSectionLabel paints a small, muted all-caps header above a
// group of rows.
func (a *app) drawSectionLabel(f *ink.Font, label string, y int) {
	f.SetActive(ink.DarkGray)
	ink.DrawString(image.Point{X: a.layout.margin, Y: y}, label)
}

// drawListRow paints one full-width tappable row with a bold title on
// top, an optional muted subtitle underneath, a hairline separator along
// the top edge, and an optional right-edge chevron for drill-downs.
// Used by Settings, Shelf/Dir pickers, Profile list, and the first-run
// backend picker so every list in the app shares one visual language.
func (a *app) drawListRow(titleF, subF *ink.Font, r image.Rectangle, title, subtitle string, chevron bool) {
	a.drawHairline(r.Min.X, r.Max.X, r.Min.Y)

	titleH := a.layout.fpx(36)
	subH := a.layout.fpx(28)
	gap := a.layout.sy(12)

	if subtitle == "" {
		titleY := r.Min.Y + (r.Dy()+titleH)/2
		titleF.SetActive(ink.Black)
		ink.DrawString(image.Point{X: r.Min.X, Y: titleY}, truncate(title, 48))
	} else {
		totalH := titleH + gap + subH
		titleY := r.Min.Y + (r.Dy()-totalH)/2 + titleH
		subY := titleY + gap + subH
		titleF.SetActive(ink.Black)
		ink.DrawString(image.Point{X: r.Min.X, Y: titleY}, truncate(title, 40))
		subF.SetActive(ink.DarkGray)
		ink.DrawString(image.Point{X: r.Min.X, Y: subY}, truncate(subtitle, 48))
	}

	if chevron {
		chevronF := ink.OpenFont(ink.DefaultFontBold, a.layout.fpx(48), true)
		defer chevronF.Close()
		chevronF.SetActive(ink.DarkGray)
		ink.DrawString(
			image.Point{X: r.Max.X - a.layout.sx(30), Y: r.Min.Y + (r.Dy()+a.layout.fpx(48))/2 - a.layout.sy(8)},
			">")
	}
}

// drawHairline paints a 1-pixel light-gray horizontal rule. The shared
// separator element for list rows and section dividers across every
// screen.
func (a *app) drawHairline(x1, x2, y int) {
	ink.FillArea(image.Rect(x1, y, x2, y+1), ink.LightGray)
}

// drawToggle paints a two-position pill indicating a boolean
// setting. On: filled dark with the thumb on the right. Off: filled light
// with the thumb on the left. Sharp-edged because rounded rects are not
// part of the ink package and fake rounding reads worse on e-ink than a
// clean rectangle.
func (a *app) drawToggle(r image.Rectangle, on bool) {
	ink.DrawRect(r, ink.Black)
	inset := r.Inset(a.layout.sx(4))
	thumbW := inset.Dy()
	if on {
		ink.FillArea(inset, ink.DarkGray)
		thumb := image.Rect(inset.Max.X-thumbW, inset.Min.Y, inset.Max.X, inset.Max.Y)
		ink.FillArea(thumb, ink.Black)
		ink.DrawRect(thumb, ink.White)
	} else {
		ink.FillArea(inset, ink.LightGray)
		thumb := image.Rect(inset.Min.X, inset.Min.Y, inset.Min.X+thumbW, inset.Max.Y)
		ink.FillArea(thumb, ink.White)
		ink.DrawRect(thumb, ink.Black)
	}
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
	case p.In(a.layout.serverRow):
		a.startChangeInfo()
		return true
	case p.In(a.layout.profileRow):
		a.openProfileList()
		return true
	case p.In(a.layout.filterRow):
		if a.cfg.Backend == BackendWebDAV {
			a.openDirPicker(a.cfg.Path)
		} else {
			a.openShelfPicker()
		}
		return true
	case p.In(a.layout.deleteRow):
		a.cfg.DeleteMissing = !a.cfg.DeleteMissing
		_ = SaveConfig(a.cfgPath, a.cfg)
		ink.Repaint()
		return true
	case p.In(a.layout.updateRow):
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
	// Step by pageSize (written during draw) rather than len(rowRects),
	// because the last page can be shorter than a full window. Using
	// rowRects as the step here would skip forward by only the partial
	// count and land on a non-page-aligned offset.
	step := a.picker.pageSize
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

	var prevPageRect, nextPageRect image.Rectangle

	if loading {
		ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(300)}, "Loading...")
		ink.ShowHourglassAt(image.Point{X: a.layout.margin, Y: a.layout.sy(360)})
	} else if pickerErr != nil {
		ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(300)}, "Could not load feed:")
		ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(350)}, truncate(pickerErr.Error(), 60))
	} else {
		rowH := a.layout.sy(110)
		pageBtnH := a.layout.sy(60)
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

		// Up-row: fixed above the paginated window so the user can go
		// up from any page. Rendered as a full-width list row with a
		// left-aligned "< Back" label so it shares the app-wide row
		// idiom; still visually distinct from the subsection chevron
		// rows because its chevron points the other way.
		listTop := areaTop
		var upRect image.Rectangle
		if !atRoot {
			upRect = image.Rect(a.layout.margin, listTop, a.layout.screen.X-a.layout.margin, listTop+rowH-a.layout.sy(20))
			a.drawHairline(upRect.Min.X, upRect.Max.X, upRect.Min.Y)
			parent := "previous level"
			if stackLen > 0 {
				parent = stackTitles[stackLen-1]
			}
			rowTitleFont.SetActive(ink.Black)
			titleY := upRect.Min.Y + (upRect.Dy()+a.layout.fpx(36))/2
			ink.DrawString(image.Point{X: upRect.Min.X + a.layout.sx(40), Y: titleY},
				"< Back to "+truncate(parent, 32))
			listTop += rowH
		}

		// Paging applies only to subsection rows. pageSize is the true
		// viewport (used for Prev/Next steps and the "Page N" label);
		// the last page may render fewer rows.
		visibleArea := areaBottom - listTop
		pageSize := (visibleArea - pageBtnH - a.layout.sy(20)) / rowH
		if pageSize < 1 {
			pageSize = 1
		}
		p := paginate(len(visibleSubs), pageSize, offset)
		offset = p.offset
		end := p.end

		rects := make([]image.Rectangle, 0, end-offset)
		for i := offset; i < end; i++ {
			sub := visibleSubs[i]
			var subtitle string
			if sub.CountKnown {
				subtitle = fmt.Sprintf("%d books", sub.Count)
			}
			y1 := listTop + (i-offset)*rowH
			rect := image.Rect(a.layout.margin, y1, a.layout.screen.X-a.layout.margin, y1+rowH)
			rects = append(rects, rect)
			a.drawListRow(rowTitleFont, rowSubFont, rect, sub.Name, subtitle, true)
		}
		if n := len(rects); n > 0 {
			last := rects[n-1]
			a.drawHairline(last.Min.X, last.Max.X, last.Max.Y)
		}

		// Page nav: subtle subtle single-border buttons with a muted
		// page indicator between them. Shown only when subsections
		// overflow a page.
		if len(visibleSubs) > pageSize {
			btnY1 := listTop + pageSize*rowH + a.layout.sy(20)
			btnY2 := btnY1 + pageBtnH
			contentW := a.layout.screen.X - 2*a.layout.margin
			navBtnW := contentW / 4
			if offset > 0 {
				prevPageRect = image.Rect(a.layout.margin, btnY1, a.layout.margin+navBtnW, btnY2)
				ink.DrawRect(prevPageRect, ink.Black)
				drawCenteredText(btnFont, prevPageRect, "< Prev", a.layout.fpx(44))
			}
			if end < len(visibleSubs) {
				nextPageRect = image.Rect(a.layout.screen.X-a.layout.margin-navBtnW, btnY1, a.layout.screen.X-a.layout.margin, btnY2)
				ink.DrawRect(nextPageRect, ink.Black)
				drawCenteredText(btnFont, nextPageRect, "Next >", a.layout.fpx(44))
			}
			page := offset/pageSize + 1
			total := (len(visibleSubs) + pageSize - 1) / pageSize
			pageLabel := fmt.Sprintf("Page %d of %d  ·  %d items", page, total, len(visibleSubs))
			smallFont.SetActive(ink.DarkGray)
			pageLabelW := ink.StringWidth(pageLabel)
			ink.DrawString(
				image.Point{X: (a.layout.screen.X - pageLabelW) / 2, Y: btnY1 + (btnY2-btnY1+a.layout.fpx(26))/2},
				pageLabel,
			)
		}

		a.picker.mu.Lock()
		a.picker.offset = offset
		a.picker.pageSize = pageSize
		a.picker.rowRects = rects
		a.picker.prevPageRect = prevPageRect
		a.picker.nextPageRect = nextPageRect
		a.picker.upRect = upRect
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
	offset := a.picker.offset
	prev := a.picker.prevPageRect
	next := a.picker.nextPageRect
	upRect := a.picker.upRect
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

	rowTitleFont := ink.OpenFont(ink.DefaultFontBold, a.layout.fpx(36), true)
	defer rowTitleFont.Close()
	rowSubFont := ink.OpenFont(ink.DefaultFont, a.layout.fpx(28), true)
	defer rowSubFont.Close()
	smallFont := ink.OpenFont(ink.DefaultFont, a.layout.fpx(26), true)
	defer smallFont.Close()

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
	ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(140)}, "Select folder")

	smallFont.SetActive(ink.DarkGray)
	ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(210)}, truncate(path, 60))
	a.drawHairline(a.layout.margin, a.layout.screen.X-a.layout.margin, a.layout.sy(240))

	var prevPageRect, nextPageRect image.Rectangle

	if loading {
		body.SetActive(ink.Black)
		ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(300)}, "Loading...")
		ink.ShowHourglassAt(image.Point{X: a.layout.margin, Y: a.layout.sy(360)})
	} else if pickErr != nil {
		body.SetActive(ink.Black)
		ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(300)}, "Could not list folder:")
		ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(350)}, truncate(pickErr.Error(), 60))
	} else {
		rowH := a.layout.sy(110)
		pageBtnH := a.layout.sy(60)
		areaTop := a.layout.pickerAreaTop
		// Reserve space at the bottom for the "Sync this folder" button.
		selectBtnH := a.layout.sy(100)
		areaBottom := a.layout.pickerAreaBottom - selectBtnH - a.layout.sy(40)

		atRoot := path == "/" || path == ""

		// Up-row: full-width list-row styled, "<" on the left.
		listTop := areaTop
		var upRect image.Rectangle
		if !atRoot {
			upRect = image.Rect(a.layout.margin, listTop, a.layout.screen.X-a.layout.margin, listTop+rowH-a.layout.sy(20))
			a.drawHairline(upRect.Min.X, upRect.Max.X, upRect.Min.Y)
			rowTitleFont.SetActive(ink.Black)
			titleY := upRect.Min.Y + (upRect.Dy()+a.layout.fpx(36))/2
			parent := dirParent(path)
			ink.DrawString(image.Point{X: upRect.Min.X + a.layout.sx(40), Y: titleY},
				"< Back to "+truncate(parent, 32))
			listTop += rowH
		}

		visibleArea := areaBottom - listTop
		pageSize := (visibleArea - pageBtnH - a.layout.sy(20)) / rowH
		if pageSize < 1 {
			pageSize = 1
		}
		p := paginate(len(dirs), pageSize, offset)
		offset = p.offset
		end := p.end

		rects := make([]image.Rectangle, 0, end-offset)
		for i := offset; i < end; i++ {
			y1 := listTop + (i-offset)*rowH
			rect := image.Rect(a.layout.margin, y1, a.layout.screen.X-a.layout.margin, y1+rowH)
			rects = append(rects, rect)
			a.drawListRow(rowTitleFont, rowSubFont, rect, dirs[i], "", true)
		}
		if n := len(rects); n > 0 {
			last := rects[n-1]
			a.drawHairline(last.Min.X, last.Max.X, last.Max.Y)
		}
		if len(dirs) > pageSize {
			btnY1 := listTop + pageSize*rowH + a.layout.sy(20)
			btnY2 := btnY1 + pageBtnH
			contentW := a.layout.screen.X - 2*a.layout.margin
			navBtnW := contentW / 4
			if offset > 0 {
				prevPageRect = image.Rect(a.layout.margin, btnY1, a.layout.margin+navBtnW, btnY2)
				ink.DrawRect(prevPageRect, ink.Black)
				drawCenteredText(btnFont, prevPageRect, "< Prev", a.layout.fpx(44))
			}
			if end < len(dirs) {
				nextPageRect = image.Rect(a.layout.screen.X-a.layout.margin-navBtnW, btnY1, a.layout.screen.X-a.layout.margin, btnY2)
				ink.DrawRect(nextPageRect, ink.Black)
				drawCenteredText(btnFont, nextPageRect, "Next >", a.layout.fpx(44))
			}
			page := offset/pageSize + 1
			total := (len(dirs) + pageSize - 1) / pageSize
			pageLabel := fmt.Sprintf("Page %d of %d  ·  %d items", page, total, len(dirs))
			smallFont.SetActive(ink.DarkGray)
			pageLabelW := ink.StringWidth(pageLabel)
			ink.DrawString(
				image.Point{X: (a.layout.screen.X - pageLabelW) / 2, Y: btnY1 + (btnY2-btnY1+a.layout.fpx(26))/2},
				pageLabel,
			)
		}

		a.dirPicker.mu.Lock()
		a.dirPicker.offset = offset
		a.dirPicker.pageSize = pageSize
		a.dirPicker.rowRects = rects
		a.dirPicker.upRect = upRect
		a.dirPicker.prevPageRect = prevPageRect
		a.dirPicker.nextPageRect = nextPageRect
		a.dirPicker.mu.Unlock()

		// Select button sits just above the Back button.
		selectY2 := a.layout.backButton.Min.Y - a.layout.sy(40)
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

// dirParent returns a human-readable label for the parent of an absolute
// WebDAV path. The root is shown as "/".
func dirParent(p string) string {
	if p == "" || p == "/" {
		return "/"
	}
	p = strings.TrimSuffix(p, "/")
	i := strings.LastIndex(p, "/")
	if i < 0 {
		return "/"
	}
	if i == 0 {
		return "/"
	}
	return p[strings.LastIndex(p[:i], "/")+1 : i]
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
	upRect := a.dirPicker.upRect
	a.dirPicker.mu.Unlock()

	if !upRect.Empty() && e.Point.In(upRect) {
		a.openDirPicker(parentDir(path))
		return true
	}
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

	for i, r := range rects {
		if !e.Point.In(r) {
			continue
		}
		abs := offset + i
		if abs >= 0 && abs < len(dirs) {
			a.openDirPicker(normaliseRoot(path) + "/" + dirs[abs])
			return true
		}
	}
	return false
}

// dirPickerPage scrolls the directory list by one page and repaints.
func (a *app) dirPickerPage(direction int) {
	a.dirPicker.mu.Lock()
	step := a.dirPicker.pageSize
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

// ---------- Pre-flight space-warn confirmation ----------

// confirmSpace parks the plan on the UI, flips to the oversize-confirm
// screen, and blocks the sync goroutine until the user answers. Returns
// true to proceed with the download anyway, false to cancel the sync.
func (a *app) confirmSpace(plan SyncPlan) bool {
	a.spaceWarn.mu.Lock()
	a.spaceWarn.plan = plan
	a.spaceWarn.ch = make(chan bool, 1)
	ch := a.spaceWarn.ch
	a.spaceWarn.mu.Unlock()
	a.screen = screenSpaceWarn
	ink.Repaint()
	return <-ch
}

// answerSpace replies to an in-flight oversize prompt. Safe to call when
// no prompt is pending; it is a no-op.
func (a *app) answerSpace(ok bool) {
	a.spaceWarn.mu.Lock()
	ch := a.spaceWarn.ch
	a.spaceWarn.ch = nil
	a.spaceWarn.mu.Unlock()
	if ch == nil {
		return
	}
	ch <- ok
	a.screen = screenMain
	ink.Repaint()
}

// formatBytes renders b as a short human-readable size. Binary units
// (KiB/MiB/GiB) are skipped in favour of base-10 because free-space
// estimates are already approximate and base-10 matches how every
// PocketBook file dialog phrases sizes.
func formatBytes(b int64) string {
	if b < 0 {
		b = 0
	}
	const (
		kb = 1000
		mb = 1000 * kb
		gb = 1000 * mb
	)
	switch {
	case b >= gb:
		return fmt.Sprintf("%.1f GB", float64(b)/float64(gb))
	case b >= mb:
		return fmt.Sprintf("%.0f MB", float64(b)/float64(mb))
	case b >= kb:
		return fmt.Sprintf("%.0f KB", float64(b)/float64(kb))
	default:
		return fmt.Sprintf("%d B", b)
	}
}

func (a *app) drawSpaceWarn() {
	title := ink.OpenFont(ink.DefaultFontBold, a.layout.fpx(64), true)
	defer title.Close()
	title.SetActive(ink.Black)

	body := ink.OpenFont(ink.DefaultFont, a.layout.fpx(32), true)
	defer body.Close()
	body.SetActive(ink.Black)

	btnFont := ink.OpenFont(ink.DefaultFontBold, a.layout.fpx(44), true)
	defer btnFont.Close()
	btnFont.SetActive(ink.Black)

	a.spaceWarn.mu.Lock()
	plan := a.spaceWarn.plan
	a.spaceWarn.mu.Unlock()

	ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(140)}, "Not enough space")
	body.SetActive(ink.DarkGray)
	ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(200)},
		"This sync needs more room than the device has.")
	a.drawHairline(a.layout.margin, a.layout.screen.X-a.layout.margin, a.layout.sy(240))

	hero := ink.OpenFont(ink.DefaultFontBold, a.layout.fpx(40), true)
	defer hero.Close()
	hero.SetActive(ink.Black)
	newCount := len(plan.NewBooks) + len(plan.UpdatedBooks)
	ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(310)},
		fmt.Sprintf("%d books · %s needed", newCount, formatBytes(plan.DownloadBytes)))
	body.SetActive(ink.DarkGray)
	ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(365)},
		fmt.Sprintf("%s free on device", formatBytes(plan.FreeBytes)))

	y := a.layout.sy(440)
	if plan.UnknownSizes > 0 {
		ink.DrawString(image.Point{X: a.layout.margin, Y: y},
			fmt.Sprintf("%d books had unknown size; total is a lower bound.", plan.UnknownSizes))
		y += a.layout.sy(50)
	}
	if plan.ReclaimableBytes > 0 {
		ink.DrawString(image.Point{X: a.layout.margin, Y: y},
			fmt.Sprintf("Up to %s freed after delete-missing.",
				formatBytes(plan.ReclaimableBytes)))
		y += a.layout.sy(50)
	}
	y += a.layout.sy(30)
	body.SetActive(ink.Black)
	ink.DrawString(image.Point{X: a.layout.margin, Y: y},
		"Download anyway? Partial syncs are safe — the device")
	ink.DrawString(image.Point{X: a.layout.margin, Y: y + a.layout.sy(45)},
		"just stops when the disk fills up.")

	btnH := a.layout.sy(100)
	btnY2 := a.layout.backButton.Max.Y
	btnY1 := btnY2 - btnH
	contentW := a.layout.screen.X - 2*a.layout.margin
	half := (contentW - a.layout.sx(40)) / 2
	yesRect := image.Rect(a.layout.margin, btnY1, a.layout.margin+half, btnY2)
	noRect := image.Rect(a.layout.screen.X-a.layout.margin-half, btnY1, a.layout.screen.X-a.layout.margin, btnY2)

	ink.DrawRect(yesRect, ink.Black)
	ink.DrawRect(yesRect.Inset(2), ink.Black)
	drawCenteredText(btnFont, yesRect, "Download", a.layout.fpx(44))

	ink.DrawRect(noRect, ink.Black)
	drawCenteredText(btnFont, noRect, "Cancel", a.layout.fpx(44))

	a.spaceWarn.mu.Lock()
	a.spaceWarn.yesRect = yesRect
	a.spaceWarn.noRect = noRect
	a.spaceWarn.mu.Unlock()
}

func (a *app) spaceWarnKey(e ink.KeyEvent) bool {
	switch e.Key {
	case ink.KeyOk:
		a.answerSpace(true)
		return true
	case ink.KeyBack:
		a.answerSpace(false)
		return true
	}
	return false
}

func (a *app) spaceWarnPointer(e ink.PointerEvent) bool {
	if e.State != ink.PointerDown {
		return false
	}
	a.spaceWarn.mu.Lock()
	yes := a.spaceWarn.yesRect
	no := a.spaceWarn.noRect
	a.spaceWarn.mu.Unlock()
	if e.Point.In(yes) {
		a.answerSpace(true)
		return true
	}
	if e.Point.In(no) {
		a.answerSpace(false)
		return true
	}
	return false
}

// ---------- Library-refresh dialog ----------

// drawLibraryRefresh paints a centred informational dialog shown after a
// sync that changed the library. The actual work happens in
// runLibraryScanner; scanner.app takes foreground focus as soon as it
// starts, so this draw pass is what the user sees in the ~instant between
// sync completion and the scanner UI appearing. It also serves as the
// backdrop the user returns to when scanner exits, right before the
// goroutine flips back to screenMain.
func (a *app) drawLibraryRefresh() {
	title := ink.OpenFont(ink.DefaultFontBold, a.layout.fpx(54), true)
	defer title.Close()
	title.SetActive(ink.Black)

	body := ink.OpenFont(ink.DefaultFont, a.layout.fpx(32), true)
	defer body.Close()
	body.SetActive(ink.Black)

	title.SetActive(ink.Black)
	ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(300)}, "Refreshing library")
	body.SetActive(ink.Black)

	a.libRefresh.mu.Lock()
	dots := a.libRefresh.dots
	rect := image.Rect(a.layout.margin, a.layout.sy(380), a.layout.screen.X-a.layout.margin, a.layout.sy(430))
	a.libRefresh.rect = rect
	a.libRefresh.mu.Unlock()

	ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(400)}, "Indexing new books on the device"+strings.Repeat(".", dots))
	ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(450)}, "This closes on its own.")
}

// refreshLibRefreshDots redraws just the "Indexing..." body line with the
// current dot count and pushes a partial e-ink update. Called from the
// spinner ticker goroutine.
func (a *app) refreshLibRefreshDots() {
	body := ink.OpenFont(ink.DefaultFont, a.layout.fpx(32), true)
	defer body.Close()
	body.SetActive(ink.Black)

	a.libRefresh.mu.Lock()
	dots := a.libRefresh.dots
	rect := a.libRefresh.rect
	a.libRefresh.mu.Unlock()
	if rect.Empty() {
		return
	}

	ink.FillArea(rect, ink.White)
	ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(400)}, "Indexing new books on the device"+strings.Repeat(".", dots))
	ink.PartialUpdate(rect)
}

// startLibRefreshSpinner kicks off the trailing-dots animation on
// screenLibraryRefresh. Idempotent: a second call stops the previous ticker
// before starting a new one.
func (a *app) startLibRefreshSpinner() {
	a.libRefresh.mu.Lock()
	if a.libRefresh.stop != nil {
		close(a.libRefresh.stop)
	}
	stop := make(chan struct{})
	a.libRefresh.stop = stop
	a.libRefresh.dots = 0
	a.libRefresh.mu.Unlock()

	go func() {
		t := time.NewTicker(600 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				a.libRefresh.mu.Lock()
				a.libRefresh.dots = (a.libRefresh.dots + 1) % 4
				a.libRefresh.mu.Unlock()
				a.refreshLibRefreshDots()
			}
		}
	}()
}

// stopLibRefreshSpinner halts the ticker started by startLibRefreshSpinner.
// Safe to call when no spinner is running.
func (a *app) stopLibRefreshSpinner() {
	a.libRefresh.mu.Lock()
	if a.libRefresh.stop != nil {
		close(a.libRefresh.stop)
		a.libRefresh.stop = nil
	}
	a.libRefresh.mu.Unlock()
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

	ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(140)}, "Confirm deletion")
	body.SetActive(ink.DarkGray)
	ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(200)},
		fmt.Sprintf("%d books are no longer on the server.", len(pending)))
	a.drawHairline(a.layout.margin, a.layout.screen.X-a.layout.margin, a.layout.sy(240))

	body.SetActive(ink.Black)
	ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(310)},
		"Delete them from this device?")

	// Preview up to 5 titles, indented and muted.
	const preview = 5
	body.SetActive(ink.DarkGray)
	y := a.layout.sy(380)
	for i, b := range pending {
		if i == preview {
			ink.DrawString(image.Point{X: a.layout.margin + a.layout.sx(40), Y: y},
				fmt.Sprintf("… and %d more", len(pending)-preview))
			break
		}
		label := b.Author + " — " + b.Title
		ink.DrawString(image.Point{X: a.layout.margin + a.layout.sx(40), Y: y}, truncate(label, 60))
		y += a.layout.sy(50)
	}

	// Primary: Delete; Secondary: Keep. Keep lives on the left as the
	// safer option (Back key maps to it too).
	btnH := a.layout.sy(100)
	btnY2 := a.layout.backButton.Max.Y
	btnY1 := btnY2 - btnH
	contentW := a.layout.screen.X - 2*a.layout.margin
	half := (contentW - a.layout.sx(40)) / 2
	noRect := image.Rect(a.layout.margin, btnY1, a.layout.margin+half, btnY2)
	yesRect := image.Rect(a.layout.screen.X-a.layout.margin-half, btnY1, a.layout.screen.X-a.layout.margin, btnY2)

	ink.DrawRect(yesRect, ink.Black)
	ink.DrawRect(yesRect.Inset(2), ink.Black)
	drawCenteredText(btnFont, yesRect, "Delete", a.layout.fpx(44))

	ink.DrawRect(noRect, ink.Black)
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

	rowTitleFont := ink.OpenFont(ink.DefaultFontBold, a.layout.fpx(36), true)
	defer rowTitleFont.Close()
	rowSubFont := ink.OpenFont(ink.DefaultFont, a.layout.fpx(28), true)
	defer rowSubFont.Close()

	btnFont := ink.OpenFont(ink.DefaultFontBold, a.layout.fpx(44), true)
	defer btnFont.Close()
	btnFont.SetActive(ink.Black)

	a.profileList.mu.Lock()
	names := append([]string(nil), a.profileList.names...)
	active := a.profileList.active
	perr := a.profileList.err
	a.profileList.mu.Unlock()

	ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(140)}, "Server profiles")

	if perr != nil {
		body.SetActive(ink.Black)
		ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(240)}, "Could not list profiles:")
		ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(290)}, truncate(perr.Error(), 60))
		ink.DrawRect(a.layout.backButton, ink.Black)
		drawCenteredText(btnFont, a.layout.backButton, "Back", a.layout.fpx(44))
		return
	}

	// One list row per profile. Tap anywhere on the row to open the
	// detail panel. The active profile shows "Active" as its subtitle so
	// users can see at a glance which one they're on.
	rowH := a.layout.sy(110)
	areaTop := a.layout.pickerAreaTop
	rects := make([]image.Rectangle, 0, len(names))
	for i, n := range names {
		y1 := areaTop + i*rowH
		rect := image.Rect(a.layout.margin, y1, a.layout.screen.X-a.layout.margin, y1+rowH)
		rects = append(rects, rect)
		sub := ""
		if n == active {
			sub = "Active"
		}
		a.drawListRow(rowTitleFont, rowSubFont, rect, n, sub, true)
	}
	if n := len(rects); n > 0 {
		last := rects[n-1]
		a.drawHairline(last.Min.X, last.Max.X, last.Max.Y)
	}

	// Primary action: Add new server, sized like the Sync buttons so it
	// reads as the same kind of commit action.
	btnH := a.layout.sy(100)
	addY2 := a.layout.backButton.Min.Y - a.layout.sy(40)
	addY1 := addY2 - btnH
	addRect := image.Rect(a.layout.margin, addY1, a.layout.screen.X-a.layout.margin, addY2)
	ink.DrawRect(addRect, ink.Black)
	ink.DrawRect(addRect.Inset(2), ink.Black)
	drawCenteredText(btnFont, addRect, "Add new server", a.layout.fpx(44))

	a.profileList.mu.Lock()
	a.profileList.rowRects = rects
	a.profileList.addRect = addRect
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
	addRect := a.profileList.addRect
	a.profileList.mu.Unlock()

	if e.Point.In(addRect) {
		a.startAddProfile()
		return true
	}
	for i, r := range rects {
		if !e.Point.In(r) || i >= len(names) {
			continue
		}
		a.openProfileDetail(names[i])
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

// deleteProfileByName removes the named profile. When the active
// profile disappears (either by direct deletion or because it was
// renamed / wasn't the target), fileDoc promotes another profile to
// active; reloadActiveConfig picks that up. Deleting the last profile
// drops to the first-run wizard.
func (a *app) deleteProfileByName(name string) {
	wasActive := a.cfg != nil && a.cfg.Profile == name
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
		if a.store != nil {
			_ = a.store.Close()
			a.store = nil
		}
		a.cfg = nil
		a.client = nil
		a.connState = connUnknown
		a.wizard = wizardState{step: stepWelcome}
		a.screen = screenFirstRun
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

// ---------- Profile detail ----------

// openProfileDetail loads the named profile's fields and switches to
// its detail panel. Falls back to a name-only display if the profile
// can't be read (e.g. mid-edit file).
func (a *app) openProfileDetail(name string) {
	p, err := LoadProfileByName(a.cfgPath, name)
	active := ""
	if a.cfg != nil {
		active = a.cfg.Profile
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
	a.screen = screenProfileDetail
	ink.Repaint()
}

func (a *app) drawProfileDetail() {
	title := ink.OpenFont(ink.DefaultFontBold, a.layout.fpx(64), true)
	defer title.Close()
	title.SetActive(ink.Black)

	body := ink.OpenFont(ink.DefaultFont, a.layout.fpx(32), true)
	defer body.Close()
	body.SetActive(ink.Black)

	btnFont := ink.OpenFont(ink.DefaultFontBold, a.layout.fpx(44), true)
	defer btnFont.Close()
	btnFont.SetActive(ink.Black)

	a.profileDetail.mu.Lock()
	name := a.profileDetail.name
	backend := a.profileDetail.backend
	host := a.profileDetail.host
	isActive := a.profileDetail.isActive
	loadErr := a.profileDetail.loadErr
	confirming := a.profileDetail.confirmDelete
	a.profileDetail.mu.Unlock()

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
		a.screen = screenProfileList
		ink.Repaint()
		return true
	}
	return false
}

func (a *app) profileDetailPointer(e ink.PointerEvent) bool {
	if e.State != ink.PointerDown {
		return false
	}
	if e.Point.In(a.layout.backButton) {
		a.screen = screenProfileList
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
// changes so the main-screen banner appears. Wakes Wi-Fi via
// ink.ConnectDefault before the HTTP call so a check fired at app
// launch (before Wi-Fi has finished associating) does not silently
// fail with "network unreachable".
func (a *app) runUpdateCheck() {
	a.update.mu.Lock()
	if a.update.checking {
		a.update.mu.Unlock()
		return
	}
	a.update.checking = true
	a.update.checkErr = nil
	a.update.mu.Unlock()

	if err := ink.ConnectDefault(); err != nil {
		a.update.mu.Lock()
		a.update.checking = false
		a.update.checkErr = fmt.Errorf("no Wi-Fi: %w", err)
		a.update.mu.Unlock()
		ink.Repaint()
		return
	}

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
	ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(140)}, "Updates")

	body := ink.OpenFont(ink.DefaultFont, a.layout.fpx(32), true)
	defer body.Close()

	hero := ink.OpenFont(ink.DefaultFontBold, a.layout.fpx(40), true)
	defer hero.Close()

	rowTitleFont := ink.OpenFont(ink.DefaultFontBold, a.layout.fpx(36), true)
	defer rowTitleFont.Close()
	rowSubFont := ink.OpenFont(ink.DefaultFont, a.layout.fpx(28), true)
	defer rowSubFont.Close()

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
	total := a.update.total
	installErr := a.update.installErr
	installed := a.update.installed
	a.update.mu.Unlock()

	body.SetActive(ink.DarkGray)
	ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(200)}, "Installed version "+version)
	a.drawHairline(a.layout.margin, a.layout.screen.X-a.layout.margin, a.layout.sy(240))

	y := a.layout.sy(310)
	switch {
	case installed:
		hero.SetActive(ink.Black)
		ink.DrawString(image.Point{X: a.layout.margin, Y: y}, "Installed "+rel.Version)
		body.SetActive(ink.DarkGray)
		ink.DrawString(image.Point{X: a.layout.margin, Y: y + a.layout.sy(55)}, "Relaunching…")
	case installErr != nil:
		hero.SetActive(ink.Black)
		ink.DrawString(image.Point{X: a.layout.margin, Y: y}, "Install failed")
		body.SetActive(ink.DarkGray)
		ink.DrawString(image.Point{X: a.layout.margin, Y: y + a.layout.sy(55)}, truncate(installErr.Error(), 60))
	case downloading:
		hero.SetActive(ink.Black)
		headline := "Downloading " + rel.Version
		ink.DrawString(image.Point{X: a.layout.margin, Y: y}, headline)
		body.SetActive(ink.DarkGray)
		var line string
		if total > 0 {
			pct := int(100 * downloaded / total)
			if pct > 100 {
				pct = 100
			}
			line = fmt.Sprintf("%d%%  ·  %d / %d KB", pct, downloaded/1024, total/1024)
		} else {
			line = fmt.Sprintf("%d KB received", downloaded/1024)
		}
		ink.DrawString(image.Point{X: a.layout.margin, Y: y + a.layout.sy(55)}, line)
		if total > 0 {
			barY1 := y + a.layout.sy(90)
			barY2 := barY1 + a.layout.sy(30)
			barX1 := a.layout.margin
			barX2 := a.layout.screen.X - a.layout.margin
			bar := image.Rect(barX1, barY1, barX2, barY2)
			ink.DrawRect(bar, ink.Black)
			fillW := int(int64(bar.Dx()-6) * downloaded / total)
			if fillW > bar.Dx()-6 {
				fillW = bar.Dx() - 6
			}
			if fillW > 0 {
				ink.FillArea(image.Rect(barX1+a.layout.sx(3), barY1+a.layout.sy(3), barX1+a.layout.sx(3)+fillW, barY2-a.layout.sy(3)), ink.DarkGray)
			}
		}
	case checking:
		hero.SetActive(ink.Black)
		ink.DrawString(image.Point{X: a.layout.margin, Y: y}, "Checking for updates…")
	case checkErr != nil:
		hero.SetActive(ink.Black)
		ink.DrawString(image.Point{X: a.layout.margin, Y: y}, "Could not check")
		body.SetActive(ink.DarkGray)
		ink.DrawString(image.Point{X: a.layout.margin, Y: y + a.layout.sy(55)}, truncate(checkErr.Error(), 60))
	case available:
		hero.SetActive(ink.Black)
		headline := rel.Version + " is available"
		ink.DrawString(image.Point{X: a.layout.margin, Y: y}, headline)
		body.SetActive(ink.DarkGray)
		detail := "Tap Install now to update"
		if rel.BinarySize > 0 {
			detail = formatBytes(rel.BinarySize) + "  ·  " + detail
		}
		ink.DrawString(image.Point{X: a.layout.margin, Y: y + a.layout.sy(55)}, detail)
		if rel.SHA256 != "" {
			ink.DrawString(image.Point{X: a.layout.margin, Y: y + a.layout.sy(105)}, "sha256 "+rel.SHA256[:12]+"…")
		}
	default:
		hero.SetActive(ink.Black)
		ink.DrawString(image.Point{X: a.layout.margin, Y: y}, "You are up to date")
	}

	// Auto-check toggle row: list-row style matching Settings.
	btnH := a.layout.sy(100)
	toggleH := a.layout.sy(110)
	contentW := a.layout.screen.X - 2*a.layout.margin
	toggleY2 := a.layout.backButton.Min.Y - a.layout.sy(40)
	toggleY1 := toggleY2 - toggleH
	btnY2 := toggleY1 - a.layout.sy(30)
	btnY1 := btnY2 - btnH

	toggleRect := image.Rect(a.layout.margin, toggleY1, a.layout.margin+contentW, toggleY2)
	autoSub := "Check once a week"
	if !a.cfg.CheckUpdates {
		autoSub = "Disabled"
	}
	a.drawListRow(rowTitleFont, rowSubFont, toggleRect,
		"Automatic update checks", autoSub, false)
	togPillH := a.layout.sy(60)
	togPillW := a.layout.sx(120)
	togCY := (toggleRect.Min.Y + toggleRect.Max.Y) / 2
	togX2 := toggleRect.Max.X - a.layout.sx(20)
	togRect := image.Rect(togX2-togPillW, togCY-togPillH/2, togX2, togCY+togPillH/2)
	a.drawToggle(togRect, a.cfg.CheckUpdates)
	a.drawHairline(toggleRect.Min.X, toggleRect.Max.X, toggleRect.Max.Y)

	// Primary action row (either Install now or Check for updates).
	primary := image.Rect(a.layout.margin, btnY1, a.layout.margin+contentW, btnY2)
	var installBtn, checkBtn image.Rectangle
	if available && !installed && !downloading {
		installBtn = primary
		ink.DrawRect(installBtn, ink.Black)
		ink.DrawRect(installBtn.Inset(2), ink.Black)
		drawCenteredText(btnFont, installBtn, "Install now", a.layout.fpx(44))
	} else if !downloading && !installed && !checking {
		checkBtn = primary
		ink.DrawRect(checkBtn, ink.Black)
		drawCenteredText(btnFont, checkBtn, "Check for updates", a.layout.fpx(44))
	}

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
	err = Download(ctx, rel, staged, func(n, total int64) {
		a.update.mu.Lock()
		a.update.downloaded = n
		a.update.total = total
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

	// Give the user a couple of seconds to read the success line, then
	// replace the running process with the freshly-installed binary.
	// syscall.Exec inherits our stdio + env and reads the new image
	// from disk (the inode the renamed file points at), so the user
	// sees the main screen again without leaving the Applications menu
	// themselves. Any InkView cleanup that ink.Run would normally do on
	// exit is skipped here, but the display subsystem is re-opened by
	// the new process, so the user doesn't notice.
	time.Sleep(2 * time.Second)
	a.relaunch(exe)
}

// relaunch execs the binary at path, replacing the current process.
// On success the call doesn't return; on failure the update screen
// shows a manual-relaunch hint so the user can exit and tap the app
// icon themselves.
func (a *app) relaunch(path string) {
	if a.store != nil {
		_ = a.store.Close()
	}
	if a.netStop != nil {
		a.netStop()
	}
	args := []string{path}
	if len(os.Args) > 1 {
		args = append(args, os.Args[1:]...)
	}
	err := syscall.Exec(path, args, os.Environ())
	// Only reached when exec fails (rare; kernel block, missing perms).
	a.update.mu.Lock()
	a.update.installErr = fmt.Errorf("auto-relaunch failed: %w. Quit pocketbeam and reopen it from the Applications menu to finish the update.", err)
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
