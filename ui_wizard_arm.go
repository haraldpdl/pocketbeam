// First-run wizard: profile name, backend choice, URL / user / password
// entry, and the connection probe that writes the first config. The
// wizard's state lives in appState (the probe runs on its own goroutine).

package main

import (
	"context"
	"image"
	"path/filepath"
	"strings"

	ink "github.com/dennwc/inkview"
)

func (a *app) drawWizard() {
	title := a.font(ink.DefaultFontBold, 54)
	title.SetActive(ink.Black)

	body := a.font(ink.DefaultFont, 32)
	body.SetActive(ink.Black)

	// One snapshot for the whole pass: the probe goroutine can move the
	// wizard on mid-draw, and a step drawn with the next step's error
	// would be worse than a pass that is one repaint behind.
	wiz := a.Wizard()

	switch wiz.step {
	case stepWelcome:
		title.SetActive(ink.Black)
		ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(200)}, "pocketbeam")
		body.SetActive(ink.DarkGray)
		ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(260)}, "Wireless sync from a book server.")
		a.drawHairline(a.layout.margin, a.layout.screen.X-a.layout.margin, a.layout.sy(300))

		body.SetActive(ink.Black)
		ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(370)}, "Supported servers")
		body.SetActive(ink.DarkGray)
		ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(430)}, "· Calibre-Web and any OPDS catalog")
		ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(480)}, "· Nextcloud, Synology, ownCloud, WebDAV")

		body.SetActive(ink.Black)
		ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(620)}, "Press OK or tap to begin.")

	case stepProfileName:
		title.SetActive(ink.Black)
		ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(200)}, "Name this profile")
		body.SetActive(ink.DarkGray)
		ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(260)}, "Short identifier for this server (no spaces).")
		a.drawHairline(a.layout.margin, a.layout.screen.X-a.layout.margin, a.layout.sy(300))
		body.SetActive(ink.Black)
		ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(370)}, "Tap or press OK to re-open the keyboard.")
		if wiz.name != "" {
			body.SetActive(ink.DarkGray)
			ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(480)}, "Name: "+wiz.name)
		}

	case stepBackend:
		title.SetActive(ink.Black)
		ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(200)}, "Choose server type")
		body.SetActive(ink.DarkGray)
		ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(260)}, "Tap the option that matches your server.")

		rowTitleFont := a.font(ink.DefaultFontBold, 36)
		rowSubFont := a.font(ink.DefaultFont, 28)

		w := a.layout.screen.X
		btnW := w - 2*a.layout.margin
		opdsBtn := image.Rect(a.layout.margin, a.layout.sy(360), a.layout.margin+btnW, a.layout.sy(500))
		webdavBtn := image.Rect(a.layout.margin, a.layout.sy(500), a.layout.margin+btnW, a.layout.sy(640))
		a.UpdateWizard(func(w *wizardState) {
			w.opdsBtn, w.webdavBtn = opdsBtn, webdavBtn
		})

		a.drawListRow(rowTitleFont, rowSubFont, opdsBtn,
			"Calibre-Web / OPDS", "Calibre-Web, COPS, and other OPDS catalogs", true)
		a.drawListRow(rowTitleFont, rowSubFont, webdavBtn,
			"WebDAV / Nextcloud", "Nextcloud, Synology, ownCloud, generic WebDAV", true)
		a.drawHairline(webdavBtn.Min.X, webdavBtn.Max.X, webdavBtn.Max.Y)

	case stepURL, stepUser, stepPass:
		title.SetActive(ink.Black)
		ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(200)}, "pocketbeam setup")
		body.SetActive(ink.DarkGray)
		var step, what string
		switch wiz.step {
		case stepURL:
			step, what = "Step 1 of 3", "Server URL"
		case stepUser:
			step, what = "Step 2 of 3", "Username"
		case stepPass:
			step, what = "Step 3 of 3", "Password"
		}
		ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(260)}, step+"  ·  "+what)
		a.drawHairline(a.layout.margin, a.layout.screen.X-a.layout.margin, a.layout.sy(300))
		body.SetActive(ink.Black)
		ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(370)}, "Tap or press OK to open the keyboard.")
		body.SetActive(ink.DarkGray)
		y := a.layout.sy(490)
		if wiz.url != "" {
			ink.DrawString(image.Point{X: a.layout.margin, Y: y}, "Server: "+truncate(wiz.url, 48))
			y += a.layout.sy(50)
		}
		if wiz.user != "" {
			ink.DrawString(image.Point{X: a.layout.margin, Y: y}, "User: "+truncate(wiz.user, 48))
		}

	case stepTesting:
		title.SetActive(ink.Black)
		ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(300)}, "Testing connection")
		body.SetActive(ink.DarkGray)
		ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(370)}, truncate(wiz.url, 55))
		a.showHourglassAt(image.Point{X: a.layout.margin, Y: a.layout.sy(480)})

	case stepError:
		title.SetActive(ink.Black)
		ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(200)}, "Connection failed")
		body.SetActive(ink.DarkGray)
		ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(260)}, "Check that the server URL and credentials are correct.")
		a.drawHairline(a.layout.margin, a.layout.screen.X-a.layout.margin, a.layout.sy(300))
		msg := "Unknown error"
		if wiz.err != nil {
			msg = wiz.err.Error()
		}
		body.SetActive(ink.Black)
		ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(370)}, truncate(msg, 60))
		body.SetActive(ink.DarkGray)
		ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(620)}, "Press OK or tap to try again.")
	}
}

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
		if e.Point.In(wiz.opdsBtn) {
			a.pickBackend(BackendOPDS)
			return true
		}
		if e.Point.In(wiz.webdavBtn) {
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
		a.UpdateWizard(func(w *wizardState) {
			w.name = name
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

// sanitizeProfileName strips whitespace and characters that would confuse
// the config-file section parser (brackets, equals). Empty input returns
// "" so the wizard can re-ask.
func sanitizeProfileName(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, " ", "-")
	s = strings.Trim(s, "[]=")
	return s
}

// runProbe probes the server and transitions the UI on success or failure.
// Runs on its own goroutine, so it reads the entered details as one
// snapshot and publishes every result through the appState accessors.
func (a *app) runProbe() {
	in := a.Wizard()
	backend := in.backend
	if backend == "" {
		backend = BackendOPDS
	}
	var err error
	switch backend {
	case BackendWebDAV:
		err = ProbeWebDAV(context.Background(), in.url, in.user, in.pass, "/")
	default:
		err = ProbeCWA(context.Background(), in.url, in.user, in.pass)
	}
	if err != nil {
		a.failWizard(err)
		return
	}
	libraryDir := "CWA"
	if backend == BackendWebDAV {
		libraryDir = "WebDAV"
	}
	profile := in.name
	if profile == "" {
		profile = defaultProfileName
	}
	cfg := &Config{
		Profile:      profile,
		Backend:      backend,
		Host:         in.url,
		User:         in.user,
		Pass:         in.pass,
		Library:      filepath.Join(ink.FlashDir, "Books", libraryDir),
		StateDB:      filepath.Join(ink.ConfigPath, "pocketbeam.db"),
		CheckUpdates: true,
	}
	if backend == BackendWebDAV {
		cfg.Path = "/"
	}
	if err := SaveConfig(a.cfgPath, cfg); err != nil {
		a.failWizard(err)
		return
	}
	// When the wizard is being used to add a profile, the new section is
	// written but the file's `active` marker still points at the existing
	// one. Flip it so the newly-created profile becomes the working one.
	if in.addProfile {
		if err := SetActiveProfile(a.cfgPath, profile); err != nil {
			a.failWizard(err)
			return
		}
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
