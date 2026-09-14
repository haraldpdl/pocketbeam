// Drawing for the update screen. It paints from a snapshot of the
// self-update flow, so every state renders the same on the device and
// into an image on a build machine; the check, the download and the
// install live in ui_update_arm.go.

package main

import (
	"fmt"
	"image"
)

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

// updateSnapshot is one consistent read of the self-update flow: the
// release a check found, the active download's progress, and the
// terminal result once an install succeeds or fails. Drawing takes the
// lock once and works from the copy, so a check or a download landing
// mid-pass is never observed half applied.
type updateSnapshot struct {
	checking    bool
	available   bool
	rel         Release
	checkErr    error
	downloading bool
	downloaded  int64
	total       int64 // -1 until the HTTP response's Content-Length is known
	installErr  error
	installed   bool
}

// updateView is the whole screen: the flow's state plus the two values
// that come from outside it. currentVer is carried rather than read from
// the build-time global during the draw, so a rendered screen does not
// depend on how the renderer was built.
type updateView struct {
	updateSnapshot
	autoCheck  bool
	currentVer string
}

func updateViewOf(u updateSnapshot, autoCheck bool, currentVer string) updateView {
	return updateView{updateSnapshot: u, autoCheck: autoCheck, currentVer: currentVer}
}

// updateAction is what the screen's primary button does in the state it
// is drawn in, or updateActionNone when the flow is busy and the button
// is left off the screen entirely.
type updateAction int

const (
	updateActionNone updateAction = iota
	updateActionInstall
	updateActionCheck
)

// primaryAction decides the button. The draw labels it and the pointer
// handler acts on it, so a tap can only start what the user saw offered.
// It hangs off the flow's own state: nothing else on the screen bears on
// what the button does.
func (u updateSnapshot) primaryAction() updateAction {
	switch {
	case u.available && !u.installed && !u.downloading:
		return updateActionInstall
	case !u.downloading && !u.installed && !u.checking:
		return updateActionCheck
	}
	return updateActionNone
}

func drawUpdate(c Canvas, l layout, v updateView) {
	title := l.font(c, 64, true)
	rowTitleFont := l.font(c, 36, true)
	rowSubFont := l.font(c, 28, false)
	small := l.font(c, 26, false)
	btnFont := l.font(c, 44, true)

	// Header on the shared 140 / 210 / 240 grid, as on the main screen: a
	// 64px title occupies a glyph box down to sy(204), so the muted line
	// under it starts at sy(210) and the hairline closes the block.
	c.SetFont(title, black)
	c.Text(image.Point{X: l.margin, Y: l.sy(140)}, "Updates")
	c.SetFont(small, darkGray)
	c.Text(image.Point{X: l.margin, Y: l.sy(210)}, "Installed version "+v.currentVer)
	l.drawHairline(c, l.margin, l.screen.X-l.margin, l.sy(240))

	drawUpdateStatus(c, l, v.updateSnapshot)

	// Auto-check toggle row: list-row style matching Settings.
	l.drawToggleRow(c, rowTitleFont, rowSubFont, l.updateToggleRow,
		autoCheckTitle, autoCheckSubtitle(v.autoCheck), v.autoCheck)

	switch v.primaryAction() {
	case updateActionInstall:
		// Double border: installing is the screen's commit action.
		c.Rect(l.updatePrimaryButton, black)
		c.Rect(l.updatePrimaryButton.Inset(2), black)
		drawCenteredText(c, btnFont, l.updatePrimaryButton, "Install now")
	case updateActionCheck:
		c.Rect(l.updatePrimaryButton, black)
		drawCenteredText(c, btnFont, l.updatePrimaryButton, "Check for updates")
	}

	c.Rect(l.backButton, black)
	drawCenteredText(c, btnFont, l.backButton, "Back")
}

