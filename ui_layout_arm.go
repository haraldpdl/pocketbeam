// Screen-relative geometry: the layout struct computed once from the
// panel size, plus the scaling helpers every draw function goes through.

package main

import (
	"image"
)

// layout holds screen-relative rectangles for every clickable element and for
// the progress strip. Computed once in Init from the actual ScreenSize so the
// UI adapts to whatever resolution the device reports.
type layout struct {
	screen image.Point
	margin int
	// scale multiplies all font heights so the UI stays readable on
	// smaller PocketBook panels (Touch HD, Touch Lux 5) without
	// blowing up unnecessarily on the HD 6" devices we originally
	// designed against (Era / Era Color at 1264x1680).
	scale float64

	// main screen
	syncButton     image.Rectangle
	networkButton  image.Rectangle
	settingsButton image.Rectangle
	quitButton     image.Rectangle
	progressArea   image.Rectangle // the strip refreshed via PartialUpdate
	progressBar    image.Rectangle

	// settings screen: a stack of tappable rows grouped into three
	// sections. Row rects are full-width tap targets; each has a bold
	// title and a muted value subtitle. deleteToggle is a smaller pill
	// drawn inside deleteRow, left in place for the pointer handler
	// only because the whole row is tappable anyway.
	serverRow     image.Rectangle
	profileRow    image.Rectangle
	filterRow     image.Rectangle
	deleteRow     image.Rectangle
	deleteToggle  image.Rectangle
	updateRow     image.Rectangle
	serverLabelY  int
	libraryLabelY int
	aboutLabelY   int
	backButton    image.Rectangle

	// shelf picker: per-row tap rects computed dynamically in draw.
	pickerAreaTop    int
	pickerAreaBottom int
}

// computeLayout lays out the UI relative to the given screen size. Positions
// are derived from the usable area after stripping generous safety margins
// that guard against a PocketBook status bar at top and nav bar at bottom
// (ScreenSize does not account for either).
func computeLayout(sz image.Point) layout {
	w, h := sz.X, sz.Y
	scale := scaleFactor(sz)
	// sc scales a reference-device (1264x1680) pixel length to the current
	// screen. Keeps every spacing in this function proportionate when the
	// layout is computed for a smaller PocketBook panel.
	sc := func(px int) int { return int(float64(px)*scale + 0.5) }
	sideMargin := sc(60)
	topSafe := sc(100)
	bottomSafe := sc(180)
	if w > 0 && w < 1200 {
		sideMargin = sc(40)
	}
	contentW := w - 2*sideMargin

	// Main Sync Now button: centered, below the last-sync summary area.
	syncY1 := topSafe + sc(420)
	syncBtn := image.Rect(sideMargin, syncY1, sideMargin+contentW, syncY1+sc(200))

	// Progress strip: fixed position below the Sync button, above the bottom
	// row. Covers counter + bar + current-book line.
	progY1 := syncBtn.Max.Y + sc(60)
	progArea := image.Rect(sideMargin, progY1, sideMargin+contentW, progY1+sc(240))
	progBar := image.Rect(sideMargin, progY1+sc(80), sideMargin+contentW, progY1+sc(130))

	// Bottom button row: Network | Settings | Quit, anchored from the bottom.
	btnH := sc(100)
	btnGap := sc(40)
	btnY2 := h - bottomSafe
	btnY1 := btnY2 - btnH
	btnW := (contentW - 2*btnGap) / 3 // 3 buttons with two gaps between them
	networkBtn := image.Rect(sideMargin, btnY1, sideMargin+btnW, btnY2)
	settingsBtn := image.Rect(networkBtn.Max.X+btnGap, btnY1, networkBtn.Max.X+btnGap+btnW, btnY2)
	quitBtn := image.Rect(w-sideMargin-btnW, btnY1, w-sideMargin, btnY2)

	// Settings screen: list of full-width tappable rows grouped into
	// SERVER / LIBRARY / ABOUT sections. Each section gets a small
	// label; rows stretch the content width and are sized to divide the
	// remaining vertical space evenly so the layout fits any panel.
	settingsTop := topSafe + sc(220)
	settingsBottom := btnY1 - sc(40)
	sectionLabelH := sc(60)
	settingsRowCount := 5
	settingsSectionCount := 3
	rowsH := (settingsBottom - settingsTop) - settingsSectionCount*sectionLabelH
	settingsRowH := rowsH / settingsRowCount

	sy := settingsTop
	serverLabelY := sy + sc(44)
	sy += sectionLabelH
	serverRow := image.Rect(sideMargin, sy, sideMargin+contentW, sy+settingsRowH)
	sy += settingsRowH
	profileRow := image.Rect(sideMargin, sy, sideMargin+contentW, sy+settingsRowH)
	sy += settingsRowH
	libraryLabelY := sy + sc(44)
	sy += sectionLabelH
	filterRow := image.Rect(sideMargin, sy, sideMargin+contentW, sy+settingsRowH)
	sy += settingsRowH
	deleteRow := image.Rect(sideMargin, sy, sideMargin+contentW, sy+settingsRowH)
	sy += settingsRowH
	aboutLabelY := sy + sc(44)
	sy += sectionLabelH
	updateRow := image.Rect(sideMargin, sy, sideMargin+contentW, sy+settingsRowH)

	// Delete-missing toggle pill: right-aligned inside deleteRow, sized
	// to read clearly as a binary control.
	toggleH := sc(60)
	toggleW := sc(120)
	toggleCY := (deleteRow.Min.Y + deleteRow.Max.Y) / 2
	toggleX2 := deleteRow.Max.X - sc(20)
	deleteToggle := image.Rect(toggleX2-toggleW, toggleCY-toggleH/2, toggleX2, toggleCY+toggleH/2)

	// Shelf picker: rows live between the header (below topSafe) and the
	// Back button (same position as bottom btnY1).
	pickerTop := topSafe + sc(220)
	pickerBottom := btnY1 - sc(40)

	return layout{
		screen:           sz,
		margin:           sideMargin,
		scale:            scale,
		syncButton:       syncBtn,
		networkButton:    networkBtn,
		settingsButton:   settingsBtn,
		quitButton:       quitBtn,
		progressArea:     progArea,
		progressBar:      progBar,
		serverRow:        serverRow,
		profileRow:       profileRow,
		filterRow:        filterRow,
		deleteRow:        deleteRow,
		deleteToggle:     deleteToggle,
		updateRow:        updateRow,
		serverLabelY:     serverLabelY,
		libraryLabelY:    libraryLabelY,
		aboutLabelY:      aboutLabelY,
		backButton:       networkBtn,
		pickerAreaTop:    pickerTop,
		pickerAreaBottom: pickerBottom,
	}
}

