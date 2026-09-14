// The one shared drawing helper that is still device-only: it repaints a
// row on the panel rather than just painting it, so it needs the app's
// draw serialization and the panel refresh.

package main

import (
	"image"
)

// refreshToggleRow repaints one toggle row in place and pushes only that
// strip to the panel. Flipping a toggle changes nothing else on the
// screen, so routing it through Draw would clear and re-flash the whole
// panel for a pill that moved a few millimetres.
//
// It runs under DrawFull: only the pointer handlers call it, so the
// screen is current by construction and DrawIfOn's test would be
// redundant, but a background goroutine can be mid-draw in its own
// strip, and the two passes share the canvas's single active face and
// colour. Unserialized, the row and the goroutine's strip render in each
// other's face - and unlike the strip, which the next tick repaints, the
// row stays wrong until the user leaves and re-enters the screen.
func (a *app) refreshToggleRow(r image.Rectangle, title, subtitle string, on bool) {
	c := deviceCanvas
	a.DrawFull(func() {
		c.Fill(r, white)
		a.layout.drawToggleRow(c, a.layout.font(c, 36, true), a.layout.font(c, 28, false), r, title, subtitle, on)
		c.PartialUpdate(r)
	})
}
