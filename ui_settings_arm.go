// Settings screen: what each row answers when it is tapped, and the
// snapshot the shared drawing in ui_settings.go paints from.

package main

import (
	ink "github.com/dennwc/inkview"
)

// settingsView collects everything the settings rows show into one
// snapshot, so the drawing itself touches no shared state and can run
// off-device.
func (a *app) settingsView() settingsView {
	var updateVer string
	a.update.mu.Lock()
	if a.update.available {
		updateVer = a.update.release.Version
	}
	a.update.mu.Unlock()
	return settingsViewOf(a.Config(), updateVer)
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
