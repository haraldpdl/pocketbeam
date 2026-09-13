package main

import (
	"errors"
	"image"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestUpdateConfigPublishesACopy(t *testing.T) {
	var s appState
	original := &Config{Profile: "default", Host: "https://a.example", FilterHrefs: []string{"/one"}}
	s.SetSession(original, nil, nil)

	updated := s.UpdateConfig(func(c *Config) {
		c.DeleteMissing = true
		c.FilterHrefs = []string{"/two"}
	})

	if original.DeleteMissing {
		t.Error("the config a goroutine already read was mutated in place")
	}
	if got := original.FilterHrefs[0]; got != "/one" {
		t.Errorf("original filter = %q, want %q", got, "/one")
	}
	if !updated.DeleteMissing || updated.FilterHrefs[0] != "/two" {
		t.Errorf("returned config missing the edit: %+v", updated)
	}
	if s.Config() != updated {
		t.Error("Config() does not return the config UpdateConfig published")
	}
	if updated.Host != original.Host {
		t.Errorf("unedited field lost: host = %q, want %q", updated.Host, original.Host)
	}
}

func TestUpdateConfigWithoutProfileIsNoOp(t *testing.T) {
	var s appState
	called := false
	if got := s.UpdateConfig(func(*Config) { called = true }); got != nil {
		t.Errorf("UpdateConfig with no profile = %+v, want nil", got)
	}
	if called {
		t.Error("fn ran even though no profile is active")
	}
}

func TestSetSessionClosesReplacedStore(t *testing.T) {
	var s appState
	first := openTestStore(t, "first.db")
	second := openTestStore(t, "second.db")

	s.SetSession(&Config{}, nil, first)
	s.SetSession(&Config{}, nil, second)

	if _, err := first.BookCount(); err == nil {
		t.Error("replaced store is still open")
	}
	if _, err := second.BookCount(); err != nil {
		t.Errorf("published store is not usable: %v", err)
	}

	// Re-publishing the same store must not close it.
	s.SetSession(&Config{}, nil, second)
	if _, err := second.BookCount(); err != nil {
		t.Errorf("re-publishing the same store closed it: %v", err)
	}

	s.ClearSession()
	if _, err := second.BookCount(); err == nil {
		t.Error("ClearSession left the store open")
	}
	if cfg, client, store := s.Session(); cfg != nil || client != nil || store != nil {
		t.Errorf("ClearSession left state behind: %v %v %v", cfg, client, store)
	}
}

// TestConcurrentAccessKeepsFieldGroupsTogether is the regression guard for
// the unsynchronised transitions: the probe goroutine used to write the
// wizard's step and error (and the config pointer) while the InkView event
// loop read them, so a repaint could catch one field of a transition
// without the other. Every write below moves a group of fields; every read
// asserts the group is consistent. Run with -race it also flags the raw
// data race.
func TestConcurrentAccessKeepsFieldGroupsTogether(t *testing.T) {
	var s appState
	s.SetSession(&Config{Profile: "p0", Host: "host0", User: "user0"}, nil, nil)

	const rounds = 500
	var wg sync.WaitGroup

	wg.Add(1)
	go func() { // stands in for runProbe
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			s.UpdateWizard(func(w *wizardState) {
				w.step = stepError
				w.err = errors.New("probe failed")
			})
			s.SetScreen(screenFirstRun)
			s.UpdateWizard(func(w *wizardState) {
				w.step = stepTesting
				w.err = nil
			})
			s.SetScreen(screenMain)
		}
	}()

	wg.Add(1)
	go func() { // stands in for a settings toggle
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			s.UpdateConfig(func(c *Config) {
				c.Host = "host1"
				c.User = "user1"
			})
			s.UpdateConfig(func(c *Config) {
				c.Host = "host0"
				c.User = "user0"
			})
		}
	}()

	wg.Add(1)
	go func() { // stands in for Draw
		defer wg.Done()
		for i := 0; i < rounds*4; i++ {
			w := s.Wizard()
			if (w.step == stepError) != (w.err != nil) {
				t.Errorf("torn wizard transition: step %v with err %v", w.step, w.err)
				return
			}
			cfg := s.Config()
			if cfg.Host[len(cfg.Host)-1] != cfg.User[len(cfg.User)-1] {
				t.Errorf("torn config edit: host %q user %q", cfg.Host, cfg.User)
				return
			}
			_ = s.Screen()
			_ = s.Stats()
		}
	}()

	wg.Wait()
}

func TestUpdateStatsKeepsUntouchedFields(t *testing.T) {
	var s appState
	synced := SyncSummary{At: time.Unix(1700000000, 0), Downloaded: 3}
	s.UpdateStats(func(st *mainStats) {
		st.lastSync = synced
		st.hasLastSync = true
		st.bookCount = 12
	})
	// A refresh whose book count succeeded but whose last-sync query
	// failed must keep the previous summary rather than blank the screen.
	s.UpdateStats(func(st *mainStats) { st.bookCount = 13 })

	got := s.Stats()
	if got.bookCount != 13 || !got.hasLastSync || got.lastSync != synced {
		t.Errorf("stats = %+v, want bookCount 13 with last sync %+v kept", got, synced)
	}
}

