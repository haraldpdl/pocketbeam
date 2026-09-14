// Blocking confirmations the sync goroutine raises: the pre-flight
// space warning and the delete-missing prompt. Both park their answer on
// a channel the sync goroutine waits on; the drawing is in ui_confirm.go.

package main

import (
	"sync"

	ink "github.com/dennwc/inkview"
)

// spaceWarnState parks a pre-flight plan while the oversize-confirm screen
// is up and routes the user's answer back to the sync goroutine over ch.
// ch is non-nil only while a prompt is pending.
type spaceWarnState struct {
	mu   sync.Mutex
	plan SyncPlan
	ch   chan bool
}

// deleteConfirmState holds the deletion set awaiting user confirmation
// and the channel the sync goroutine blocks on. ch is non-nil only while
// a prompt is pending.
type deleteConfirmState struct {
	mu      sync.Mutex
	pending []LocalBook
	ch      chan bool
}

// confirmSpace parks the plan on the UI, flips to the oversize-confirm
// screen, and blocks the sync goroutine until the user answers. Returns
// true to proceed with the download anyway, false to cancel the sync.
func (a *app) confirmSpace(plan SyncPlan) bool {
	a.spaceWarn.mu.Lock()
	a.spaceWarn.plan = plan
	a.spaceWarn.ch = make(chan bool, 1)
	ch := a.spaceWarn.ch
	a.spaceWarn.mu.Unlock()
	a.SetScreen(screenSpaceWarn)
	ink.Repaint()
	return <-ch
}

// answerSpace replies to an in-flight oversize prompt. Safe to call when
// no prompt is pending; it is a no-op.
func (a *app) answerSpace(ok bool) {
	a.spaceWarn.mu.Lock()
	ch := a.spaceWarn.ch
	a.spaceWarn.ch = nil
	a.spaceWarn.mu.Unlock()
	if ch == nil {
		return
	}
	ch <- ok
	a.SetScreen(screenMain)
	ink.Repaint()
}

// spaceWarnView snapshots the parked plan for the draw, so the drawing
// itself touches no shared state and can run off-device.
func (a *app) spaceWarnView() spaceWarnView {
	a.spaceWarn.mu.Lock()
	defer a.spaceWarn.mu.Unlock()
	return spaceWarnViewOf(a.spaceWarn.plan)
}

func (a *app) spaceWarnKey(e ink.KeyEvent) bool {
	switch e.Key {
	case ink.KeyOk:
		a.answerSpace(true)
		return true
	case ink.KeyBack:
		a.answerSpace(false)
		return true
	}
	return false
}

func (a *app) spaceWarnPointer(e ink.PointerEvent) bool {
	switch {
	case e.Point.In(a.layout.confirmLeftButton):
		a.answerSpace(true)
		return true
	case e.Point.In(a.layout.confirmRightButton):
		a.answerSpace(false)
		return true
	}
	return false
}

// ---------- Delete-missing confirmation ----------

// confirmDeletions is invoked by the sync goroutine when delete-missing
// finds books absent from the remote. It parks the deletions on the UI,
// flips to the confirm screen, and blocks until the user answers.
func (a *app) confirmDeletions(deletions []LocalBook) bool {
	a.delConfirm.mu.Lock()
	a.delConfirm.pending = deletions
	a.delConfirm.ch = make(chan bool, 1)
	ch := a.delConfirm.ch
	a.delConfirm.mu.Unlock()
	a.SetScreen(screenDeleteConfirm)
	ink.Repaint()
	return <-ch
}

// answerDelete replies to an in-flight confirmation prompt. Safe to call
// when no prompt is pending; it is a no-op.
func (a *app) answerDelete(ok bool) {
	a.delConfirm.mu.Lock()
	ch := a.delConfirm.ch
	a.delConfirm.ch = nil
	a.delConfirm.pending = nil
	a.delConfirm.mu.Unlock()
	if ch == nil {
		return
	}
	ch <- ok
	// The sync goroutine will flip back to screenMain once Sync returns;
	// until then the screen stays on a transient "processing" visual.
	a.SetScreen(screenMain)
	ink.Repaint()
}

// deleteConfirmView phrases the parked deletion set for the draw.
func (a *app) deleteConfirmView() deleteConfirmView {
	a.delConfirm.mu.Lock()
	defer a.delConfirm.mu.Unlock()
	return deleteConfirmViewOf(a.delConfirm.pending)
}

func (a *app) deleteConfirmKey(e ink.KeyEvent) bool {
	switch e.Key {
	case ink.KeyOk:
		a.answerDelete(true)
		return true
	case ink.KeyBack:
		a.answerDelete(false)
		return true
	}
	return false
}

func (a *app) deleteConfirmPointer(e ink.PointerEvent) bool {
	switch {
	case e.Point.In(a.layout.confirmLeftButton):
		a.answerDelete(false)
		return true
	case e.Point.In(a.layout.confirmRightButton):
		a.answerDelete(true)
		return true
	}
	return false
}
