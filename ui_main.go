// Drawing for the main sync screen. It paints from a plain snapshot of
// the app state, so it renders the same on the device and into an image
// on a build machine; the event handling and the sync runner it drives
// live in ui_main_arm.go.

package main

import (
	"fmt"
	"image"
	"time"
)

// syncSnapshot is one consistent reading of a sync's progress. Values,
// not a live syncState: the progress callback writes that from the sync
// goroutine while the screen draws, and the elapsed time is resolved
// here so the drawing itself does not depend on the clock.
type syncSnapshot struct {
	active       bool
	planning     bool // true while Plan() is running before downloads start
	index        int
	total        int
	title        string
	author       string
	err          error
	elapsed      time.Duration // time spent on the current book, 0 before the first one
	unknownSizes int
}

// mainView is everything the main screen shows, resolved before the draw
// starts: the header subtitle, the library stats, the sync progress, and
// the version of a waiting update ("" when none).
type mainView struct {
	subtitle string
	stats    mainStats
	// lastSyncAgo is stats.lastSync.At already phrased ("3 hours ago"),
	// read only when stats.hasLastSync. Resolved by the caller like
	// syncSnapshot.elapsed, so the draw depends on no clock and renders
	// the same image every time.
	lastSyncAgo string
	sync        syncSnapshot
	updateVer   string
}

// mainSubtitle renders the header's second line: which server this
// profile talks to, and which slice of it is being synced.
func mainSubtitle(cfg *Config) string {
	if cfg == nil {
		return ""
	}
	if cfg.Backend == BackendWebDAV {
		p := cfg.Path
		if p == "" {
			p = "/"
		}
		return cfg.Host + "  ·  " + p
	}
	return cfg.Host + "  ·  " + cfg.FilterLabel()
}

func drawMain(c Canvas, l layout, v mainView) {
	title := l.font(c, 64, true)
	hero := l.font(c, 48, true)
	body := l.font(c, 32, false)
	small := l.font(c, 26, false)
	btnFont := l.font(c, 44, true)

	// Title + host subtitle (host in muted gray so the filter/host context
	// is present but secondary to the action area). A string sits in a
	// glyph box as tall as the size it was opened at, so the 64px title
	// occupies down to sy(204): the subtitle goes below that, on the same
	// 140/210/240 header grid every other screen uses.
	c.SetFont(title, black)
	c.Text(image.Point{X: l.margin, Y: l.sy(140)}, "pocketbeam")
	c.SetFont(small, darkGray)
	c.Text(image.Point{X: l.margin, Y: l.sy(210)}, truncate(v.subtitle, 60))

	// Hairline under the header.
	l.drawHairline(c, l.margin, l.screen.X-l.margin, l.sy(240))

	// Status hero: emphatic primary line + muted supporting details.
	statusY := l.sy(300)
	c.SetFont(hero, black)
	if v.stats.hasLastSync {
		c.Text(image.Point{X: l.margin, Y: statusY}, "Last synced "+v.lastSyncAgo)
		c.SetFont(body, darkGray)
		c.Text(image.Point{X: l.margin, Y: statusY + l.sy(60)},
			fmt.Sprintf("%d books in library", v.stats.bookCount))
		if v.stats.lastSync.Failed > 0 {
			c.Text(image.Point{X: l.margin, Y: statusY + l.sy(110)},
				fmt.Sprintf("%d failed — will retry next sync", v.stats.lastSync.Failed))
		}
	} else {
		c.Text(image.Point{X: l.margin, Y: statusY}, "Not yet synced")
		c.SetFont(body, darkGray)
		c.Text(image.Point{X: l.margin, Y: statusY + l.sy(60)},
			"Tap Sync Now to begin")
	}

	// Update badge: light-gray filled strip sitting above the sync button
	// when a newer release is waiting. Informational only; the install
	// lives in Settings → About.
	if v.updateVer != "" {
		badge := image.Rect(
			l.margin,
			l.syncButton.Min.Y-l.sy(80),
			l.screen.X-l.margin,
			l.syncButton.Min.Y-l.sy(20),
		)
		c.Fill(badge, lightGray)
		drawCenteredText(c, small, badge, "Update "+v.updateVer+" available in Settings")
	}

	// Primary action: Sync Now (or Stop during an active sync). Kept as
	// the screen's most prominent element — double border signals
	// primary.
	c.Rect(l.syncButton, black)
	c.Rect(l.syncButton.Inset(2), black)
	btnLabel := "Sync Now"
	if v.sync.active {
		btnLabel = "Stop"
	}
	drawCenteredText(c, btnFont, l.syncButton, btnLabel)

	// Live progress area (drawn fully here on idle-to-sync transition; during
	// the sync it is refreshed in-place via drawMainProgress + PartialUpdate).
	drawMainProgress(c, l, v.sync)

	// Bottom action row: secondary actions styled with a single border so
	// they read as lighter than the primary Sync button.
	c.Rect(l.networkButton, black)
	c.Rect(l.settingsButton, black)
	c.Rect(l.quitButton, black)
	drawCenteredText(c, btnFont, l.networkButton, "Network")
	drawCenteredText(c, btnFont, l.settingsButton, "Settings")
	drawCenteredText(c, btnFont, l.quitButton, "Quit")
}

// drawMainProgress renders the progress strip (counter, bar, current
// book line) without touching the rest of the main screen, so the sync
// goroutine can push just this strip between full repaints.
func drawMainProgress(c Canvas, l layout, s syncSnapshot) {
	body := l.font(c, 32, false)
	c.Fill(l.progressArea, white)
	c.SetFont(body, black)

	switch {
	case s.planning:
		c.Text(image.Point{X: l.margin, Y: l.progressArea.Min.Y + l.sy(30)},
			"Checking remote catalog and free space...")
	case s.active:
		counter := fmt.Sprintf("%d / %d", s.index, s.total)
		if s.elapsed >= time.Second {
			counter += "  (" + formatElapsed(s.elapsed) + ")"
		}
		c.Text(image.Point{X: l.margin, Y: l.progressArea.Min.Y + l.sy(30)}, counter)
		c.Rect(l.progressBar, black)
		if s.total > 0 {
			fillW := (l.progressBar.Dx() - 6) * s.index / s.total
			c.Fill(image.Rect(
				l.progressBar.Min.X+l.sx(3),
				l.progressBar.Min.Y+l.sy(3),
				l.progressBar.Min.X+l.sx(3)+fillW,
				l.progressBar.Max.Y-l.sy(3),
			), darkGray)
		}
		c.Text(image.Point{X: l.margin, Y: l.progressArea.Min.Y + l.sy(180)}, truncate(s.author+": "+s.title, 60))
	case s.err != nil:
		c.Text(image.Point{X: l.margin, Y: l.progressArea.Min.Y + l.sy(30)}, "Last error:")
		c.Text(image.Point{X: l.margin, Y: l.progressArea.Min.Y + l.sy(80)}, truncate(s.err.Error(), 60))
	case s.unknownSizes > 0:
		c.Text(image.Point{X: l.margin, Y: l.progressArea.Min.Y + l.sy(30)},
			fmt.Sprintf("%d book(s) had unknown size; space estimate was a lower bound.", s.unknownSizes))
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
