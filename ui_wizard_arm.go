// First-run wizard: profile name, backend choice, URL / user / password
// entry, and the connection probe that writes the first config.

package main

import (
	"context"
	"image"
	"path/filepath"
	"strings"

	ink "github.com/dennwc/inkview"
)

// wizardStep tracks where the user is in the first-run flow.
type wizardStep int

const (
	stepWelcome wizardStep = iota
	stepProfileName
	stepBackend
	stepURL
	stepUser
	stepPass
	stepTesting
	stepError
)

// wizardState holds the in-progress first-run input and result.
type wizardState struct {
	step    wizardStep
	backend string // "opds" or "webdav"
	url     string
	user    string
	pass    string
	name    string // profile name; "default" on the initial first run, user-entered when adding another
	err     error
	// addProfile is true when the wizard is being used to create another
	// profile from the profile-list screen rather than the initial
	// first-run setup. It controls where the wizard returns on success
	// and whether the profile-name step is shown.
	addProfile bool
	// Tap targets for the backend-choice step, captured during draw so the
	// pointer handler knows where the two buttons live.
	opdsBtn   image.Rectangle
	webdavBtn image.Rectangle
}

func (a *app) drawWizard() {
	title := ink.OpenFont(ink.DefaultFontBold, a.layout.fpx(54), true)
	defer title.Close()
	title.SetActive(ink.Black)

	body := ink.OpenFont(ink.DefaultFont, a.layout.fpx(32), true)
	defer body.Close()
	body.SetActive(ink.Black)

	switch a.wizard.step {
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
		if a.wizard.name != "" {
			body.SetActive(ink.DarkGray)
			ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(480)}, "Name: "+a.wizard.name)
		}

	case stepBackend:
		title.SetActive(ink.Black)
		ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(200)}, "Choose server type")
		body.SetActive(ink.DarkGray)
		ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(260)}, "Tap the option that matches your server.")

		rowTitleFont := ink.OpenFont(ink.DefaultFontBold, a.layout.fpx(36), true)
		defer rowTitleFont.Close()
		rowSubFont := ink.OpenFont(ink.DefaultFont, a.layout.fpx(28), true)
		defer rowSubFont.Close()

		w := a.layout.screen.X
		btnW := w - 2*a.layout.margin
		a.wizard.opdsBtn = image.Rect(a.layout.margin, a.layout.sy(360), a.layout.margin+btnW, a.layout.sy(500))
		a.wizard.webdavBtn = image.Rect(a.layout.margin, a.layout.sy(500), a.layout.margin+btnW, a.layout.sy(640))

		a.drawListRow(rowTitleFont, rowSubFont, a.wizard.opdsBtn,
			"Calibre-Web / OPDS", "Calibre-Web, COPS, and other OPDS catalogs", true)
		a.drawListRow(rowTitleFont, rowSubFont, a.wizard.webdavBtn,
			"WebDAV / Nextcloud", "Nextcloud, Synology, ownCloud, generic WebDAV", true)
		a.drawHairline(a.wizard.webdavBtn.Min.X, a.wizard.webdavBtn.Max.X, a.wizard.webdavBtn.Max.Y)

	case stepURL, stepUser, stepPass:
		title.SetActive(ink.Black)
		ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(200)}, "pocketbeam setup")
		body.SetActive(ink.DarkGray)
		var step, what string
		switch a.wizard.step {
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
		if a.wizard.url != "" {
			ink.DrawString(image.Point{X: a.layout.margin, Y: y}, "Server: "+truncate(a.wizard.url, 48))
			y += a.layout.sy(50)
		}
		if a.wizard.user != "" {
			ink.DrawString(image.Point{X: a.layout.margin, Y: y}, "User: "+truncate(a.wizard.user, 48))
		}

	case stepTesting:
		title.SetActive(ink.Black)
		ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(300)}, "Testing connection")
		body.SetActive(ink.DarkGray)
		ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(370)}, truncate(a.wizard.url, 55))
		a.showHourglassAt(image.Point{X: a.layout.margin, Y: a.layout.sy(480)})

	case stepError:
		title.SetActive(ink.Black)
		ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(200)}, "Connection failed")
		body.SetActive(ink.DarkGray)
		ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(260)}, "Check that the server URL and credentials are correct.")
		a.drawHairline(a.layout.margin, a.layout.screen.X-a.layout.margin, a.layout.sy(300))
		msg := "Unknown error"
		if a.wizard.err != nil {
			msg = a.wizard.err.Error()
		}
		body.SetActive(ink.Black)
		ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(370)}, truncate(msg, 60))
		body.SetActive(ink.DarkGray)
		ink.DrawString(image.Point{X: a.layout.margin, Y: a.layout.sy(620)}, "Press OK or tap to try again.")
	}
}