// scaleFactor returns a multiplier for font sizes and generous spacings
// so the UI stays readable on smaller PocketBook panels without looking
// bloated on HD 6" screens. Three tiers match the three resolution
// families we see across the PocketBook line:
//   - >= 1200 wide: Era / Era Color / InkPad Color / InkPad X (scale 1.0)
//   - >= 1000 wide: Touch HD, HD 3 (~1072 wide, scale 0.85)
//   - smaller: Touch Lux 5 and legacy 6" (scale 0.7)
//
// We key off pixel width rather than DPI because the only value InkView
// hands us for free is the framebuffer size; DPI-per-model tables live
// in KOReader and would be heavy to maintain.
func scaleFactor(sz image.Point) float64 {
	w := sz.X
	if sz.Y < w {
		w = sz.Y
	}
	switch {
	case w >= 1200:
		return 1.0
	case w >= 1000:
		return 0.85
	default:
		return 0.7
	}
}

// fpx scales a base font size in px by s.scale, rounded to nearest pixel.
// Centralised so every drawString that picks a font size scales the
// same way.
func (s layout) fpx(base int) int {
	return int(float64(base)*s.scale + 0.5)
}

// sy scales a Y coordinate authored for the 1264x1680 reference device to
// the current screen. Every hard-coded Y in a draw function should flow
// through this so smaller PocketBook panels (Touch HD, Touch Lux 5) stay
// proportionate instead of pushing content off the bottom.
func (s layout) sy(base int) int {
	return int(float64(base)*s.scale + 0.5)
}

// sx scales an X coordinate the same way. Used for sub-indents (e.g.,
// bullet-list content) where the offset must scale with the page margin.
func (s layout) sx(base int) int {
	return int(float64(base)*s.scale + 0.5)
}
