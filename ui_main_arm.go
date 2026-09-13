// Main sync screen and the sync runner: progress strip, the goroutine
// that plans and runs a sync, and the library-refresh hand-off to the
// PocketBook scanner.

package main

import (
	"context"
	"fmt"
	"image"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	ink "github.com/dennwc/inkview"
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

// syncState holds live progress from a running sync. Written by the progress
// callback (goroutine), read by Draw (event loop). Protected by mu.
type syncState struct {
	mu       sync.Mutex
	active   bool
	planning bool // true while Plan() is running before downloads start
	index    int
	total    int
	title    string
	author   string
	err      error
	// unknownSizes is the count of new/updated books whose size the
	// pre-flight plan couldn't determine. Non-zero means the post-sync
	// summary tacks on a "lower-bound" note so the user knows the size
	// estimate wasn't exact.
	unknownSizes int
	bookStart    time.Time          // when the current book's download began
	cancel       context.CancelFunc // populated while active; nil otherwise
	cancelled    bool               // true when the user tapped Cancel so the summary can say so
}

func (a *app) drawMain() {
	title := a.font(ink.DefaultFontBold, 64)
	hero := a.font(ink.DefaultFontBold, 48)
	body := a.font(ink.DefaultFont, 32)
	small := a.font(ink.DefaultFont, 26)
	btnFont := a.font(ink.DefaultFontBold, 44)

	cfg := a.Config()
	stats := a.Stats()

	// Title + host subtitle (host in muted gray so the filter/host context
	// is present but secondary to the action area).
	title.SetActive(ink.Black)
	ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(140)}, "pocketbeam")
	small.SetActive(ink.DarkGray)
	subtitle := cfg.Host
	if cfg.Backend == BackendWebDAV {
		p := cfg.Path
		if p == "" {
			p = "/"
		}
		subtitle += "  ·  " + p
	} else {
		subtitle += "  ·  " + cfg.FilterLabel()
	}
	ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(190)}, truncate(subtitle, 60))

	// Hairline under the header.
	a.drawHairline(a.layout.margin, a.layout.screen.X-a.layout.margin, a.layout.sy(220))

	// Status hero: emphatic primary line + muted supporting details.
	statusY := a.layout.sy(300)
	hero.SetActive(ink.Black)
	body.SetActive(ink.DarkGray)
	if stats.hasLastSync {
		ink.DrawString(image.Point{X: a.layout.margin, Y: statusY},
			"Last synced "+humanAgo(stats.lastSync.At))
		ink.DrawString(image.Point{X: a.layout.margin, Y: statusY + a.layout.sy(60)},
			fmt.Sprintf("%d books in library", stats.bookCount))
		if stats.lastSync.Failed > 0 {
			ink.DrawString(image.Point{X: a.layout.margin, Y: statusY + a.layout.sy(110)},
				fmt.Sprintf("%d failed — will retry next sync", stats.lastSync.Failed))
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
				a.layout.progressBar.Min.X+a.layout.sx(3),
				a.layout.progressBar.Min.Y+a.layout.sy(3),
				a.layout.progressBar.Min.X+a.layout.sx(3)+fillW,
				a.layout.progressBar.Max.Y-a.layout.sy(3),
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
// partial e-ink update, so the rest of the main screen stays stable. A
// sync keeps running while the user is elsewhere - on Settings, or on
// the deletion prompt the sync itself raised - so the draw runs under
// DrawIfOn and is dropped whenever the main screen is not the one up.
func (a *app) refreshProgress() {
	a.DrawIfOn(screenMain, func() {
		body := a.font(ink.DefaultFont, 32)
		a.drawMainProgressContent(body)
		ink.PartialUpdate(a.layout.progressArea)
	})
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
		a.SetScreen(screenSettings)
		ink.Repaint()
		return true
	}
	return false
}

func (a *app) mainPointer(e ink.PointerEvent) bool {
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
		a.SetScreen(screenSettings)
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

	// Arm Stop before the first network round-trip so a tap during the
	// probe or the catalog listing aborts the run instead of waiting for
	// the first download.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a.sync.mu.Lock()
	a.sync.cancel = cancel
	a.sync.cancelled = false
	a.sync.mu.Unlock()

	// One snapshot of the active profile for the whole run, so a profile
	// switch landing mid-sync cannot pair one profile's catalog with
	// another's store.
	cfg, _, store := a.Session()

	if err := a.ensureConnected(ctx); err != nil {
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
	src, err := newSource(cfg)
	if err != nil {
		a.finishSyncWithError(err)
		a.refreshMainStats()
		ink.Repaint()
		return
	}
	opts := SyncOptions{
		DeleteMissing: cfg.DeleteMissing,
		Scope:         ScopeFor(cfg),
	}
	if cfg.DeleteMissing {
		opts.Confirm = a.confirmDeletions
	}
	a.sync.mu.Lock()
	a.sync.planning = true
	a.sync.mu.Unlock()
	a.refreshProgress()

	plan, books, err := Plan(ctx, src, store, cfg.Library, opts)
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
			a.SetScreen(screenMain)
			ink.Repaint()
			return
		}
	}
	a.sync.mu.Lock()
	a.sync.unknownSizes = plan.UnknownSizes
	a.sync.mu.Unlock()

	opts.PrefetchedBooks = books
	res := Sync(ctx, src, store, cfg.Library, progress, opts)

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
	_ = store.SetLastSync(SyncSummary{
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
		a.SetScreen(screenLibraryRefresh)
		a.startLibRefreshSpinner()
		ink.Repaint()
		go a.runLibraryScanner()
		return
	}
	// In case the confirm screen is still up (e.g. user closed the device
	// with the prompt showing), flip back to main.
	a.SetScreen(screenMain)
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
		a.SetScreen(screenMain)
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
// Used when the sync bails before calling Sync() (no network, probe fail,
// listing error). A run the user stopped reports "Sync stopped." instead
// of the context error the aborted request surfaced.
func (a *app) finishSyncWithError(err error) {
	a.sync.mu.Lock()
	a.sync.active = false
	a.sync.cancel = nil
	if a.sync.cancelled {
		a.sync.cancelled = false
		err = fmt.Errorf("Sync stopped.")
	}
	a.sync.err = err
	a.sync.mu.Unlock()
}

// ensureConnected wakes the Wi-Fi and verifies the server is reachable with
// the configured credentials, and refreshes the client's server-type
// detection so the next WalkAll picks the right path (CWA fast path vs
// generic recursive walk). Returns a user-facing error on failure; nil on
// success. All UI actions that issue HTTP requests to the server should
// call this first so the user gets a consistent, readable error instead of
// a raw Go transport dump. ctx bounds the probe and detection requests so
// the caller's cancel or timeout also covers this step.
func (a *app) ensureConnected(ctx context.Context) error {
	if err := ink.ConnectDefault(); err != nil {
		return fmt.Errorf("No Wi-Fi connection. Open Network to configure.")
	}
	cfg, client, _ := a.Session()
	var err error
	switch cfg.Backend {
	case BackendWebDAV:
		err = ProbeWebDAV(ctx, cfg.Host, cfg.User, cfg.Pass, cfg.Path)
	default:
		err = ProbeCWA(ctx, cfg.Host, cfg.User, cfg.Pass)
	}
	if err != nil {
		return err
	}
	if cfg.Backend != BackendWebDAV && client != nil {
		_ = client.DetectType(ctx) // non-fatal: falls back to generic walk
	}
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

// ---------- Library-refresh dialog ----------

// drawLibraryRefresh paints a centred informational dialog shown after a
// sync that changed the library. The actual work happens in
// runLibraryScanner; scanner.app takes foreground focus as soon as it
// starts, so this draw pass is what the user sees in the ~instant between
// sync completion and the scanner UI appearing. It also serves as the
// backdrop the user returns to when scanner exits, right before the
// goroutine flips back to screenMain.
func (a *app) drawLibraryRefresh() {
	title := a.font(ink.DefaultFontBold, 54)
	title.SetActive(ink.Black)

	body := a.font(ink.DefaultFont, 32)
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
	a.DrawIfOn(screenLibraryRefresh, func() {
		a.libRefresh.mu.Lock()
		dots := a.libRefresh.dots
		rect := a.libRefresh.rect
		a.libRefresh.mu.Unlock()
		if rect.Empty() {
			return
		}

		body := a.font(ink.DefaultFont, 32)
		body.SetActive(ink.Black)
		ink.FillArea(rect, ink.White)
		ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(400)}, "Indexing new books on the device"+strings.Repeat(".", dots))
		ink.PartialUpdate(rect)
	})
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
