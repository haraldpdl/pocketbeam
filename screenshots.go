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
		{"feed-picker", func(c Canvas, l layout) { drawPicker(c, l, feedPickerFixtureView()) }},
		{"dir-picker", func(c Canvas, l layout) { drawPicker(c, l, dirPickerFixtureView()) }},
		{"delete-confirm", func(c Canvas, l layout) { drawDeleteConfirm(c, l, deleteConfirmFixtureView()) }},
		{"space-warning", func(c Canvas, l layout) { drawSpaceWarn(c, l, spaceWarnFixtureView()) }},
		{"library-refresh", func(c Canvas, l layout) { drawLibraryRefresh(c, l, libraryRefreshView{dots: 2}) }},
		{"profiles", func(c Canvas, l layout) { drawProfileList(c, l, profileListFixtureView()) }},
		{"profile-detail", func(c Canvas, l layout) { drawProfileDetail(c, l, profileDetailFixtureView()) }},
		{"update", func(c Canvas, l layout) { drawUpdate(c, l, updateFixtureView()) }},
		{"update-downloading", func(c Canvas, l layout) { drawUpdate(c, l, updateDownloadingFixtureView()) }},
	}
}

// screenshotRelease is the waiting release every update fixture offers,
// matching the version the main and settings fixtures announce.
var screenshotRelease = Release{
	Version:    "v0.6.0",
	BinarySize: 7_300_000,
	SHA256:     "9f2c4a1b7d3e5086c41f9ab2d7e6035481cbb9de7a2f4c10d8e63b5a90271fe4",
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

// feedPickerFixtureView is the OPDS feed picker part-way down a catalog:
// a breadcrumb of where the user is, more subsections than fit on one
// page so the page indicator shows, and one level already picked so the
// Add / Done pair is on screen instead of the single-button state.
func feedPickerFixtureView() pickerView {
	subs := []FilterOption{
		{Name: "Fantasy", Href: "/opds/category/1", Count: 128, CountKnown: true},
		{Name: "Science Fiction", Href: "/opds/category/2", Count: 214, CountKnown: true},
		{Name: "Crime", Href: "/opds/category/3", Count: 61, CountKnown: true},
		{Name: "History", Href: "/opds/category/4", Count: 47, CountKnown: true},
		{Name: "Biography", Href: "/opds/category/5", Count: 33, CountKnown: true},
		{Name: "Travel", Href: "/opds/category/6", Count: 18, CountKnown: true},
		{Name: "Cookery", Href: "/opds/category/7", Count: 12, CountKnown: true},
		{Name: "Poetry", Href: "/opds/category/8", Count: 9, CountKnown: true},
		{Name: "Reference", Href: "/opds/category/9", Count: 5, CountKnown: true},
	}
	return feedPickerViewOf(feedPickerSnapshot{
		titles:   []string{"Calibre-Web", "Categories"},
		href:     "/opds/category",
		level:    OPDSLevel{FeedTitle: "Categories", Subsections: subs},
		selected: []FilterOption{{Name: "Science Fiction", Href: "/opds/category/2"}},
	})
}

// dirPickerFixtureView is the WebDAV directory picker inside a share,
// listing the folders one level down.
func dirPickerFixtureView() pickerView {
	return dirPickerViewOf(dirPickerSnapshot{
		path: "/Books/Fiction",
		dirs: []string{"Anthologies", "Novellas", "Series", "Standalone"},
	})
}

// profileListFixtureView is the profile list of a device syncing from
// three servers, with the active one marked.
func profileListFixtureView() profileListView {
	return profileListView{
		names:  []string{"default", "nextcloud", "friends-opds"},
		active: "default",
	}
}

// profileDetailFixtureView is the panel of a profile that is not the
// active one, so it shows both actions it can offer.
func profileDetailFixtureView() profileDetailView {
	return profileDetailView{
		name:    "nextcloud",
		backend: BackendWebDAV,
		host:    "https://nc.example.com/remote.php/dav/files/alice",
	}
}

// updateFixtureView is the update screen with a release waiting: the
// state the About row in Settings opens into.
func updateFixtureView() updateView {
	return updateViewOf(updateSnapshot{available: true, rel: screenshotRelease}, true, "v0.5.0")
}

// updateDownloadingFixtureView is the same screen while the release is
// coming down. The counters are fixed values, so the image does not
// depend on a clock or on a real transfer.
func updateDownloadingFixtureView() updateView {
	v := updateFixtureView()
	v.downloading = true
	v.downloaded = 2_920_000
	v.total = screenshotRelease.BinarySize
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

// deleteConfirmFixtureView is the delete-missing prompt with more books
// waiting than it lists, the state that shows both what the prompt names
// and how it counts the rest.
func deleteConfirmFixtureView() deleteConfirmView {
	return deleteConfirmViewOf([]LocalBook{
		{Author: "Ursula K. Le Guin", Title: "The Dispossessed"},
		{Author: "Octavia E. Butler", Title: "Parable of the Sower"},
		{Author: "Stanisław Lem", Title: "Solaris"},
		{Author: "Ann Leckie", Title: "Ancillary Justice"},
		{Author: "Arkady Martine", Title: "A Memory Called Empire"},
		{Author: "Becky Chambers", Title: "The Long Way to a Small, Angry Planet"},
		{Author: "Adrian Tchaikovsky", Title: "Children of Time"},
		{Author: "N. K. Jemisin", Title: "The Fifth Season"},
	})
}

// spaceWarnFixtureView is the pre-flight warning for a first sync of a
// large library onto a device that is nearly full, with delete-missing
// able to win some of the shortfall back.
func spaceWarnFixtureView() spaceWarnView {
	return spaceWarnViewOf(SyncPlan{
		NewBooks:         make([]Book, 142),
		UpdatedBooks:     make([]Book, 3),
		DownloadBytes:    1_840_000_000,
		FreeBytes:        612_000_000,
		ReclaimableBytes: 240_000_000,
		UnknownSizes:     4,
	})
}
