package main

import "time"

// tapDebounce is the minimum gap between two taps that both fire a
// handler. It has to outlast the stream of pointer events one touch can
// produce, and be short enough that two deliberate taps still count as
// two: a quarter second sits between the two.
const tapDebounce = 250 * time.Millisecond

// tapPhase is the part of a touch a pointer event represents. The device
// UI maps InkView's pointer states onto it; those constants come from
// the PocketBook C headers and exist only in the ARM build, so the gate
// below stays free of them and can be exercised off-device.
type tapPhase int

const (
	tapOther tapPhase = iota // move, long, hold: not a tap boundary
	tapPress
	tapRelease
)

// tapGate turns the device's raw pointer stream into single taps. One
// gate serves the whole app: every screen's pointer handler runs its
// event through it, which is what keeps the two rules below honest
// across a screen change.
//
// A touch normally arrives as a press followed by a release, and the
// press is what fires. But PocketBook sometimes delivers only one half
// of a glancing touch, so a release with no press of its own fires too
// rather than being silently swallowed.
//
// Two rules stop one touch from firing twice:
//
//   - A release that closes a press the gate has already seen never
//     fires, however long the finger rested on the screen. This also
//     covers the hand-off between screens: the press that opens a picker
//     is answered by Settings, and its release must not then land on
//     whatever row the picker has drawn under the user's finger.
//   - A tap within tapDebounce of the last accepted one is dropped. A
//     full-screen e-ink redraw takes long enough that an impatient
//     second tap arrives while the old rows are still on display; in the
//     pickers that used to drill two levels deep, or walk two levels
//     back up, for a single intended step.
//
// Touched from the InkView event loop only, so it needs no lock.
type tapGate struct {
	last    time.Time
	pressed bool // a press was seen and its release is still to come
}

// accept reports whether an event of this phase, at this time, should
// fire a tap handler.
func (g *tapGate) accept(phase tapPhase, now time.Time) bool {
	switch phase {
	case tapPress:
		// The release is claimed even when the press itself is dropped
		// as too fast: otherwise the second half of a rushed double tap
		// would fire the handler its press was just denied.
		g.pressed = true
		return g.fire(now)
	case tapRelease:
		if g.pressed {
			g.pressed = false
			return false
		}
		return g.fire(now)
	default:
		return false
	}
}

// fire reports whether a tap at now clears the debounce window,
// recording it when it does.
func (g *tapGate) fire(now time.Time) bool {
	if now.Sub(g.last) < tapDebounce {
		return false
	}
	g.last = now
	return true
}
