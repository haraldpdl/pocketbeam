// The off-device Canvas: draws into an 8-bit grayscale image with the Go
// fonts, so screens can be rendered to PNG for the README and asserted on
// in tests without a PocketBook.
//
// It is excluded from the device build (the tag below) because nothing on
// the device renders into an image, and the embedded typefaces and the
// rasteriser would be dead weight in a binary users download over Wi-Fi.

//go:build !arm

package main

import (
	"image"
	"image/color"
	"image/draw"
	"sync"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
)

// imageCanvas renders into img. It mirrors InkView's single-active-face
// model: SetFont selects the face and colour that Text and TextWidth
// then use.
//
// Glyph shapes and metrics are the Go fonts', not the device's, so a
// rendered screen is a faithful layout of the real draw calls rather
// than a pixel-exact photograph of the panel.
type imageCanvas struct {
	img    *image.Gray
	faces  faceCache
	active imageFace
	src    image.Image
}

func newImageCanvas(size image.Point) *imageCanvas {
	c := &imageCanvas{
		img: image.NewGray(image.Rectangle{Max: size}),
		src: image.NewUniform(black),
	}
	c.faces.open = func(px int, bold bool) (Face, bool) { return newImageFace(px, bold), true }
	c.Clear()
	return c
}

// imageFace pairs a rasterisable face with the pixel size it was opened
// at, so Height answers the same question inkFace.Height does.
type imageFace struct {
	face font.Face
	px   int
}

func (f imageFace) Height() int { return f.px }

// goFonts parses the two embedded typefaces once. They are compiled into
// the binary, so a parse failure is a corrupt build rather than a
// runtime condition worth handling.
var goFonts = sync.OnceValue(func() [2]*sfnt.Font {
	regular, err := opentype.Parse(goregular.TTF)
	if err != nil {
		panic("parse goregular: " + err.Error())
	}
	bold, err := opentype.Parse(gobold.TTF)
	if err != nil {
		panic("parse gobold: " + err.Error())
	}
	return [2]*sfnt.Font{regular, bold}
})

func newImageFace(px int, bold bool) imageFace {
	f := goFonts()[0]
	if bold {
		f = goFonts()[1]
	}
	// px is the height of the whole glyph box, which is how the screens
	// space their lines, so the em size is scaled down by the typeface's
	// own ascent+descent ratio rather than used as-is. Without this the
	// Go fonts render a third taller than the layout reserves and lines
	// collide where the device's do not.
	face := openGoFace(f, float64(px))
	m := face.Metrics()
	if h := (m.Ascent + m.Descent).Ceil(); h > px && h > 0 {
		face = openGoFace(f, float64(px)*float64(px)/float64(h))
	}
	return imageFace{face: face, px: px}
}

// openGoFace rasterises f at em pixels. DPI 72 makes Size the em height
// in pixels.
func openGoFace(f *sfnt.Font, em float64) font.Face {
	face, err := opentype.NewFace(f, &opentype.FaceOptions{
		Size:    em,
		DPI:     72,
		Hinting: font.HintingFull,
	})
	if err != nil {
		panic("open Go font face: " + err.Error())
	}
	return face
}

func (c *imageCanvas) Size() image.Point { return c.img.Bounds().Size() }

func (c *imageCanvas) Clear() { c.Fill(c.img.Bounds(), white) }

func (c *imageCanvas) Fill(r image.Rectangle, col color.Color) {
	draw.Draw(c.img, r.Intersect(c.img.Bounds()), image.NewUniform(col), image.Point{}, draw.Src)
}

// Rect draws the one-pixel outline InkView's DrawRect draws.
func (c *imageCanvas) Rect(r image.Rectangle, col color.Color) {
	if r.Empty() {
		return
	}
	c.Fill(image.Rect(r.Min.X, r.Min.Y, r.Max.X, r.Min.Y+1), col)
	c.Fill(image.Rect(r.Min.X, r.Max.Y-1, r.Max.X, r.Max.Y), col)
	c.Fill(image.Rect(r.Min.X, r.Min.Y, r.Min.X+1, r.Max.Y), col)
	c.Fill(image.Rect(r.Max.X-1, r.Min.Y, r.Max.X, r.Max.Y), col)
}

func (c *imageCanvas) Font(px int, bold bool) Face { return c.faces.face(px, bold) }

func (c *imageCanvas) SetFont(f Face, col color.Color) {
	c.active = f.(imageFace)
	c.src = image.NewUniform(col)
}

// Text draws s with p as the top-left corner of the glyph box, the
// position InkView's DrawString takes. The drawer wants a baseline, so
// the face's ascent is added.
func (c *imageCanvas) Text(p image.Point, s string) {
	if c.active.face == nil {
		return
	}
	d := font.Drawer{
		Dst:  c.img,
		Src:  c.src,
		Face: c.active.face,
		Dot: fixed.Point26_6{
			X: fixed.I(p.X),
			Y: fixed.I(p.Y) + c.active.face.Metrics().Ascent,
		},
	}
	d.DrawString(s)
}

func (c *imageCanvas) TextWidth(s string) int {
	if c.active.face == nil {
		return 0
	}
	return font.MeasureString(c.active.face, s).Ceil()
}

// The panel-refresh and busy-icon calls have no counterpart off-device:
// the image is complete as soon as the draw function returns, and there
// is no user waiting at it.
func (c *imageCanvas) FullUpdate()                     {}
func (c *imageCanvas) PartialUpdate(_ image.Rectangle) {}
func (c *imageCanvas) ShowHourglass()                  {}
func (c *imageCanvas) ShowHourglassAt(_ image.Point)   {}
func (c *imageCanvas) HideHourglass()                  {}
