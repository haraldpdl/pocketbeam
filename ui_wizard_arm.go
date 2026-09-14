// First-run wizard: profile name, backend choice, URL / user / password
// entry, and the connection probe that writes the first config. The
// wizard's state lives in appState (the probe runs on its own goroutine);
// the drawing is in ui_wizard.go.

package main

import (
	"context"
	"fmt"
	"path/filepath"

	ink "github.com/dennwc/inkview"
)

func (a *app) wizardKey(e ink.KeyEvent) bool {
	switch a.Wizard().step {
	case stepWelcome:
		if e.Key == ink.KeyOk || e.Key == ink.KeyNext {
			a.setWizardStep(stepBackend)
			ink.Repaint()
			return true
		}
	case stepBackend:
		// Two options; OK defaults to OPDS (the more common setup), Next
		// jumps to WebDAV. Users can also tap the button they want.
		if e.Key == ink.KeyOk {
			a.pickBackend(BackendOPDS)
			return true
		}
		if e.Key == ink.KeyNext {
			a.pickBackend(BackendWebDAV)
			return true
		}
	case stepProfileName:
		if e.Key == ink.KeyOk {
			ink.OpenKeyboard("new-profile-name", 40)
			return true
		}
	case stepError:
		if e.Key == ink.KeyOk || e.Key == ink.KeyNext {
			a.startURLEntry()
			return true
		}
	case stepURL, stepUser, stepPass:
		if e.Key == ink.KeyOk {
			a.reopenKeyboardForStep()
			return true
		}
	}
	return false
}

func (a *app) wizardPointer(e ink.PointerEvent) bool {
	wiz := a.Wizard()
	switch wiz.step {
	case stepWelcome:
		a.setWizardStep(stepBackend)
		ink.Repaint()
		return true
	case stepBackend:
		if e.Point.In(a.layout.wizardOPDSRow) {
			a.pickBackend(BackendOPDS)
			return true
		}
		if e.Point.In(a.layout.wizardWebDAVRow) {
			a.pickBackend(BackendWebDAV)
			return true
		}
	case stepProfileName:
		ink.OpenKeyboard("new-profile-name", 40)
		return true
	case stepError:
		a.startURLEntry()
		return true
	case stepURL, stepUser, stepPass:
		a.reopenKeyboardForStep()
		return true
	}
	return false
}

// pickBackend records the chosen backend on the wizard state and advances
// to the URL entry step. OPDS suggests a Calibre-Web URL; WebDAV suggests
// a Nextcloud-style URL to nudge the user into the right format.
func (a *app) pickBackend(b string) {
	a.UpdateWizard(func(w *wizardState) { w.backend = b })
	a.startURLEntry()
}

// setWizardStep advances the wizard without changing anything else.
func (a *app) setWizardStep(step wizardStep) {
	a.UpdateWizard(func(w *wizardState) { w.step = step })
}

// reopenKeyboardForStep pops the right keyboard for the current wizard step.
// Used when OpenKeyboard called directly from a keyboard handler races and
// the chained keyboard does not actually appear, so the user taps the screen
// or presses OK to request it explicitly.
func (a *app) reopenKeyboardForStep() {
	switch a.Wizard().step {
	case stepURL:
		ink.OpenKeyboard(a.urlHint(), 512)
	case stepUser:
		ink.OpenKeyboard("Username", 128)
	case stepPass:
		ink.OpenKeyboard("Password", 128)
	}
}

func (a *app) startURLEntry() {
	a.UpdateWizard(func(w *wizardState) {
		w.step = stepURL
		w.err = nil
	})
	ink.OpenKeyboard(a.urlHint(), 512)
}

func (a *app) urlHint() string {
	if a.Wizard().backend == BackendWebDAV {
		return "https://nc.example.com/remote.php/dav/files/alice"
	}
	return "https://library.example.com:8083"
}