func (a *app) wizardKey(e ink.KeyEvent) bool {
	switch a.wizard.step {
	case stepWelcome:
		if e.Key == ink.KeyOk || e.Key == ink.KeyNext {
			a.wizard.step = stepBackend
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
	if e.State != ink.PointerDown {
		return false
	}
	switch a.wizard.step {
	case stepWelcome:
		a.wizard.step = stepBackend
		ink.Repaint()
		return true
	case stepBackend:
		if e.Point.In(a.wizard.opdsBtn) {
			a.pickBackend(BackendOPDS)
			return true
		}
		if e.Point.In(a.wizard.webdavBtn) {
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
	a.wizard.backend = b
	a.startURLEntry()
}

// reopenKeyboardForStep pops the right keyboard for the current wizard step.
// Used when OpenKeyboard called directly from a keyboard handler races and
// the chained keyboard does not actually appear, so the user taps the screen
// or presses OK to request it explicitly.
func (a *app) reopenKeyboardForStep() {
	switch a.wizard.step {
	case stepURL:
		ink.OpenKeyboard(a.urlHint(), 512)
	case stepUser:
		ink.OpenKeyboard("Username", 128)
	case stepPass:
		ink.OpenKeyboard("Password", 128)
	}
}

func (a *app) startURLEntry() {
	a.wizard.step = stepURL
	a.wizard.err = nil
	ink.OpenKeyboard(a.urlHint(), 512)
}

func (a *app) urlHint() string {
	if a.wizard.backend == BackendWebDAV {
		return "https://nc.example.com/remote.php/dav/files/alice"
	}
	return "https://library.example.com:8083"
}

// onKeyboardInput routes based on current wizard step.
func (a *app) onKeyboardInput(text string) {
	switch a.wizard.step {
	case stepProfileName:
		name := sanitizeProfileName(text)
		if name == "" {
			ink.OpenKeyboard("new-profile-name", 40)
			return
		}
		a.wizard.name = name
		a.wizard.step = stepBackend
		ink.Repaint()
	case stepURL:
		a.wizard.url = text
		a.wizard.step = stepUser
		ink.OpenKeyboard("Username", 128)
	case stepUser:
		a.wizard.user = text
		a.wizard.step = stepPass
		ink.OpenKeyboard("Password", 128)
	case stepPass:
		a.wizard.pass = text
		a.wizard.step = stepTesting
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
func (a *app) runProbe() {
	backend := a.wizard.backend
	if backend == "" {
		backend = BackendOPDS
	}
	var err error
	switch backend {
	case BackendWebDAV:
		err = ProbeWebDAV(context.Background(), a.wizard.url, a.wizard.user, a.wizard.pass, "/")
	default:
		err = ProbeCWA(context.Background(), a.wizard.url, a.wizard.user, a.wizard.pass)
	}
	if err != nil {
		a.wizard.err = err
		a.wizard.step = stepError
		ink.Repaint()
		return
	}
	libraryDir := "CWA"
	if backend == BackendWebDAV {
		libraryDir = "WebDAV"
	}
	profile := a.wizard.name
	if profile == "" {
		profile = defaultProfileName
	}
	cfg := &Config{
		Profile:      profile,
		Backend:      backend,
		Host:         a.wizard.url,
		User:         a.wizard.user,
		Pass:         a.wizard.pass,
		Library:      filepath.Join(ink.FlashDir, "Books", libraryDir),
		StateDB:      filepath.Join(ink.ConfigPath, "pocketbeam.db"),
		CheckUpdates: true,
	}
	if backend == BackendWebDAV {
		cfg.Path = "/"
	}
	if err := SaveConfig(a.cfgPath, cfg); err != nil {
		a.wizard.err = err
		a.wizard.step = stepError
		ink.Repaint()
		return
	}
	// When the wizard is being used to add a profile, the new section is
	// written but the file's `active` marker still points at the existing
	// one. Flip it so the newly-created profile becomes the working one.
	if a.wizard.addProfile {
		if err := SetActiveProfile(a.cfgPath, profile); err != nil {
			a.wizard.err = err
			a.wizard.step = stepError
			ink.Repaint()
			return
		}
	}
	client, err := NewClient(cfg.Host, cfg.User, cfg.Pass)
	if err != nil {
		a.wizard.err = err
		a.wizard.step = stepError
		ink.Repaint()
		return
	}
	store, err := OpenStore(cfg.StateDB)
	if err != nil {
		a.wizard.err = err
		a.wizard.step = stepError
		ink.Repaint()
		return
	}
	// Close any existing store handle before swapping (happens on settings → edit).
	if a.store != nil {
		_ = a.store.Close()
	}
	a.cfg = cfg
	a.client = client
	a.store = store
	a.refreshMainStats()
	a.screen = screenMain
	a.wizard = wizardState{}
	ink.Repaint()
}
