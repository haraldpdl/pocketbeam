// Self-update: background check, the update screen, and the download /
// install / relaunch flow.

package main

import (
	"context"
	"fmt"
	"image"
	"os"
	"sync"
	"syscall"
	"time"

	ink "github.com/dennwc/inkview"
)

// updateState holds the state of the self-update flow: a pending
// release (if a check found one newer than the running version), the
// active download's progress, and any terminal message shown once the
// install succeeds or fails.
type updateState struct {
	mu          sync.Mutex
	checking    bool
	available   bool
	release     Release
	checkErr    error
	downloading bool
	downloaded  int64
	total       int64 // -1 until the HTTP response's Content-Length is known
	installErr  error
	installed   bool // true once the new binary has been written to disk
	installBtn  image.Rectangle
	checkBtn    image.Rectangle
	toggleBtn   image.Rectangle // enable/disable automatic weekly checks
	backBtn     image.Rectangle
}

// updateSnapshot is one consistent read of updateState. Drawing takes
// the lock once and works from the copy, so a check or a download
// landing mid-pass is never observed half applied.
type updateSnapshot struct {
	checking    bool
	available   bool
	rel         Release
	checkErr    error
	downloading bool
	downloaded  int64
	total       int64
	installErr  error
	installed   bool
}

func (u *updateState) snapshot() updateSnapshot {
	u.mu.Lock()
	defer u.mu.Unlock()
	return updateSnapshot{
		checking:    u.checking,
		available:   u.available,
		rel:         u.release,
		checkErr:    u.checkErr,
		downloading: u.downloading,
		downloaded:  u.downloaded,
		total:       u.total,
		installErr:  u.installErr,
		installed:   u.installed,
	}
}

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
	store := a.Store()
	if store == nil {
		return
	}
	if last, ok, _ := store.GetMeta(metaLastUpdateCheck); ok {
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
	// Read the endpoint before claiming the check: deleting the last
	// profile clears the session, and a check that raced that would have
	// no config to query.
	cfg := a.Config()
	if cfg == nil {
		return
	}
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
	newer, rel, err := CheckLatest(ctx, cfg.EffectiveUpdateURL(), version)

	a.update.mu.Lock()
	a.update.checking = false
	a.update.release = rel
	a.update.available = newer
	a.update.checkErr = err
	a.update.mu.Unlock()
	if store := a.Store(); store != nil {
		_ = store.SetMeta(metaLastUpdateCheck, time.Now().Format(time.RFC3339))
	}
	ink.Repaint()
}

// openUpdateScreen transitions to the dedicated update screen. If no
// release is loaded yet (user tapped "Check for updates" from an idle
// state), trigger a fresh check in the background.
func (a *app) openUpdateScreen() {
	a.SetScreen(screenUpdate)
	ink.Repaint()
	a.update.mu.Lock()
	haveRelease := a.update.release.Version != ""
	a.update.mu.Unlock()
	if !haveRelease {
		go a.runUpdateCheck()
	}
}

// autoCheckTitle names the automatic-checks row; autoCheckSubtitle is
// its value line. Both the full draw and the in-place refresh that
// follows a tap on the row go through these.
const autoCheckTitle = "Automatic update checks"

func autoCheckSubtitle(on bool) string {
	if on {
		return "Check once a week"
	}
	return "Disabled"
}

// updateStatusArea is the strip drawUpdateStatus owns: headline, detail
// line and the download bar. Refreshed on its own while a download runs
// so a progress tick costs one partial update instead of a full-screen
// repaint.
func (a *app) updateStatusArea() image.Rectangle {
	return image.Rect(a.layout.margin, a.layout.sy(300),
		a.layout.screen.X-a.layout.margin, a.layout.sy(460))
}

