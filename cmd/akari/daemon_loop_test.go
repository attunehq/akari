package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSyncIntervalRunsImmediatelyThenRepeats(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var n atomic.Int32
	errc := make(chan error, 1)
	go func() {
		errc <- runSyncInterval(ctx, 15*time.Millisecond, func(context.Context) error {
			if n.Add(1) >= 3 {
				cancel()
			}
			return nil
		}, func(string, ...any) {})
	}()
	select {
	case err := <-errc:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("interval loop did not stop after three passes")
	}
	if got := n.Load(); got < 3 {
		t.Fatalf("passes = %d, want at least 3", got)
	}
}

func TestSyncIntervalContinuesAfterPassError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var n atomic.Int32
	errc := make(chan error, 1)
	go func() {
		errc <- runSyncInterval(ctx, 15*time.Millisecond, func(context.Context) error {
			if n.Add(1) >= 2 {
				cancel()
			}
			return errors.New("upload failed")
		}, func(string, ...any) {})
	}()
	select {
	case err := <-errc:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("interval loop stopped on a pass error")
	}
	if got := n.Load(); got < 2 {
		t.Fatalf("passes = %d, want at least 2 after an error", got)
	}
}

func TestSyncIntervalStopsOnCancelDuringPass(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	errc := make(chan error, 1)
	go func() {
		errc <- runSyncInterval(ctx, time.Hour, func(ctx context.Context) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		}, func(string, ...any) {})
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("pass did not start")
	}
	cancel()
	select {
	case err := <-errc:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("interval loop did not return after cancel")
	}
}

func TestDaemonRunRecordsStartupErrorsInRotatingLog(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("AppData", configHome)
	t.Setenv("HOME", configHome)
	logPath := filepath.Join(configHome, "daemon.log")

	err := runDaemonLoop(context.Background(), []string{
		"--config", filepath.Join(configHome, "missing.toml"),
		"--daemon-log", logPath,
	})
	if err == nil {
		t.Fatal("daemon run unexpectedly accepted a missing config")
	}
	data, readErr := os.ReadFile(logPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !strings.Contains(string(data), "akari: "+err.Error()) {
		t.Fatalf("daemon log %q does not contain startup error %q", data, err)
	}
}

func TestSyncIntervalReturnsRestartErrorWithoutAnotherPass(t *testing.T) {
	var n atomic.Int32
	err := runSyncInterval(context.Background(), time.Hour, func(context.Context) error {
		n.Add(1)
		return restartAfterUpdateError{path: "/opt/akari"}
	}, func(string, ...any) {})
	path, ok := restartPath(err)
	if !ok {
		t.Fatalf("error = %v, want restartAfterUpdateError", err)
	}
	if path != "/opt/akari" {
		t.Fatalf("restart path = %q, want /opt/akari", path)
	}
	if n.Load() != 1 {
		t.Fatalf("passes = %d, want 1", n.Load())
	}
}

func TestFinishDaemonLoopRestartsAfterUpdate(t *testing.T) {
	orig := restartDaemon
	t.Cleanup(func() { restartDaemon = orig })
	var gotPath string
	var gotArgs []string
	restartDaemon = func(self string, args []string) error {
		gotPath = self
		gotArgs = append([]string(nil), args...)
		return nil
	}

	if err := finishDaemonLoop(restartAfterUpdateError{path: "/opt/akari"}, []string{"akari", "daemon", "run"}); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/opt/akari" {
		t.Fatalf("restart path = %q, want /opt/akari", gotPath)
	}
	if strings.Join(gotArgs, " ") != "akari daemon run" {
		t.Fatalf("restart args = %v, want akari daemon run", gotArgs)
	}
}

func TestFinishDaemonLoopPassesOtherErrorsThrough(t *testing.T) {
	orig := restartDaemon
	t.Cleanup(func() { restartDaemon = orig })
	restartDaemon = func(string, []string) error {
		t.Fatal("restart ran for a non-update error")
		return nil
	}
	want := errors.New("config missing")
	if err := finishDaemonLoop(want, []string{"akari"}); !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
}

func TestFinishDaemonLoopWrapsRestartFailure(t *testing.T) {
	orig := restartDaemon
	t.Cleanup(func() { restartDaemon = orig })
	restartDaemon = func(string, []string) error {
		return errors.New("exec failed")
	}
	err := finishDaemonLoop(restartAfterUpdateError{path: "/opt/akari"}, []string{"akari"})
	if err == nil || !strings.Contains(err.Error(), "restart after update") {
		t.Fatalf("error = %v, want restart wrapping", err)
	}
}

func TestDaemonRunRejectsNonPositiveInterval(t *testing.T) {
	err := runSyncInterval(context.Background(), 0, func(context.Context) error {
		t.Fatal("pass ran with a non-positive interval")
		return nil
	}, nil)
	if err == nil {
		t.Fatal("non-positive interval was accepted")
	}
}
