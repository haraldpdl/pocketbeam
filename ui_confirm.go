// Drawing for the two blocking confirmations the sync goroutine raises:
// the pre-flight space warning and the delete-missing prompt. Both paint
// from a plain snapshot of what the sync found, so they render the same
// on the device and into an image on a build machine; the channels the
// sync goroutine waits on and the keys and taps that answer them live in
// ui_confirm_arm.go.

package main

import (
	"fmt"
	"image"
)

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

// spaceWarnView is the shortfall the prompt reports, resolved from the
// pre-flight plan before the draw starts. The plan itself carries the
// book lists the sync is about to run, which the screen never shows.
type spaceWarnView struct {
	books     int
	needBytes int64
	freeBytes int64
	// reclaimableBytes is what delete-missing would free afterwards, and
	// unknownSizes how many books the source gave no size for; each is a
	// line the prompt only shows when it is non-zero.
	reclaimableBytes int64
	unknownSizes     int
}

func spaceWarnViewOf(plan SyncPlan) spaceWarnView {
	return spaceWarnView{
		books:            len(plan.NewBooks) + len(plan.UpdatedBooks),
		needBytes:        plan.DownloadBytes,
		freeBytes:        plan.FreeBytes,
		reclaimableBytes: plan.ReclaimableBytes,
		unknownSizes:     plan.UnknownSizes,
	}
}

func drawSpaceWarn(c Canvas, l layout, v spaceWarnView) {
	title := l.font(c, 64, true)
	hero := l.font(c, 40, true)
	body := l.font(c, 32, false)
	small := l.font(c, 26, false)
	btnFont := l.font(c, 44, true)

	// Header on the 140/210/240 grid the main screen sets: a 64px title
	// fills its glyph box down to 204, so the muted line under it starts
	// at 210 and the hairline closes the block at 240.
	c.SetFont(title, black)
	c.Text(image.Point{X: l.margin, Y: l.sy(140)}, "Not enough space")
	c.SetFont(small, darkGray)
	c.Text(image.Point{X: l.margin, Y: l.sy(210)},
		"This sync needs more room than the device has.")
	l.drawHairline(c, l.margin, l.screen.X-l.margin, l.sy(240))

	c.SetFont(hero, black)
	c.Text(image.Point{X: l.margin, Y: l.sy(310)},
		fmt.Sprintf("%d books · %s needed", v.books, formatBytes(v.needBytes)))
	c.SetFont(body, darkGray)
	c.Text(image.Point{X: l.margin, Y: l.sy(365)},
		fmt.Sprintf("%s free on device", formatBytes(v.freeBytes)))

	y := l.sy(440)
	if v.unknownSizes > 0 {
		c.Text(image.Point{X: l.margin, Y: y},
			fmt.Sprintf("%d books had unknown size; total is a lower bound.", v.unknownSizes))
		y += l.sy(50)
	}
	if v.reclaimableBytes > 0 {
		c.Text(image.Point{X: l.margin, Y: y},
			fmt.Sprintf("Up to %s freed after delete-missing.", formatBytes(v.reclaimableBytes)))
		y += l.sy(50)
	}
	y += l.sy(30)
	c.SetFont(body, black)
	c.Text(image.Point{X: l.margin, Y: y},
		"Download anyway? Partial syncs are safe — the device")
	c.Text(image.Point{X: l.margin, Y: y + l.sy(45)},
		"just stops when the disk fills up.")

	// Download is the primary action and sits on the left, where the
	// Back key's equivalent (Cancel) is not.
	drawDialogButton(c, btnFont, l.confirmLeftButton, "Download", true)
	drawDialogButton(c, btnFont, l.confirmRightButton, "Cancel", false)
}

// deletePreview is how many titles the delete prompt names before it
// counts the rest: enough to recognise what is about to go, short
// enough to leave the question and the buttons on the screen.
const deletePreview = 5

// deleteConfirmView is the deletion set as the prompt shows it: how many
// books the sync found missing on the server, and the first few of them
// already phrased.
type deleteConfirmView struct {
	total   int
	preview []string
}

// deleteConfirmViewOf phrases at most deletePreview of pending. The
// caller holds the deletion set while the sync goroutine waits on the
// answer, and a library-wide delete can run to thousands of books, so
// only what is drawn is copied.
func deleteConfirmViewOf(pending []LocalBook) deleteConfirmView {
	v := deleteConfirmView{total: len(pending)}
	for _, b := range pending {
		if len(v.preview) == deletePreview {
			break
		}
		v.preview = append(v.preview, b.Author+" — "+b.Title)
	}
	return v
}

func drawDeleteConfirm(c Canvas, l layout, v deleteConfirmView) {
	title := l.font(c, 64, true)
	body := l.font(c, 32, false)
	small := l.font(c, 26, false)
	btnFont := l.font(c, 44, true)

	// Same header grid as every other screen: see drawSpaceWarn.
	c.SetFont(title, black)
	c.Text(image.Point{X: l.margin, Y: l.sy(140)}, "Confirm deletion")
	c.SetFont(small, darkGray)
	c.Text(image.Point{X: l.margin, Y: l.sy(210)},
		fmt.Sprintf("%d books are no longer on the server.", v.total))
	l.drawHairline(c, l.margin, l.screen.X-l.margin, l.sy(240))

	c.SetFont(body, black)
	c.Text(image.Point{X: l.margin, Y: l.sy(310)}, "Delete them from this device?")

	// The titles are indented and muted: they are what is at stake, but
	// the question above them is what the user answers.
	c.SetFont(body, darkGray)
	y := l.sy(380)
	for _, label := range v.preview {
		c.Text(image.Point{X: l.margin + l.sx(40), Y: y}, truncate(label, 60))
		y += l.sy(50)
	}
	if rest := v.total - len(v.preview); rest > 0 {
		c.Text(image.Point{X: l.margin + l.sx(40), Y: y}, fmt.Sprintf("… and %d more", rest))
	}

	// Keep is on the left as the safer option, which is also where the
	// Back key's answer lands; Delete is the primary on the right.
	drawDialogButton(c, btnFont, l.confirmLeftButton, "Keep", false)
	drawDialogButton(c, btnFont, l.confirmRightButton, "Delete", true)
}

// drawDialogButton paints one of a confirmation's two footer buttons.
// The primary carries the same double border the main screen's Sync
// button uses, so the action a dialog leads with reads as heavier than
// the one beside it.
func drawDialogButton(c Canvas, f Face, r image.Rectangle, label string, primary bool) {
	c.Rect(r, black)
	if primary {
		c.Rect(r.Inset(2), black)
	}
	drawCenteredText(c, f, r, label)
}