// drawUpdateStatus paints whichever of the flow's states u describes.
// It clears its own area first so it can be called on its own, outside a
// full repaint.
func (a *app) drawUpdateStatus(u updateSnapshot) {
	hero := a.font(ink.DefaultFontBold, 40)
	body := a.font(ink.DefaultFont, 32)
	ink.FillArea(a.updateStatusArea(), ink.White)

	y := a.layout.sy(310)
	switch {
	case u.installed:
		hero.SetActive(ink.Black)
		ink.DrawString(image.Point{X: a.layout.margin, Y: y}, "Installed "+u.rel.Version)
		body.SetActive(ink.DarkGray)
		ink.DrawString(image.Point{X: a.layout.margin, Y: y + a.layout.sy(55)}, "Relaunching…")
	case u.installErr != nil:
		hero.SetActive(ink.Black)
		ink.DrawString(image.Point{X: a.layout.margin, Y: y}, "Install failed")
		body.SetActive(ink.DarkGray)
		ink.DrawString(image.Point{X: a.layout.margin, Y: y + a.layout.sy(55)}, truncate(u.installErr.Error(), 60))
	case u.downloading:
		hero.SetActive(ink.Black)
		ink.DrawString(image.Point{X: a.layout.margin, Y: y}, "Downloading "+u.rel.Version)
		body.SetActive(ink.DarkGray)
		var line string
		if u.total > 0 {
			pct := int(100 * u.downloaded / u.total)
			if pct > 100 {
				pct = 100
			}
			line = fmt.Sprintf("%d%%  ·  %d / %d KB", pct, u.downloaded/1024, u.total/1024)
		} else {
			line = fmt.Sprintf("%d KB received", u.downloaded/1024)
		}
		ink.DrawString(image.Point{X: a.layout.margin, Y: y + a.layout.sy(55)}, line)
		if u.total > 0 {
			barY1 := y + a.layout.sy(90)
			barY2 := barY1 + a.layout.sy(30)
			barX1 := a.layout.margin
			barX2 := a.layout.screen.X - a.layout.margin
			bar := image.Rect(barX1, barY1, barX2, barY2)
			ink.DrawRect(bar, ink.Black)
			fillW := int(int64(bar.Dx()-6) * u.downloaded / u.total)
			if fillW > bar.Dx()-6 {
				fillW = bar.Dx() - 6
			}
			if fillW > 0 {
				ink.FillArea(image.Rect(barX1+a.layout.sx(3), barY1+a.layout.sy(3), barX1+a.layout.sx(3)+fillW, barY2-a.layout.sy(3)), ink.DarkGray)
			}
		}
	case u.checking:
		hero.SetActive(ink.Black)
		ink.DrawString(image.Point{X: a.layout.margin, Y: y}, "Checking for updates…")
	case u.checkErr != nil:
		hero.SetActive(ink.Black)
		ink.DrawString(image.Point{X: a.layout.margin, Y: y}, "Could not check")
		body.SetActive(ink.DarkGray)
		ink.DrawString(image.Point{X: a.layout.margin, Y: y + a.layout.sy(55)}, truncate(u.checkErr.Error(), 60))
	case u.available:
		hero.SetActive(ink.Black)
		ink.DrawString(image.Point{X: a.layout.margin, Y: y}, u.rel.Version+" is available")
		body.SetActive(ink.DarkGray)
		detail := "Tap Install now to update"
		if u.rel.BinarySize > 0 {
			detail = formatBytes(u.rel.BinarySize) + "  ·  " + detail
		}
		ink.DrawString(image.Point{X: a.layout.margin, Y: y + a.layout.sy(55)}, detail)
		if u.rel.SHA256 != "" {
			ink.DrawString(image.Point{X: a.layout.margin, Y: y + a.layout.sy(105)}, "sha256 "+u.rel.SHA256[:12]+"…")
		}
	default:
		hero.SetActive(ink.Black)
		ink.DrawString(image.Point{X: a.layout.margin, Y: y}, "You are up to date")
	}
}

// refreshUpdateProgress repaints the status block from the download
// goroutine and pushes only that strip. The reader already throttles the
// callback to percentage changes; keeping each of those off the
// full-repaint path is what stops an install from flashing the whole
// panel a hundred times.
func (a *app) refreshUpdateProgress() {
	// The user can leave mid-download; whatever screen replaced this one
	// owns those pixels now.
	if a.Screen() != screenUpdate {
		return
	}
	a.drawUpdateStatus(a.update.snapshot())
	ink.PartialUpdate(a.updateStatusArea())
}

