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
	// title and a muted value subtitle. The delete-missing pill sits
	// inside deleteRow and is derived from it via togglePill; the whole
	// row is the tap target.
	serverRow     image.Rectangle
	profileRow    image.Rectangle
	filterRow     image.Rectangle
	deleteRow     image.Rectangle
	updateRow     image.Rectangle
	serverLabelY  int
	libraryLabelY int
	aboutLabelY   int
	backButton    image.Rectangle

	// first-run wizard: the two backend-choice rows. Fixed geometry like
	// the settings rows, so the pointer handler reads them from here
	// rather than the draw having to publish where it put them.
	wizardOPDSRow   image.Rectangle
	wizardWebDAVRow image.Rectangle

	// confirmation dialogs: the two footer buttons the delete prompt and
	// the space warning end in. Named by side, not by answer, because
	// which side carries the primary action differs per dialog (the
	// delete prompt keeps the safe answer on the left).
	confirmLeftButton  image.Rectangle
	confirmRightButton image.Rectangle

	// library-refresh dialog: the strip holding the animated "Indexing"
	// line, repainted on its own by the spinner while the rest of the
	// dialog stays put.
	libRefreshLine image.Rectangle

	// pickers (OPDS feed, WebDAV directory): the band the paginated row
	// list is drawn in, the fixed up-row above it, and the action
	// buttons below it. Fixed geometry like the settings rows, so the
	// pointer handler reads where the draw put things instead of the
	// draw writing rects back into picker state.
	pickerAreaTop    int
	pickerAreaBottom int
	pickerUpRow      image.Rectangle
	// pickerSelectRow is the single full-height action button; when the
	// feed picker has a selection going it splits into pickerAddRow
	// (add/remove the current level) above pickerDoneRow (save the set).
	pickerSelectRow image.Rectangle
	pickerAddRow    image.Rectangle
	pickerDoneRow   image.Rectangle
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

	// A section label is positioned by the top of its glyph box, like
	// every string, and the band it sits in ends where the section's
	// first row draws its hairline. Centring the box in the band keeps
	// the letters clear of that line instead of sitting on it.
	labelY := func(bandTop int) int { return bandTop + (sectionLabelH-sc(sectionLabelPx))/2 }

	sy := settingsTop
	serverLabelY := labelY(sy)
	sy += sectionLabelH
	serverRow := image.Rect(sideMargin, sy, sideMargin+contentW, sy+settingsRowH)
	sy += settingsRowH
	profileRow := image.Rect(sideMargin, sy, sideMargin+contentW, sy+settingsRowH)
	sy += settingsRowH
	libraryLabelY := labelY(sy)
	sy += sectionLabelH
	filterRow := image.Rect(sideMargin, sy, sideMargin+contentW, sy+settingsRowH)
	sy += settingsRowH
	deleteRow := image.Rect(sideMargin, sy, sideMargin+contentW, sy+settingsRowH)
	sy += settingsRowH
	aboutLabelY := labelY(sy)
	sy += sectionLabelH
	updateRow := image.Rect(sideMargin, sy, sideMargin+contentW, sy+settingsRowH)

	// First-run wizard, backend step: two stacked full-width rows below
	// the header, sharing the row idiom the settings list uses.
	wizardOPDS := image.Rect(sideMargin, sc(360), sideMargin+contentW, sc(500))
	wizardWebDAV := image.Rect(sideMargin, sc(500), sideMargin+contentW, sc(640))

	// Confirmation dialogs: two buttons filling the bottom row's band,
	// split down the middle with the same gap the main screen's buttons
	// leave between them.
	confirmHalf := (contentW - sc(40)) / 2
	confirmLeft := image.Rect(sideMargin, btnY1, sideMargin+confirmHalf, btnY2)
	confirmRight := image.Rect(w-sideMargin-confirmHalf, btnY1, w-sideMargin, btnY2)

	// Library-refresh dialog: the animated line sits under the title,
	// and its strip is a full text line tall so repainting it clears the
	// dots the previous tick left behind.
	libRefreshLine := image.Rect(sideMargin, sc(400), w-sideMargin, sc(444))

	// Pickers: the action button sits just above the Back button, the
	// row list fills what is left between the header (below topSafe) and
	// it. The two stacked buttons share that band with a small gap, so
	// the bottom one keeps the primary position either way.
	pickerTop := topSafe + sc(220)
	selectBtnH := sc(100)
	selectY2 := btnY1 - sc(40)
	selectY1 := selectY2 - selectBtnH
	pickerSelect := image.Rect(sideMargin, selectY1, sideMargin+contentW, selectY2)
	splitH := (selectBtnH - sc(20)) / 2
	pickerAdd := image.Rect(sideMargin, selectY1, sideMargin+contentW, selectY1+splitH)
	pickerDone := image.Rect(sideMargin, selectY2-splitH, sideMargin+contentW, selectY2)
	pickerBottom := selectY1 - sc(40)
	// The up-row is one list row minus a small gap, so the paginated rows
	// below it start on the row grid.
	pickerUp := image.Rect(sideMargin, pickerTop, sideMargin+contentW, pickerTop+sc(listRowPx)-sc(20))

	return layout{
		screen:             sz,
		margin:             sideMargin,
		scale:              scale,
		syncButton:         syncBtn,
		networkButton:      networkBtn,
		settingsButton:     settingsBtn,
		quitButton:         quitBtn,
		progressArea:       progArea,
		progressBar:        progBar,
		serverRow:          serverRow,
		profileRow:         profileRow,
		filterRow:          filterRow,
		deleteRow:          deleteRow,
		updateRow:          updateRow,
		wizardOPDSRow:      wizardOPDS,
		wizardWebDAVRow:    wizardWebDAV,
		confirmLeftButton:  confirmLeft,
		confirmRightButton: confirmRight,
		libRefreshLine:     libRefreshLine,
		serverLabelY:       serverLabelY,
		libraryLabelY:      libraryLabelY,
		aboutLabelY:        aboutLabelY,
		backButton:         networkBtn,
		pickerAreaTop:      pickerTop,
		pickerAreaBottom:   pickerBottom,
		pickerUpRow:        pickerUp,
		pickerSelectRow:    pickerSelect,
		pickerAddRow:       pickerAdd,
		pickerDoneRow:      pickerDone,
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

// togglePill returns the toggle rect for a boolean list row: a pill
// sized to read clearly as a binary control, right-aligned inside the
// row. Derived from the row rather than stored so every toggle row
// (Settings' delete-missing, the update screen's automatic checks) gets
// the same geometry.
func (s layout) togglePill(row image.Rectangle) image.Rectangle {
	h := s.sy(60)
	w := s.sx(120)
	cy := (row.Min.Y + row.Max.Y) / 2
	x2 := row.Max.X - s.sx(20)
	return image.Rect(x2-w, cy-h/2, x2, cy+h/2)
}

// listRowPx is the height of one full-width list row on the reference
// device. computeLayout and rowH both scale it, so a picker's fixed
// up-row lines up with the paginated rows below it.
const listRowPx = 110

// rowH is the height of one full-width list row. Every stacked-row
// screen goes through this.
func (s layout) rowH() int {
	return s.sy(listRowPx)
}

// sx scales an X coordinate the same way. Used for sub-indents (e.g.,
// bullet-list content) where the offset must scale with the page margin.
func (s layout) sx(base int) int {
	return int(float64(base)*s.scale + 0.5)
}