// drawUpdateStatus paints whichever of the flow's states u describes. It
// clears its own area first so it can be called on its own, outside a
// full repaint: the download goroutine pushes just this strip.
func drawUpdateStatus(c Canvas, l layout, u updateSnapshot) {
	hero := l.font(c, 40, true)
	body := l.font(c, 32, false)
	c.Fill(l.updateStatusArea, white)

	y := l.sy(310)
	switch {
	case u.installed:
		c.SetFont(hero, black)
		c.Text(image.Point{X: l.margin, Y: y}, "Installed "+u.rel.Version)
		c.SetFont(body, darkGray)
		c.Text(image.Point{X: l.margin, Y: y + l.sy(55)}, "Relaunching…")
	case u.installErr != nil:
		c.SetFont(hero, black)
		c.Text(image.Point{X: l.margin, Y: y}, "Install failed")
		c.SetFont(body, darkGray)
		c.Text(image.Point{X: l.margin, Y: y + l.sy(55)}, truncate(u.installErr.Error(), 60))
	case u.downloading:
		c.SetFont(hero, black)
		c.Text(image.Point{X: l.margin, Y: y}, "Downloading "+u.rel.Version)
		c.SetFont(body, darkGray)
		c.Text(image.Point{X: l.margin, Y: y + l.sy(55)}, downloadLine(u.downloaded, u.total))
		if u.total > 0 {
			barY := y + l.sy(90)
			drawDownloadBar(c, l, image.Rect(l.margin, barY, l.screen.X-l.margin, barY+l.sy(30)), u.downloaded, u.total)
		}
	case u.checking:
		c.SetFont(hero, black)
		c.Text(image.Point{X: l.margin, Y: y}, "Checking for updates…")
	case u.checkErr != nil:
		c.SetFont(hero, black)
		c.Text(image.Point{X: l.margin, Y: y}, "Could not check")
		c.SetFont(body, darkGray)
		c.Text(image.Point{X: l.margin, Y: y + l.sy(55)}, truncate(u.checkErr.Error(), 60))
	case u.available:
		c.SetFont(hero, black)
		c.Text(image.Point{X: l.margin, Y: y}, u.rel.Version+" is available")
		c.SetFont(body, darkGray)
		detail := "Tap Install now to update"
		if u.rel.BinarySize > 0 {
			detail = formatBytes(u.rel.BinarySize) + "  ·  " + detail
		}
		c.Text(image.Point{X: l.margin, Y: y + l.sy(55)}, detail)
		if u.rel.SHA256 != "" {
			c.Text(image.Point{X: l.margin, Y: y + l.sy(105)}, "sha256 "+u.rel.SHA256[:12]+"…")
		}
	default:
		c.SetFont(hero, black)
		c.Text(image.Point{X: l.margin, Y: y}, "You are up to date")
	}
}

// downloadLine is the line under the headline while a download runs. A
// server that sends no Content-Length leaves the total unknown, so the
// percentage is dropped rather than guessed.
func downloadLine(downloaded, total int64) string {
	if total <= 0 {
		return fmt.Sprintf("%d KB received", downloaded/1024)
	}
	pct := 100 * downloaded / total
	if pct > 100 {
		pct = 100
	}
	return fmt.Sprintf("%d%%  ·  %d / %d KB", pct, downloaded/1024, total/1024)
}

// drawDownloadBar fills r in proportion to how much of the release has
// arrived. A server that sends more bytes than it announced cannot push
// the fill past the border.
func drawDownloadBar(c Canvas, l layout, r image.Rectangle, downloaded, total int64) {
	c.Rect(r, black)
	inner := r.Dx() - 6
	fillW := int(int64(inner) * downloaded / total)
	if fillW > inner {
		fillW = inner
	}
	if fillW <= 0 {
		return
	}
	c.Fill(image.Rect(r.Min.X+l.sx(3), r.Min.Y+l.sy(3), r.Min.X+l.sx(3)+fillW, r.Max.Y-l.sy(3)), darkGray)
}
