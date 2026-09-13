// Settings screen: the row list that reaches every other screen.

package main

import (
	"image"

	ink "github.com/dennwc/inkview"
)

func (a *app) drawSettings() {
	title := ink.OpenFont(ink.DefaultFontBold, a.layout.fpx(64), true)
	defer title.Close()

	rowTitleFont := ink.OpenFont(ink.DefaultFontBold, a.layout.fpx(36), true)
	defer rowTitleFont.Close()

	rowSubFont := ink.OpenFont(ink.DefaultFont, a.layout.fpx(28), true)
	defer rowSubFont.Close()

	sectionFont := ink.OpenFont(ink.DefaultFontBold, a.layout.fpx(24), true)
	defer sectionFont.Close()

	btnFont := ink.OpenFont(ink.DefaultFontBold, a.layout.fpx(44), true)
	defer btnFont.Close()

	// Header
	title.SetActive(ink.Black)
	ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(140)}, "Settings")

	// SERVER section
	a.drawSectionLabel(sectionFont, "SERVER", a.layout.serverLabelY)
	a.drawListRow(rowTitleFont, rowSubFont, a.layout.serverRow,
		"Server", truncate(a.cfg.Host, 40), true)
	a.drawListRow(rowTitleFont, rowSubFont, a.layout.profileRow,
		"Profile", a.cfg.Profile, true)
	a.drawHairline(a.layout.profileRow.Min.X, a.layout.profileRow.Max.X, a.layout.profileRow.Max.Y)

	// LIBRARY section
	a.drawSectionLabel(sectionFont, "LIBRARY", a.layout.libraryLabelY)
	filterTitle := "Sync filter"
	var filterValue string
	if a.cfg.Backend == BackendWebDAV {
		filterTitle = "Sync folder"
		filterValue = a.cfg.Path
		if filterValue == "" {
			filterValue = "/"
		}
	} else {
		filterValue = a.cfg.FilterLabel()
	}
	a.drawListRow(rowTitleFont, rowSubFont, a.layout.filterRow,
		filterTitle, filterValue, true)

	deleteSub := "Keep books removed on server"
	if a.cfg.DeleteMissing {
		deleteSub = "Remove books deleted on server"
	}
	a.drawListRow(rowTitleFont, rowSubFont, a.layout.deleteRow,
		"Delete missing", deleteSub, false)
	a.drawToggle(a.layout.deleteToggle, a.cfg.DeleteMissing)
	a.drawHairline(a.layout.deleteRow.Min.X, a.layout.deleteRow.Max.X, a.layout.deleteRow.Max.Y)

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
	btnFont.SetActive(ink.Black)
	drawCenteredText(btnFont, a.layout.backButton, "Back", a.layout.fpx(44))
}

func (a *app) settingsKey(e ink.KeyEvent) bool {
	if e.Key == ink.KeyOk {
		a.startChangeInfo()
		return true
	}
	if e.Key == ink.KeyNext {
		if a.cfg.Backend == BackendWebDAV {
			a.openDirPicker(a.cfg.Path)
		} else {
			a.openShelfPicker()
		}
		return true
	}
	return false
}

func (a *app) settingsPointer(e ink.PointerEvent) bool {
	if e.State != ink.PointerDown {
		return false
	}
	p := e.Point
	switch {
	case p.In(a.layout.serverRow):
		a.startChangeInfo()
		return true
	case p.In(a.layout.profileRow):
		a.openProfileList()
		return true
	case p.In(a.layout.filterRow):
		if a.cfg.Backend == BackendWebDAV {
			a.openDirPicker(a.cfg.Path)
		} else {
			a.openShelfPicker()
		}
		return true
	case p.In(a.layout.deleteRow):
		a.cfg.DeleteMissing = !a.cfg.DeleteMissing
		_ = SaveConfig(a.cfgPath, a.cfg)
		ink.Repaint()
		return true
	case p.In(a.layout.updateRow):
		a.openUpdateScreen()
		return true
	case p.In(a.layout.backButton):
		a.screen = screenMain
		ink.Repaint()
		return true
	}
	return false
}

// startChangeInfo re-enters the wizard to replace server URL, username, and
// password. On success, the existing store and client are closed and swapped
// for new ones against the updated config.
func (a *app) startChangeInfo() {
	a.wizard.step = stepURL
	a.wizard.err = nil
	a.wizard.url = ""
	a.wizard.user = ""
	a.wizard.pass = ""
	a.screen = screenFirstRun
	ink.OpenKeyboard("https://library.example.com:8083", 512)
}
