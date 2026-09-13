package main

import (
	"testing"
	"time"
)

// tapEvent is one pointer event, at an offset from the start of the
// sequence, and whether the gate is expected to fire on it.
type tapEvent struct {
	at    time.Duration
	phase tapPhase
	want  bool
}

func TestTapGate(t *testing.T) {
	cases := []struct {
		name   string
		events []tapEvent
	}{
		{
			"press_then_release_is_one_tap",
			[]tapEvent{
				{0, tapPress, true},
				{40 * time.Millisecond, tapRelease, false},
			},
		},
		{
			"long_press_is_one_tap",
			[]tapEvent{
				{0, tapPress, true},
				{300 * time.Millisecond, tapOther, false}, // hold
				{2 * time.Second, tapRelease, false},
			},
		},
		{
			"lone_release_fires",
			[]tapEvent{
				{0, tapRelease, true},
			},
		},
		{
			"lone_releases_debounce",
			[]tapEvent{
				{0, tapRelease, true},
				{100 * time.Millisecond, tapRelease, false},
				{400 * time.Millisecond, tapRelease, true},
			},
		},
		{
			"double_tap_inside_window_is_one_tap",
			[]tapEvent{
				{0, tapPress, true},
				{50 * time.Millisecond, tapRelease, false},
				{150 * time.Millisecond, tapPress, false},
				{200 * time.Millisecond, tapRelease, false},
			},
		},
		{
			"deliberate_second_tap_fires",
			[]tapEvent{
				{0, tapPress, true},
				{50 * time.Millisecond, tapRelease, false},
				{600 * time.Millisecond, tapPress, true},
				{650 * time.Millisecond, tapRelease, false},
			},
		},
		{
			"dropped_press_still_claims_its_release",
			[]tapEvent{
				// The second touch is denied by the debounce, and its
				// release lands outside the window; it must not fire the
				// handler the press was refused.
				{0, tapPress, true},
				{50 * time.Millisecond, tapRelease, false},
				{200 * time.Millisecond, tapPress, false},
				{500 * time.Millisecond, tapRelease, false},
			},
		},
		{
			"missing_release_does_not_block_the_next_press",
			[]tapEvent{
				{0, tapPress, true},
				{600 * time.Millisecond, tapPress, true},
			},
		},
		{
			"non_tap_phases_never_fire",
			[]tapEvent{
				{0, tapOther, false},
				{time.Second, tapOther, false},
				{2 * time.Second, tapOther, false},
			},
		},
	}

	start := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var g tapGate
			for i, e := range tc.events {
				got := g.accept(e.phase, start.Add(e.at))
				if got != e.want {
					t.Fatalf("event %d (%v at %v): accept = %v, want %v", i, e.phase, e.at, got, e.want)
				}
			}
		})
	}
}

// The gate's zero value must accept the very first tap: a gate that has
// never fired carries a zero last-tap time, which is far outside the
// debounce window for any real clock reading.
func TestTapGateZeroValueAcceptsFirstTap(t *testing.T) {
	var g tapGate
	if !g.accept(tapPress, time.Now()) {
		t.Fatal("first press on a zero-value gate was dropped")
	}
}
