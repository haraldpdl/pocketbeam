// Settings screen: the row list that reaches every other screen.

package main

import (
	"image"

	ink "github.com/dennwc/inkview"
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

func (a *app) drawSettings() {
	title := a.font(ink.DefaultFontBold, 64)
	rowTitleFont := a.font(ink.DefaultFontBold, 36)
	rowSubFont := a.font(ink.DefaultFont, 28)
	sectionFont := a.font(ink.DefaultFontBold, 24)
	btnFont := a.font(ink.DefaultFontBold, 44)

	cfg := a.Config()

	// Header
	title.SetActive(ink.Black)
	ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(140)}, "Settings")

	// SERVER section
	a.drawSectionLabel(sectionFont, "SERVER", a.layout.serverLabelY)
	a.drawListRow(rowTitleFont, rowSubFont, a.layout.serverRow,
		"Server", truncate(cfg.Host, 40), true)
	a.drawListRow(rowTitleFont, rowSubFont, a.layout.profileRow,
		"Profile", cfg.Profile, true)
	a.drawHairline(a.layout.profileRow.Min.X, a.layout.profileRow.Max.X, a.layout.profileRow.Max.Y)

	// LIBRARY section
	a.drawSectionLabel(sectionFont, "LIBRARY", a.layout.libraryLabelY)
	filterTitle := "Sync filter"
	var filterValue string
	if cfg.Backend == BackendWebDAV {
		filterTitle = "Sync folder"
		filterValue = cfg.Path
		if filterValue == "" {
			filterValue = "/"
		}
	} else {
		filterValue = cfg.FilterLabel()
	}
	a.drawListRow(rowTitleFont, rowSubFont, a.layout.filterRow,
		filterTitle, filterValue, true)

	a.drawToggleRow(rowTitleFont, rowSubFont, a.layout.deleteRow,
		deleteMissingTitle, deleteMissingSubtitle(cfg.DeleteMissing), cfg.DeleteMissing)

	// ABOUT section
	a.drawSectionLabel(sectionFont, "ABOUT", a.layout.aboutLabelY)
	updateTitle := "Check for updates"
	updateSub := "Current version " + version
	a.update.mu.Lock()
	if a.update.available && a.update.release.Version != "" {
		updateTitle = "Install update " + a.update.release.Version
	}
	a.update.mu.Unlock()
	a.drawListRow(rowTitleFont, rowSubFont, a.layout.updateRow,
		updateTitle, updateSub, true)
	a.drawHairline(a.layout.updateRow.Min.X, a.layout.updateRow.Max.X, a.layout.updateRow.Max.Y)

	// Back button in the bottom-left, matching other screens.
	ink.DrawRect(a.layout.backButton, ink.Black)
	drawCenteredText(btnFont, a.layout.backButton, "Back", a.layout.fpx(44))
}

func (a *app) settingsKey(e ink.KeyEvent) bool {
	if e.Key == ink.KeyOk {
		a.startChangeInfo()
		return true
	}
	if e.Key == ink.KeyNext {
		if cfg := a.Config(); cfg.Backend == BackendWebDAV {
			a.openDirPicker(cfg.Path)
		} else {
			a.openShelfPicker()
		}
		return true
	}
	return false
}

func (a *app) settingsPointer(e ink.PointerEvent) bool {
	p := e.Point
	switch {
	case p.In(a.layout.serverRow):
		a.startChangeInfo()
		return true
	case p.In(a.layout.profileRow):
		a.openProfileList()
		return true
	case p.In(a.layout.filterRow):
		if cfg := a.Config(); cfg.Backend == BackendWebDAV {
			a.openDirPicker(cfg.Path)
		} else {
			a.openShelfPicker()
		}
		return true
	case p.In(a.layout.deleteRow):
		// Nothing else on the screen depends on the setting, so the row
		// redraws itself instead of costing a full-screen refresh.
		if cfg := a.saveConfigChange(func(c *Config) { c.DeleteMissing = !c.DeleteMissing }); cfg != nil {
			a.refreshToggleRow(a.layout.deleteRow, deleteMissingTitle,
				deleteMissingSubtitle(cfg.DeleteMissing), cfg.DeleteMissing)
		}
		return true
	case p.In(a.layout.updateRow):
		a.openUpdateScreen()
		return true
	case p.In(a.layout.backButton):
		a.SetScreen(screenMain)
		ink.Repaint()
		return true
	}
	return false
}

// startChangeInfo re-enters the wizard to replace server URL, username, and
// password. On success, the existing store and client are closed and swapped
// for new ones against the updated config; the recorded returnTo sends the
// Back key back to Settings instead of out of the app.
func (a *app) startChangeInfo() {
	a.UpdateWizard(func(w *wizardState) {
		w.step = stepURL
		w.err = nil
		w.url = ""
		w.user = ""
		w.pass = ""
		w.returnTo = screenSettings
	})
	a.SetScreen(screenFirstRun)
	ink.OpenKeyboard("https://library.example.com:8083", 512)
}
