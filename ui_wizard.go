// Drawing for the first-run wizard. It paints from a snapshot of the
// wizard state, so every step renders the same on the device and into an
// image on a build machine; the keyboard, the connection probe and the
// step transitions live in ui_wizard_arm.go.

package main

import (
	"image"
)

// drawWizard paints the step wiz is on. wizardState is the view: it is
// already a plain snapshot of what the user has entered, so a second
// struct would only copy it field by field.
//
// It reports where the panel's busy icon belongs while the connection is
// being probed, because raising it is the panel's job and has to be
// tracked by the caller that takes it down again.
func drawWizard(c Canvas, l layout, wiz wizardState) (busyAt image.Point, busy bool) {
	title := l.font(c, 54, true)
	body := l.font(c, 32, false)

	switch wiz.step {
	case stepWelcome:
		c.SetFont(title, black)
		c.Text(image.Point{X: l.margin, Y: l.sy(200)}, "pocketbeam")
		c.SetFont(body, darkGray)
		c.Text(image.Point{X: l.margin, Y: l.sy(260)}, "Wireless sync from a book server.")
		l.drawHairline(c, l.margin, l.screen.X-l.margin, l.sy(300))

		c.SetFont(body, black)
		c.Text(image.Point{X: l.margin, Y: l.sy(370)}, "Supported servers")
		c.SetFont(body, darkGray)
		c.Text(image.Point{X: l.margin, Y: l.sy(430)}, "· Calibre-Web and any OPDS catalog")
		c.Text(image.Point{X: l.margin, Y: l.sy(480)}, "· Nextcloud, Synology, ownCloud, WebDAV")

		c.SetFont(body, black)
		c.Text(image.Point{X: l.margin, Y: l.sy(620)}, "Press OK or tap to begin.")

	case stepProfileName:
		c.SetFont(title, black)
		c.Text(image.Point{X: l.margin, Y: l.sy(200)}, "Name this profile")
		c.SetFont(body, darkGray)
		c.Text(image.Point{X: l.margin, Y: l.sy(260)}, "Short identifier for this server (no spaces).")
		l.drawHairline(c, l.margin, l.screen.X-l.margin, l.sy(300))
		c.SetFont(body, black)
		c.Text(image.Point{X: l.margin, Y: l.sy(370)}, "Tap or press OK to re-open the keyboard.")
		if wiz.err != nil {
			c.Text(image.Point{X: l.margin, Y: l.sy(430)}, truncate(wiz.err.Error(), 60))
		}
		if wiz.name != "" {
			c.SetFont(body, darkGray)
			c.Text(image.Point{X: l.margin, Y: l.sy(480)}, "Name: "+wiz.name)
		}

	case stepBackend:
		c.SetFont(title, black)
		c.Text(image.Point{X: l.margin, Y: l.sy(200)}, "Choose server type")
		c.SetFont(body, darkGray)
		c.Text(image.Point{X: l.margin, Y: l.sy(260)}, "Tap the option that matches your server.")

		rowTitleFont := l.font(c, 36, true)
		rowSubFont := l.font(c, 28, false)
		l.drawListRow(c, rowTitleFont, rowSubFont, l.wizardOPDSRow,
			"Calibre-Web / OPDS", "Calibre-Web, COPS, and other OPDS catalogs", true)
		l.drawListRow(c, rowTitleFont, rowSubFont, l.wizardWebDAVRow,
			"WebDAV / Nextcloud", "Nextcloud, Synology, ownCloud, generic WebDAV", true)
		l.drawHairline(c, l.wizardWebDAVRow.Min.X, l.wizardWebDAVRow.Max.X, l.wizardWebDAVRow.Max.Y)

	case stepURL, stepUser, stepPass:
		c.SetFont(title, black)
		c.Text(image.Point{X: l.margin, Y: l.sy(200)}, "pocketbeam setup")
		c.SetFont(body, darkGray)
		var step, what string
		switch wiz.step {
		case stepURL:
			step, what = "Step 1 of 3", "Server URL"
		case stepUser:
			step, what = "Step 2 of 3", "Username"
		case stepPass:
			step, what = "Step 3 of 3", "Password"
		}
		c.Text(image.Point{X: l.margin, Y: l.sy(260)}, step+"  ·  "+what)
		l.drawHairline(c, l.margin, l.screen.X-l.margin, l.sy(300))
		c.SetFont(body, black)
		c.Text(image.Point{X: l.margin, Y: l.sy(370)}, "Tap or press OK to open the keyboard.")
		c.SetFont(body, darkGray)
		y := l.sy(490)
		if wiz.url != "" {
			c.Text(image.Point{X: l.margin, Y: y}, "Server: "+truncate(wiz.url, 48))
			y += l.sy(50)
		}
		if wiz.user != "" {
			c.Text(image.Point{X: l.margin, Y: y}, "User: "+truncate(wiz.user, 48))
		}

	case stepTesting:
		c.SetFont(title, black)
		c.Text(image.Point{X: l.margin, Y: l.sy(300)}, "Testing connection")
		c.SetFont(body, darkGray)
		c.Text(image.Point{X: l.margin, Y: l.sy(370)}, truncate(wiz.url, 55))
		return image.Point{X: l.margin, Y: l.sy(480)}, true

	case stepError:
		c.SetFont(title, black)
		c.Text(image.Point{X: l.margin, Y: l.sy(200)}, "Connection failed")
		c.SetFont(body, darkGray)
		c.Text(image.Point{X: l.margin, Y: l.sy(260)}, "Check that the server URL and credentials are correct.")
		l.drawHairline(c, l.margin, l.screen.X-l.margin, l.sy(300))
		msg := "Unknown error"
		if wiz.err != nil {
			msg = wiz.err.Error()
		}
		c.SetFont(body, black)
		c.Text(image.Point{X: l.margin, Y: l.sy(370)}, truncate(msg, 60))
		c.SetFont(body, darkGray)
		c.Text(image.Point{X: l.margin, Y: l.sy(620)}, "Press OK or tap to try again.")
	}
	return image.Point{}, false
}