// onKeyboardInput routes based on current wizard step.
func (a *app) onKeyboardInput(text string) {
	switch a.Wizard().step {
	case stepProfileName:
		name := sanitizeProfileName(text)
		if name == "" {
			ink.OpenKeyboard("new-profile-name", 40)
			return
		}
		if profileNameTaken(a.cfgPath, name) {
			// Reusing a name would make SaveConfig overwrite that
			// profile's section with a library derived without it,
			// orphaning the books it already downloaded.
			a.UpdateWizard(func(w *wizardState) { w.err = fmt.Errorf("profile %q already exists", name) })
			ink.Repaint()
			ink.OpenKeyboard("new-profile-name", 40)
			return
		}
		a.UpdateWizard(func(w *wizardState) {
			w.name = name
			w.err = nil
			w.step = stepBackend
		})
		ink.Repaint()
	case stepURL:
		a.UpdateWizard(func(w *wizardState) {
			w.url = text
			w.step = stepUser
		})
		ink.OpenKeyboard("Username", 128)
	case stepUser:
		a.UpdateWizard(func(w *wizardState) {
			w.user = text
			w.step = stepPass
		})
		ink.OpenKeyboard("Password", 128)
	case stepPass:
		a.UpdateWizard(func(w *wizardState) {
			w.pass = text
			w.step = stepTesting
		})
		ink.Repaint()
		go a.runProbe()
	}
}

// runProbe probes the server and transitions the UI on success or failure.
// Runs on its own goroutine, so it reads the entered details as one
// snapshot and publishes every result through the appState accessors.
// The config to be written is assembled before the probe so the probe
// tests exactly the backend and credentials that get saved; resolving
// the profile being edited (and merging it) lives in config.go, where
// amd64 tests can reach it.
func (a *app) runProbe() {
	in := a.Wizard()
	cur := resolveProfileForProbe(a.cfgPath, a.Config(), in.addProfile)
	cfg := profileAfterProbe(a.cfgPath, cur, probeInput{
		Profile:  in.name,
		Backend:  in.backend,
		Host:     in.url,
		User:     in.user,
		Pass:     in.pass,
		BooksDir: filepath.Join(ink.FlashDir, "Books"),
		StateDB:  filepath.Join(ink.ConfigPath, "pocketbeam.db"),
	})
	var err error
	switch cfg.Backend {
	case BackendWebDAV:
		// The root is probed rather than cfg.Path: the credentials are
		// what is under test, and a profile scoped to a sub-directory
		// should still be reachable from the top.
		err = ProbeWebDAV(context.Background(), cfg.Host, cfg.User, cfg.Pass, "/")
	default:
		err = ProbeCWA(context.Background(), cfg.Host, cfg.User, cfg.Pass)
	}
	if err != nil {
		a.failWizard(err)
		return
	}
	if err := SaveConfig(a.cfgPath, cfg); err != nil {
		a.failWizard(err)
		return
	}
	// SaveConfig writes the section but only sets `active` when the file
	// has none, so the marker can still point at another profile: the one
	// the user is adding from Settings, or the broken section that dropped
	// the app into the wizard in the first place. Whatever the wizard just
	// wrote is what the user set up, so make it the working profile.
	if err := SetActiveProfile(a.cfgPath, cfg.Profile); err != nil {
		a.failWizard(err)
		return
	}
	client, err := NewClient(cfg.Host, cfg.User, cfg.Pass)
	if err != nil {
		a.failWizard(err)
		return
	}
	store, err := OpenStore(cfg.StateDB)
	if err != nil {
		a.failWizard(err)
		return
	}
	// SetSession closes the store handle it replaces (settings -> edit).
	a.SetSession(cfg, client, store)
	a.refreshMainStats()
	a.SetScreen(screenMain)
	// Clear the entered credentials only once the main screen is the
	// target, so a repaint landing in between never draws a blank wizard.
	a.UpdateWizard(func(w *wizardState) { *w = wizardState{} })
	ink.Repaint()
}

// failWizard parks err on the wizard and shows the error step. Both
// fields move together so the error screen can never draw the previous
// attempt's message.
func (a *app) failWizard(err error) {
	a.UpdateWizard(func(w *wizardState) {
		w.err = err
		w.step = stepError
	})
	ink.Repaint()
}