// TestWizardSnapshotCarriesEveryField pins the contract the probe
// goroutine relies on: Wizard() hands back the complete entered state, so
// runProbe can read the credentials once instead of re-reading fields the
// event loop may have cleared in between.
func TestWizardSnapshotCarriesEveryField(t *testing.T) {
	var s appState
	want := wizardState{
		step:       stepPass,
		backend:    BackendWebDAV,
		url:        "https://nc.example/remote.php/dav",
		user:       "alice",
		pass:       "hunter2",
		name:       "nextcloud",
		err:        errors.New("previous attempt"),
		addProfile: true,
		returnTo:   screenProfileList,
		opdsBtn:    image.Rect(1, 2, 3, 4),
		webdavBtn:  image.Rect(5, 6, 7, 8),
	}
	s.UpdateWizard(func(w *wizardState) { *w = want })

	if got := s.Wizard(); got != want {
		t.Errorf("wizard snapshot = %+v, want %+v", got, want)
	}
}

func openTestStore(t *testing.T, name string) *Store {
	t.Helper()
	st, err := OpenStore(filepath.Join(t.TempDir(), name))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// TestBackTarget pins the Back-key navigation graph, including the two
// entries that reuse the first-run wizard: Settings -> Server and
// profile list -> Add both record where they came from, so Back returns
// there rather than quitting the app. Only the main screen and a genuine
// first run (no profile yet, so no origin recorded) have nowhere to go.
func TestBackTarget(t *testing.T) {
	cases := []struct {
		name string
		cur  screen
		wiz  wizardState
		want screen
		ok   bool
	}{
		{"settings to main", screenSettings, wizardState{}, screenMain, true},
		{"shelf picker to settings", screenShelfPicker, wizardState{}, screenSettings, true},
		{"dir picker to settings", screenDirPicker, wizardState{}, screenSettings, true},
		{"profile list to settings", screenProfileList, wizardState{}, screenSettings, true},
		{"update to settings", screenUpdate, wizardState{}, screenSettings, true},
		{"profile detail to profile list", screenProfileDetail, wizardState{}, screenProfileList, true},
		{
			"change server returns to settings",
			screenFirstRun,
			wizardState{step: stepURL, returnTo: screenSettings},
			screenSettings, true,
		},
		{
			"add profile returns to profile list",
			screenFirstRun,
			wizardState{step: stepProfileName, addProfile: true, returnTo: screenProfileList},
			screenProfileList, true,
		},
		{"first run quits", screenFirstRun, wizardState{step: stepWelcome}, screenFirstRun, false},
		{"main quits", screenMain, wizardState{}, screenMain, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := backTarget(tc.cur, tc.wiz)
			if got != tc.want || ok != tc.ok {
				t.Errorf("backTarget(%v, %+v) = %v, %v; want %v, %v", tc.cur, tc.wiz, got, ok, tc.want, tc.ok)
			}
		})
	}
}

// TestDrawIfOnSkipsAfterScreenChange pins the no-op half of the rule: a
// draw queued for a screen the app has already left must not run.
func TestDrawIfOnSkipsAfterScreenChange(t *testing.T) {
	var s appState
	s.SetScreen(screenUpdate)
	s.SetScreen(screenSettings)

	ran := false
	s.DrawIfOn(screenUpdate, func() { ran = true })
	if ran {
		t.Error("draw ran for a screen the app had already left")
	}
	s.DrawIfOn(screenSettings, func() { ran = true })
	if !ran {
		t.Error("draw for the current screen did not run")
	}
}

// TestDrawIfOnBlocksScreenChange pins the atomic half: a screen
// transition raised while a draw is in flight waits for that draw
// instead of landing in the middle of it, which is what would leave a
// background goroutine's strip painted over the next screen.
func TestDrawIfOnBlocksScreenChange(t *testing.T) {
	var s appState
	s.SetScreen(screenUpdate)

	drawing := make(chan struct{})
	release := make(chan struct{})

	var wg sync.WaitGroup
	wg.Add(1)
	go func() { // stands in for the download goroutine
		defer wg.Done()
		s.DrawIfOn(screenUpdate, func() {
			close(drawing)
			<-release
		})
	}()

	<-drawing
	wg.Add(1)
	go func() { // stands in for a Back tap on the event loop
		defer wg.Done()
		s.SetScreen(screenSettings)
	}()

	// While the draw holds the lock the transition cannot take effect,
	// so the screen the draw is painting for stays up.
	deadline := time.Now().Add(50 * time.Millisecond)
	for time.Now().Before(deadline) {
		if got := s.Screen(); got != screenUpdate {
			close(release)
			wg.Wait()
			t.Fatalf("screen became %v while a draw was in flight", got)
		}
	}
	close(release)
	wg.Wait()

	if got := s.Screen(); got != screenSettings {
		t.Errorf("screen after the transition = %v, want screenSettings", got)
	}
}

// TestDrawFullExcludesPartialDraw pins that a full repaint and a
// background goroutine's partial one cannot overlap: they share
// InkView's single active face, and a full pass clears the screen before
// it redraws, so an interleaved partial draw renders in the wrong face
// or is wiped by the clear.
func TestDrawFullExcludesPartialDraw(t *testing.T) {
	var s appState
	s.SetScreen(screenMain)

	full := make(chan struct{})
	release := make(chan struct{})
	partialRan := make(chan struct{})

	var wg sync.WaitGroup
	wg.Add(1)
	go func() { // stands in for the event loop running Draw
		defer wg.Done()
		s.DrawFull(func() {
			close(full)
			<-release
		})
	}()

	<-full
	wg.Add(1)
	go func() { // stands in for the sync progress ticker
		defer wg.Done()
		s.DrawIfOn(screenMain, func() { close(partialRan) })
	}()

	select {
	case <-partialRan:
		close(release)
		wg.Wait()
		t.Fatal("partial draw ran while a full repaint was in flight")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)

	select {
	case <-partialRan:
	case <-time.After(time.Second):
		t.Fatal("partial draw did not run after the full repaint finished")
	}
	wg.Wait()
}
