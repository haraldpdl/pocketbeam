//go:build !arm

package main

import (
	"errors"
	"image"
	"testing"
)

func drawWizardOn(w wizardState) (*recordCanvas, bool) {
	c := newRecordCanvas()
	_, busy := drawWizard(c, computeLayout(c.Size()), w)
	return c, busy
}

func TestDrawWizardSteps(t *testing.T) {
	cases := []struct {
		name string
		wiz  wizardState
		want []string
		miss string
	}{
		{
			name: "welcome",
			wiz:  wizardState{step: stepWelcome},
			want: []string{"pocketbeam", "Wireless sync from a book server.", "OPDS catalog", "WebDAV", "Press OK or tap to begin."},
		},
		{
			name: "profile name",
			wiz:  wizardState{step: stepProfileName, name: "nextcloud"},
			want: []string{"Name this profile", "Name: nextcloud"},
		},
		{
			name: "profile name rejected",
			wiz:  wizardState{step: stepProfileName, err: errors.New(`profile "home" already exists`)},
			want: []string{`profile "home" already exists`},
			miss: "Name: ",
		},
		{
			name: "backend choice",
			wiz:  wizardState{step: stepBackend},
			want: []string{"Choose server type", "Calibre-Web / OPDS", "WebDAV / Nextcloud"},
		},
		{
			name: "server url",
			wiz:  wizardState{step: stepURL, url: "https://library.example.com:8083"},
			want: []string{"Step 1 of 3", "Server URL", "Server: https://library.example.com:8083"},
			miss: "User: ",
		},
		{
			name: "password",
			wiz:  wizardState{step: stepPass, url: "https://nc.example", user: "alice", pass: "hunter2"},
			want: []string{"Step 3 of 3", "Password", "Server: https://nc.example", "User: alice"},
			// The entered password is never echoed back to the screen.
			miss: "hunter2",
		},
		{
			name: "testing",
			wiz:  wizardState{step: stepTesting, url: "https://nc.example"},
			want: []string{"Testing connection", "https://nc.example"},
		},
		{
			name: "failed",
			wiz:  wizardState{step: stepError, err: errors.New("server did not respond")},
			want: []string{"Connection failed", "server did not respond", "Press OK or tap to try again."},
		},
		{
			name: "failed without a message",
			wiz:  wizardState{step: stepError},
			want: []string{"Connection failed", "Unknown error"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := drawWizardOn(tc.wiz)
			for _, want := range tc.want {
				if !c.drew(want) {
					t.Errorf("step did not draw %q; drew %q", want, c.texts)
				}
			}
			if tc.miss != "" && c.drew(tc.miss) {
				t.Errorf("step drew %q, which it should not; drew %q", tc.miss, c.texts)
			}
		})
	}
}

// The busy icon belongs to the one step that is waiting on the network:
// any other step raising it would leave the panel showing an hourglass
// over a screen the user is expected to act on.
func TestDrawWizardBusyOnlyWhileTesting(t *testing.T) {
	l := computeLayout(screenshotScreen)
	for _, step := range []wizardStep{stepWelcome, stepProfileName, stepBackend, stepURL, stepUser, stepPass, stepError} {
		if _, busy := drawWizardOn(wizardState{step: step}); busy {
			t.Errorf("step %d asked for the busy icon", step)
		}
	}
	at, busy := drawWizard(newRecordCanvas(), l, wizardState{step: stepTesting, url: "https://nc.example"})
	if !busy {
		t.Fatal("the testing step did not ask for the busy icon")
	}
	if panel := (image.Rectangle{Max: l.screen}); !at.In(panel) {
		t.Errorf("busy icon at %v, outside the panel %v", at, panel)
	}
}

// The backend step's two options are tap targets the pointer handler
// reads straight off the layout, so the rows have to be drawn exactly
// where the layout puts them.
func TestDrawWizardBackendRowsMatchTheTapTargets(t *testing.T) {
	l := computeLayout(screenshotScreen)
	c := newImageCanvas(screenshotScreen)
	drawWizard(c, l, wizardState{step: stepBackend})
	if n := inked(c.img, l.wizardOPDSRow); n < 100 {
		t.Errorf("OPDS row %v has %d inked pixels, want the drawn option", l.wizardOPDSRow, n)
	}
	if n := inked(c.img, l.wizardWebDAVRow); n < 100 {
		t.Errorf("WebDAV row %v has %d inked pixels, want the drawn option", l.wizardWebDAVRow, n)
	}
}

// Every line is placed by a hard-coded offset against the face it is
// drawn in, so a step that fills in one line too many silently stacks
// two of them. There is no device in CI, so the geometry is asserted
// directly, as on the main screen.
func TestDrawWizardLinesDoNotOverlap(t *testing.T) {
	long := "https://library.example.com:8083/opds/a-rather-long-path-that-gets-cut"
	for name, wiz := range map[string]wizardState{
		"welcome":              {step: stepWelcome},
		"profile name":         {step: stepProfileName, name: "nextcloud", err: errors.New("profile \"nextcloud\" already exists")},
		"backend choice":       {step: stepBackend},
		"url entered":          {step: stepURL, url: long},
		"user entered":         {step: stepUser, url: long, user: "alice"},
		"password step":        {step: stepPass, url: long, user: "alice"},
		"testing":              {step: stepTesting, url: long},
		"failed with a reason": {step: stepError, err: errors.New("server did not respond in time")},
	} {
		t.Run(name, func(t *testing.T) {
			c, _ := drawWizardOn(wiz)
			if a, b, ok := c.overlappingText(); ok {
				t.Errorf("%q at %v overlaps %q at %v", a.s, a.r, b.s, b.r)
			}
		})
	}
}
