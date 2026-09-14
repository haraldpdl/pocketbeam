// Drawing for the settings screen: the row list that reaches every other
// screen. It paints from a snapshot of the active profile, so it renders
// the same on the device and into an image on a build machine; the taps
// each row answers live in ui_settings_arm.go.

package main

import (
	"image"
)

// deleteMissingTitle names the delete-missing row; deleteMissingSubtitle
// is its value line. Both the full draw and the in-place refresh that
// follows a tap on the row go through these.
const deleteMissingTitle = "Delete missing"

func deleteMissingSubtitle(on bool) string {
	if on {
		return "Remove books deleted on server"
	}
	return "Keep books removed on server"
}

// sectionLabelPx is the size of the small all-caps section headers.
// computeLayout centres each label's glyph box in the band above the
// section's first row, so both have to agree on how tall it is.
const sectionLabelPx = 24

// settingsView is what the settings rows show, resolved before the draw
// starts: the active profile's values and the version of a waiting
// update ("" when none).
type settingsView struct {
	host    string
	profile string
	// filterTitle and filterValue are the library row: an OPDS profile
	// syncs a shelf filter, a WebDAV one a folder.
	filterTitle   string
	filterValue   string
	deleteMissing bool
	// currentVer is the running build, updateVer the version of a
	// waiting update ("" when none). Carried on the view rather than
	// read from the build-time global during the draw, so a rendered
	// screen does not depend on how the renderer was built.
	currentVer string
	updateVer  string
}

// settingsViewOf reads the rows out of the active profile. updateVer is
// the version of an update already found by the checker, which turns the
// About row from a check into an install.
func settingsViewOf(cfg *Config, updateVer string) settingsView {
	v := settingsView{
		host:          cfg.Host,
		profile:       cfg.Profile,
		filterTitle:   "Sync filter",
		filterValue:   cfg.FilterLabel(),
		deleteMissing: cfg.DeleteMissing,
		currentVer:    version,
		updateVer:     updateVer,
	}
	if cfg.Backend == BackendWebDAV {
		v.filterTitle = "Sync folder"
		v.filterValue = cfg.Path
		if v.filterValue == "" {
			v.filterValue = "/"
		}
	}
	return v
}

func drawSettings(c Canvas, l layout, v settingsView) {
	title := l.font(c, 64, true)
	rowTitleFont := l.font(c, 36, true)
	rowSubFont := l.font(c, 28, false)
	sectionFont := l.font(c, sectionLabelPx, true)
	btnFont := l.font(c, 44, true)

	// Header
	c.SetFont(title, black)
	c.Text(image.Point{X: l.margin, Y: l.sy(140)}, "Settings")

	// SERVER section
	l.drawSectionLabel(c, sectionFont, "SERVER", l.serverLabelY)
	l.drawListRow(c, rowTitleFont, rowSubFont, l.serverRow,
		"Server", truncate(v.host, 40), true)
	l.drawListRow(c, rowTitleFont, rowSubFont, l.profileRow,
		"Profile", v.profile, true)
	l.drawHairline(c, l.profileRow.Min.X, l.profileRow.Max.X, l.profileRow.Max.Y)

	// LIBRARY section
	l.drawSectionLabel(c, sectionFont, "LIBRARY", l.libraryLabelY)
	l.drawListRow(c, rowTitleFont, rowSubFont, l.filterRow,
		v.filterTitle, v.filterValue, true)
	l.drawToggleRow(c, rowTitleFont, rowSubFont, l.deleteRow,
		deleteMissingTitle, deleteMissingSubtitle(v.deleteMissing), v.deleteMissing)

	// ABOUT section
	l.drawSectionLabel(c, sectionFont, "ABOUT", l.aboutLabelY)
	updateTitle := "Check for updates"
	if v.updateVer != "" {
		updateTitle = "Install update " + v.updateVer
	}
	l.drawListRow(c, rowTitleFont, rowSubFont, l.updateRow,
		updateTitle, "Current version "+v.currentVer, true)
	l.drawHairline(c, l.updateRow.Min.X, l.updateRow.Max.X, l.updateRow.Max.Y)

	// Back button in the bottom-left, matching other screens.
	c.Rect(l.backButton, black)
	drawCenteredText(c, btnFont, l.backButton, "Back")
}
