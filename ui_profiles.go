// Drawing for the two server-profile screens: the paginated list of
// profiles and the per-profile detail panel. Both paint from a snapshot
// of what the profile file holds, so they render the same on the device
// and into an image on a build machine; the taps, the profile switch and
// the delete live in ui_profiles_arm.go.

package main

import (
	"fmt"
	"image"
)

// profileListView is the profile list resolved before the draw starts:
// the profile names, which one is active, the error that replaces the
// list when the file cannot be read, and the page the user is on.
type profileListView struct {
	names  []string
	active string
	err    error
	offset int
}

// rows renders the list as the shared row idiom. The active profile is
// the only one with a subtitle, so it reads as the current one at a
// glance. Shared by the draw and the pointer handler's page geometry,
// which has to count the same rows the draw lays out.
func (v profileListView) rows() []listRow {
	rows := make([]listRow, 0, len(v.names))
	for _, n := range v.names {
		sub := ""
		if n == v.active {
			sub = "Active"
		}
		rows = append(rows, listRow{title: n, subtitle: sub})
	}
	return rows
}

func drawProfileList(c Canvas, l layout, v profileListView) {
	title := l.font(c, 64, true)
	body := l.font(c, 32, false)
	rowTitleFont := l.font(c, 36, true)
	rowSubFont := l.font(c, 28, false)
	btnFont := l.font(c, 44, true)
	smallFont := l.font(c, 26, false)

	c.SetFont(title, black)
	c.Text(image.Point{X: l.margin, Y: l.sy(140)}, "Server profiles")

	if v.err != nil {
		c.SetFont(body, black)
		c.Text(image.Point{X: l.margin, Y: l.sy(240)}, "Could not list profiles:")
		c.Text(image.Point{X: l.margin, Y: l.sy(290)}, truncate(v.err.Error(), 60))
		c.Rect(l.backButton, black)
		drawCenteredText(c, btnFont, l.backButton, "Back")
		return
	}

	// One list row per profile, paginated like the pickers so a long
	// profile list cannot overrun the buttons below it. Tap anywhere on a
	// row to open the detail panel.
	l.drawPagedList(c,
		listFonts{rowTitle: rowTitleFont, rowSub: rowSubFont, button: btnFont, label: smallFont},
		l.pickerAreaTop, l.profileListBottom, v.rows(), v.offset)

	// Primary action: Add new server, sized like the Sync buttons so it
	// reads as the same kind of commit action.
	c.Rect(l.profileAction, black)
	c.Rect(l.profileAction.Inset(2), black)
	drawCenteredText(c, btnFont, l.profileAction, "Add new server")

	c.Rect(l.backButton, black)
	drawCenteredText(c, btnFont, l.backButton, "Back")
}

// profileDetailView is one profile's panel: its stored fields, whether
// it is the profile the app is currently on, and the error that replaces
// them when the profile cannot be read (a config file caught mid-edit).
type profileDetailView struct {
	name     string
	backend  string
	host     string
	isActive bool
	loadErr  error
	// confirming arms the delete: the first tap swaps the button's label
	// for the confirmation, the second commits.
	confirming bool
	// lastProfile is true when deleting this one would leave none, which
	// resets the app to the first-run wizard. The confirmation says so
	// instead of naming the profile.
	lastProfile bool
}

// canMakeActive reports whether the panel offers the Make active
// button. The active profile has nothing to switch to, and a profile
// that could not be read has no server details to switch to either. The
// pointer handler asks the same question, so a tap in that slot is only
// taken when the button is on screen.
func (v profileDetailView) canMakeActive() bool {
	return !v.isActive && v.loadErr == nil
}

// deleteLabel is what the delete button reads: the plain action, or the
// confirmation the second tap commits.
func (v profileDetailView) deleteLabel() string {
	if !v.confirming {
		return "Delete this profile"
	}
	if v.lastProfile {
		return "Tap again to reset pocketbeam"
	}
	return fmt.Sprintf("Tap again to delete \"%s\"", v.name)
}

func drawProfileDetail(c Canvas, l layout, v profileDetailView) {
	title := l.font(c, 64, true)
	body := l.font(c, 32, false)
	small := l.font(c, 26, false)
	btnFont := l.font(c, 44, true)

	// Header on the shared 140 / 210 / 240 grid: a 64px title occupies a
	// glyph box down to sy(204), so the muted line under it starts at
	// sy(210) and the hairline closes the block at sy(240).
	c.SetFont(title, black)
	c.Text(image.Point{X: l.margin, Y: l.sy(140)}, truncate(v.name, 40))
	c.SetFont(small, darkGray)
	status := "Profile"
	if v.isActive {
		status = "Profile · Active"
	}
	c.Text(image.Point{X: l.margin, Y: l.sy(210)}, status)
	l.drawHairline(c, l.margin, l.screen.X-l.margin, l.sy(240))

	c.SetFont(body, black)
	if v.loadErr != nil {
		c.Text(image.Point{X: l.margin, Y: l.sy(310)}, "Could not load profile:")
		c.SetFont(body, darkGray)
		c.Text(image.Point{X: l.margin, Y: l.sy(360)}, truncate(v.loadErr.Error(), 60))
	} else {
		c.Text(image.Point{X: l.margin, Y: l.sy(310)}, v.backend)
		c.SetFont(body, darkGray)
		c.Text(image.Point{X: l.margin, Y: l.sy(360)}, truncate(v.host, 55))
	}

	// Action stack anchored above Back: Delete (always) with Make active
	// on top of it, and the armed delete taking the second border that
	// marks a primary action.
	if v.canMakeActive() {
		c.Rect(l.profileUpperAction, black)
		c.Rect(l.profileUpperAction.Inset(2), black)
		drawCenteredText(c, btnFont, l.profileUpperAction, "Make active")
	}
	c.Rect(l.profileAction, black)
	if v.confirming {
		c.Rect(l.profileAction.Inset(2), black)
	}
	drawCenteredText(c, btnFont, l.profileAction, truncate(v.deleteLabel(), 40))

	c.Rect(l.backButton, black)
	drawCenteredText(c, btnFont, l.backButton, "Back")
}
