//go:build live

package main

import (
	"context"
	"strings"
	"testing"
)

// These tests hit a real CWA instance. They are gated behind the `live` build
// tag so the default `go test ./...` run does not depend on network state.
// Run with: go test -tags=live -run 'TestLive'
//
// Environment variables:
//   BOOKBEAM_TEST_HOST (default: http://cwa.example.internal:8083)
//   BOOKBEAM_TEST_USER (default: admin)
//   BOOKBEAM_TEST_PASS (default: changeme)

const (
	liveHost = "http://cwa.example.internal:8083"
	liveUser = "admin"
	livePass = "changeme"
)

func TestLiveProbe_Happy(t *testing.T) {
	if err := ProbeCWA(context.Background(), liveHost, liveUser, livePass); err != nil {
		t.Errorf("happy path: %v", err)
	}
}

func TestLiveProbe_WrongPassword(t *testing.T) {
	err := ProbeCWA(context.Background(), liveHost, liveUser, "wrong-password")
	if err == nil || !strings.Contains(err.Error(), "rejected") {
		t.Errorf("got %v, want 'rejected'", err)
	}
}

func TestLiveProbe_DNSFail(t *testing.T) {
	err := ProbeCWA(context.Background(), "http://nosuchhost.invalid", "u", "p")
	if err == nil || !strings.Contains(err.Error(), "resolved") {
		t.Errorf("got %v, want 'resolved'", err)
	}
}

func TestLiveProbe_ConnectionRefused(t *testing.T) {
	err := ProbeCWA(context.Background(), "http://cwa.example.internal:59999", "u", "p")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "refused") && !strings.Contains(err.Error(), "reach") {
		t.Errorf("got %v, want 'refused' or 'reach'", err)
	}
}
