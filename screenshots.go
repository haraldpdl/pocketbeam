// The screenshot generator: renders each screen that draws through a
// Canvas into a PNG, so the README images come from the real drawing
// code instead of a photograph of a device. `make screenshots` writes
// them; screenshots_test.go fails when a committed image no longer
// matches what the code draws.

//go:build !arm

package main

import (
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"time"
)

// screenshotScreen is the panel the images are rendered at: the
// PocketBook Era Color (1264x1680), the device pocketbeam is developed
// against and the resolution tier the layout treats as its reference.
var screenshotScreen = image.Point{X: 1264, Y: 1680}

// screenshotDir is where the committed images live, relative to the
// repository root.
const screenshotDir = "docs/screenshots"

// screenshotHost is the server address every fixture is configured
// against, so the header reads the same on all of them.
const screenshotHost = "https://library.example.com:8083"

// screenshot is one rendered image: the file name (without extension)
// and the screen to paint.
type screenshot struct {
	name string
	draw func(c Canvas, l layout)
}

// screenshots lists every screen that can be rendered off the device.
// Screens whose drawing still lives in the InkView-only files are
// missing here and get added as they move.
func screenshots() []screenshot {
	return []screenshot{
		{"main-first-run", func(c Canvas, l layout) { drawMain(c, l, mainFirstRunView()) }},
		{"main", func(c Canvas, l layout) { drawMain(c, l, mainIdleView()) }},
		{"main-syncing", func(c Canvas, l layout) { drawMain(c, l, mainSyncingView()) }},
		{"first-run", func(c Canvas, l layout) { drawWizard(c, l, wizardURLView()) }},
		{"settings", func(c Canvas, l layout) { drawSettings(c, l, settingsFixtureView()) }},
	}
}

// mainIdleView is the main screen between syncs, with an update waiting.
// The fixtures are fixed values rather than recorded device state: an
// image has to come out the same on every machine, and it should show
// the screen at its most informative.
func mainIdleView() mainView {
	return mainView{
		subtitle: mainSubtitle(&Config{
			Host:        screenshotHost,
			FilterHrefs: []string{"/opds/shelf/2"},
			FilterNames: []string{"Science Fiction"},
		}),
		stats: mainStats{
			lastSync:    SyncSummary{Downloaded: 4},
			hasLastSync: true,
			bookCount:   214,
		},
		lastSyncAgo: "3 hours ago",
		updateVer:   "v0.6.0",
	}
}

// mainFirstRunView is the sync screen as the install guide's reader
// first meets it: the wizard has just written the config, nothing has
// been synced yet, and the release they sideloaded is the current one,
// so no update is waiting.
func mainFirstRunView() mainView {
	v := mainIdleView()
	// The wizard never asks for a filter, so a fresh profile syncs the
	// whole catalog and the header says All books.
	v.subtitle = mainSubtitle(&Config{Host: screenshotHost})
	v.stats = mainStats{}
	v.lastSyncAgo = ""
	v.updateVer = ""
	return v
}

// mainSyncingView is the same screen mid-sync: the primary button reads
// Stop and the progress strip is live. The update badge is left out
// because the two never compete for the user's attention in practice.
func mainSyncingView() mainView {
	v := mainIdleView()
	v.updateVer = ""
	v.sync = syncSnapshot{
		active:  true,
		index:   7,
		total:   24,
		title:   "The Dispossessed",
		author:  "Ursula K. Le Guin",
		elapsed: 42 * time.Second,
	}
	return v
}

// wizardURLView is the first-run wizard at the server-URL step with an
// address already entered, the step that shows what setting pocketbeam
// up asks for.
func wizardURLView() wizardState {
	return wizardState{step: stepURL, url: screenshotHost}
}

// settingsFixtureView is the settings list of a configured OPDS profile,
// with an update waiting so the About row shows the install it offers.
func settingsFixtureView() settingsView {
	v := settingsViewOf(&Config{
		Host:          screenshotHost,
		Profile:       "default",
		FilterHrefs:   []string{"/opds/shelf/2"},
		FilterNames:   []string{"Science Fiction"},
		DeleteMissing: true,
	}, "v0.6.0")
	// A rendered image has to come out the same on every machine, and
	// version is whatever the renderer was built with.
	v.currentVer = "v0.5.0"
	return v
}

// renderScreenshot paints s into a fresh image.
func renderScreenshot(s screenshot) *image.Gray {
	c := newImageCanvas(screenshotScreen)
	s.draw(c, computeLayout(screenshotScreen))
	return c.img
}

// writeScreenshots renders every screenshot into dir, creating it if
// needed.
func writeScreenshots(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, s := range screenshots() {
		path := filepath.Join(dir, s.name+".png")
		f, err := os.Create(path)
		if err != nil {
			return err
		}
		if err := png.Encode(f, renderScreenshot(s)); err != nil {
			f.Close()
			return fmt.Errorf("encode %s: %w", path, err)
		}
		if err := f.Close(); err != nil {
			return err
		}
		fmt.Println("wrote", path)
	}
	return nil
}
