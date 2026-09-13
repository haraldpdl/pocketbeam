// Blocking confirmations the sync goroutine raises: the pre-flight
// space warning and the delete-missing prompt. Both park their answer on
// a channel the sync goroutine waits on.

package main

import (
	"fmt"
	"image"
	"sync"

	ink "github.com/dennwc/inkview"
)

// spaceWarnState parks a pre-flight plan while the oversize-confirm screen
// is up and routes the user's answer back to the sync goroutine over ch.
// ch is non-nil only while a prompt is pending.
type spaceWarnState struct {
	mu      sync.Mutex
	plan    SyncPlan
	ch      chan bool
	yesRect image.Rectangle
	noRect  image.Rectangle
}

// deleteConfirmState holds the deletion set awaiting user confirmation
// and the channel the sync goroutine blocks on. ch is non-nil only while
// a prompt is pending.
type deleteConfirmState struct {
	mu      sync.Mutex
	pending []LocalBook
	ch      chan bool
	yesRect image.Rectangle
	noRect  image.Rectangle
}

// confirmSpace parks the plan on the UI, flips to the oversize-confirm
// screen, and blocks the sync goroutine until the user answers. Returns
// true to proceed with the download anyway, false to cancel the sync.
func (a *app) confirmSpace(plan SyncPlan) bool {
	a.spaceWarn.mu.Lock()
	a.spaceWarn.plan = plan
	a.spaceWarn.ch = make(chan bool, 1)
	ch := a.spaceWarn.ch
	a.spaceWarn.mu.Unlock()
	a.SetScreen(screenSpaceWarn)
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
	a.SetScreen(screenMain)
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

	a.spaceWarn.mu.Lock()
	plan := a.spaceWarn.plan
	a.spaceWarn.mu.Unlock()

	title.SetActive(ink.Black)
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
	a.SetScreen(screenDeleteConfirm)
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
	a.SetScreen(screenMain)
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

	a.delConfirm.mu.Lock()
	pending := append([]LocalBook(nil), a.delConfirm.pending...)
	a.delConfirm.mu.Unlock()

	title.SetActive(ink.Black)
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
