//go:build !arm

package main

import (
	"fmt"
	"image"
	"testing"
)

func TestSettingsViewOf(t *testing.T) {
	cases := []struct {
		name        string
		cfg         *Config
		updateVer   string
		wantTitle   string
		wantValue   string
		wantProfile string
	}{
		{
			name:        "opds unfiltered",
			cfg:         &Config{Host: "https://cwa.example", Profile: "default"},
			wantTitle:   "Sync filter",
			wantValue:   "All books",
			wantProfile: "default",
		},
		{
			name:        "opds filtered",
			cfg:         &Config{Host: "https://cwa.example", Profile: "home", FilterHrefs: []string{"/s/1"}, FilterNames: []string{"Sci-Fi"}},
			wantTitle:   "Sync filter",
			wantValue:   "Sci-Fi",
			wantProfile: "home",
		},
		{
			name:        "webdav root",
			cfg:         &Config{Backend: BackendWebDAV, Host: "https://nc.example", Profile: "nc"},
			wantTitle:   "Sync folder",
			wantValue:   "/",
			wantProfile: "nc",
		},
		{
			name:        "webdav sub-folder",
			cfg:         &Config{Backend: BackendWebDAV, Host: "https://nc.example", Profile: "nc", Path: "/Books"},
			wantTitle:   "Sync folder",
			wantValue:   "/Books",
			wantProfile: "nc",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := settingsViewOf(tc.cfg, tc.updateVer)
			if v.filterTitle != tc.wantTitle || v.filterValue != tc.wantValue {
				t.Errorf("library row = %q / %q, want %q / %q", v.filterTitle, v.filterValue, tc.wantTitle, tc.wantValue)
			}
			if v.profile != tc.wantProfile || v.host != tc.cfg.Host {
				t.Errorf("server row = %q / %q, want %q / %q", v.host, v.profile, tc.cfg.Host, tc.wantProfile)
			}
		})
	}
}

func settingsTestView() settingsView {
	return settingsViewOf(&Config{
		Host:    "https://books.example.com:8083",
		Profile: "default",
	}, "")
}

func drawSettingsOn(v settingsView) *recordCanvas {
	c := newRecordCanvas()
	drawSettings(c, computeLayout(c.Size()), v)
	return c
}

func TestDrawSettingsShowsEveryRow(t *testing.T) {
	c := drawSettingsOn(settingsTestView())
	for _, want := range []string{
		"Settings", "SERVER", "Server", "https://books.example.com:8083", "Profile", "default",
		"LIBRARY", "Sync filter", "All books", deleteMissingTitle, deleteMissingSubtitle(false),
		"ABOUT", "Check for updates", "Current version " + version, "Back",
	} {
		if !c.drew(want) {
			t.Errorf("settings did not draw %q; drew %q", want, c.texts)
		}
	}
}

// The About row is the only way to install an update, so it has to name
// the waiting version instead of offering another check.
func TestDrawSettingsAboutRowOffersTheWaitingUpdate(t *testing.T) {
	v := settingsTestView()
	v.updateVer = "v9.9.9"
	c := drawSettingsOn(v)
	if !c.drew("Install update v9.9.9") || c.drew("Check for updates") {
		t.Errorf("About row with an update waiting drew %q", c.texts)
	}
}

func TestDrawSettingsDeleteMissingSubtitleFollowsTheSetting(t *testing.T) {
	v := settingsTestView()
	v.deleteMissing = true
	c := drawSettingsOn(v)
	if !c.drew(deleteMissingSubtitle(true)) || c.drew(deleteMissingSubtitle(false)) {
		t.Errorf("delete-missing row drew %q", c.texts)
	}
}

// The rows are stacked by the layout and their text is centred inside
// them, so a taller face or an extra section would push two lines into
// each other without any text assertion noticing.
func TestDrawSettingsLinesDoNotOverlap(t *testing.T) {
	long := settingsTestView()
	long.host = "https://a-very-long-server-name.example.com:8443/opds/catalog"
	long.filterValue = "Science Fiction, Fantasy, History, Biographies"
	long.deleteMissing = true
	long.updateVer = "v9.9.9"
	for name, v := range map[string]settingsView{
		"plain":       settingsTestView(),
		"long values": long,
		"webdav": settingsViewOf(&Config{
			Backend: BackendWebDAV,
			Host:    "https://nc.example/remote.php/dav/files/alice",
			Profile: "nextcloud",
			Path:    "/Books/Fiction",
		}, ""),
	} {
		t.Run(name, func(t *testing.T) {
			if a, b, ok := drawSettingsOn(v).overlappingText(); ok {
				t.Errorf("%q at %v overlaps %q at %v", a.s, a.r, b.s, b.r)
			}
		})
	}
}

// Section labels sit in the band above the section's first row, and a
// string occupies a glyph box as tall as the face it is drawn in, so a
// label hung too low has the row's hairline running through its letters.
// Text assertions cannot see that, and there is no device in CI, so the
// clearance is asserted on the layout directly - at every panel tier,
// because the band and the label scale separately.
func TestSettingsSectionLabelsClearTheirRows(t *testing.T) {
	for _, sz := range []image.Point{screenshotScreen, {X: 1072, Y: 1448}, {X: 758, Y: 1024}} {
		t.Run(fmt.Sprintf("%dx%d", sz.X, sz.Y), func(t *testing.T) {
			l := computeLayout(sz)
			h := l.fpx(sectionLabelPx)
			for _, tc := range []struct {
				label string
				y     int
				row   image.Rectangle
			}{
				{"SERVER", l.serverLabelY, l.serverRow},
				{"LIBRARY", l.libraryLabelY, l.filterRow},
				{"ABOUT", l.aboutLabelY, l.updateRow},
			} {
				if bottom := tc.y + h; bottom > tc.row.Min.Y {
					t.Errorf("%s label ends at y=%d, past its row's hairline at y=%d", tc.label, bottom, tc.row.Min.Y)
				}
				if tc.y < tc.row.Min.Y-l.sy(60) {
					t.Errorf("%s label at y=%d is above its own band (row starts at %d)", tc.label, tc.y, tc.row.Min.Y)
				}
			}
		})
	}
}
