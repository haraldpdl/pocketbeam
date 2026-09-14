// Self-update: the background check, the update screen's state and
// taps, and the download / install / relaunch flow. The drawing lives in
// ui_update.go.

package main

import (
	"context"
	"fmt"
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
}

// snapshot reads the flow's live state as one consistent set of values
// for the screen to draw from.
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

// refreshUpdateProgress repaints the status block from the download
// goroutine and pushes only that strip. The reader already throttles the
// callback; keeping each of those off the full-repaint path is what
// stops an install from flashing the whole panel a hundred times.
//
// The user can leave mid-download, so the draw runs under DrawIfOn:
// whatever screen replaced this one owns those pixels.
func (a *app) refreshUpdateProgress() {
	c := deviceCanvas
	a.DrawIfOn(screenUpdate, func() {
		drawUpdateStatus(c, a.layout, a.update.snapshot())
		c.PartialUpdate(a.layout.updateStatusArea)
	})
}

// updateView collects the flow's state, the config's automatic-check
// setting and the running version into the snapshot the shared drawing
// paints from.
func (a *app) updateView() updateView {
	return updateViewOf(a.update.snapshot(), a.Config().CheckUpdates, version)
}

func (a *app) updateKey(e ink.KeyEvent) bool {
	if e.Key == ink.KeyBack {
		a.SetScreen(screenSettings)
		ink.Repaint()
		return true
	}
	if e.Key == ink.KeyOk {
		if a.update.snapshot().primaryAction() == updateActionInstall {
			go a.runUpdateInstall()
		} else {
			go a.runUpdateCheck()
		}
		return true
	}
	return false
}

func (a *app) updatePointer(e ink.PointerEvent) bool {
	if e.Point.In(a.layout.backButton) {
		a.SetScreen(screenSettings)
		ink.Repaint()
		return true
	}
	if e.Point.In(a.layout.updatePrimaryButton) {
		switch a.update.snapshot().primaryAction() {
		case updateActionInstall:
			go a.runUpdateInstall()
			return true
		case updateActionCheck:
			go a.runUpdateCheck()
			return true
		}
		// A check or a download is running and the button is not on
		// screen, so the tap lands on nothing.
		return false
	}
	if e.Point.In(a.layout.updateToggleRow) {
		// Only the row itself changes, so it redraws in place rather
		// than re-flashing the screen.
		if cfg := a.saveConfigChange(func(c *Config) { c.CheckUpdates = !c.CheckUpdates }); cfg != nil {
			a.refreshToggleRow(a.layout.updateToggleRow, autoCheckTitle,
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