func (a *app) drawUpdate() {
	title := a.font(ink.DefaultFontBold, 64)
	body := a.font(ink.DefaultFont, 32)
	rowTitleFont := a.font(ink.DefaultFontBold, 36)
	rowSubFont := a.font(ink.DefaultFont, 28)
	btnFont := a.font(ink.DefaultFontBold, 44)

	cfg := a.Config()
	u := a.update.snapshot()

	title.SetActive(ink.Black)
	ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(140)}, "Updates")
	body.SetActive(ink.DarkGray)
	ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(200)}, "Installed version "+version)
	a.drawHairline(a.layout.margin, a.layout.screen.X-a.layout.margin, a.layout.sy(240))

	a.drawUpdateStatus(u)

	// Auto-check toggle row: list-row style matching Settings.
	btnH := a.layout.sy(100)
	toggleH := a.layout.sy(110)
	contentW := a.layout.screen.X - 2*a.layout.margin
	toggleY2 := a.layout.backButton.Min.Y - a.layout.sy(40)
	toggleY1 := toggleY2 - toggleH
	btnY2 := toggleY1 - a.layout.sy(30)
	btnY1 := btnY2 - btnH

	toggleRect := image.Rect(a.layout.margin, toggleY1, a.layout.margin+contentW, toggleY2)
	a.drawToggleRow(rowTitleFont, rowSubFont, toggleRect,
		autoCheckTitle, autoCheckSubtitle(cfg.CheckUpdates), cfg.CheckUpdates)

	// Primary action row (either Install now or Check for updates).
	primary := image.Rect(a.layout.margin, btnY1, a.layout.margin+contentW, btnY2)
	var installBtn, checkBtn image.Rectangle
	if u.available && !u.installed && !u.downloading {
		installBtn = primary
		ink.DrawRect(installBtn, ink.Black)
		ink.DrawRect(installBtn.Inset(2), ink.Black)
		drawCenteredText(btnFont, installBtn, "Install now", a.layout.fpx(44))
	} else if !u.downloading && !u.installed && !u.checking {
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
		a.SetScreen(screenSettings)
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
	a.update.mu.Lock()
	installBtn := a.update.installBtn
	checkBtn := a.update.checkBtn
	toggleBtn := a.update.toggleBtn
	backBtn := a.update.backBtn
	a.update.mu.Unlock()
	if e.Point.In(backBtn) {
		a.SetScreen(screenSettings)
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
		// Only the row itself changes, so it redraws in place rather
		// than re-flashing the screen.
		if cfg := a.saveConfigChange(func(c *Config) { c.CheckUpdates = !c.CheckUpdates }); cfg != nil {
			a.refreshToggleRow(toggleBtn, autoCheckTitle,
				autoCheckSubtitle(cfg.CheckUpdates), cfg.CheckUpdates)
		}
		return true
	}
	return false
}

// runUpdateInstall downloads the pending release to a .new file next
// to the running binary, verifies the SHA, and renames it into place.
// The user relaunches to pick up the new version; we do not call
// ink.Exit() automatically so they can read the success message.
//
// The downloading/installed flags double as the re-entrancy guard:
// drawUpdate clears the Install rect while a download runs, but the
// repaint that clears it is asynchronous, so a second tap (or OK key)
// landing before it would otherwise start a second download into the
// same .new file.
func (a *app) runUpdateInstall() {
	a.update.mu.Lock()
	if a.update.downloading || a.update.installed {
		a.update.mu.Unlock()
		return
	}
	rel := a.update.release
	a.update.downloading = true
	a.update.downloaded = 0
	a.update.total = -1 // until this response's Content-Length arrives
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
		a.refreshUpdateProgress()
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
	if store := a.Store(); store != nil {
		_ = store.Close()
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
