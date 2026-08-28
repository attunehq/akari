package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jssblck/akari/internal/selfupdate"
)

func TestShouldApplyDaemonUpdate(t *testing.T) {
	cases := []struct {
		current, latest string
		want            bool
	}{
		{"v1.0.0", "v1.0.0", false},
		{"v1.0.0", "v1.1.0", true},
		{"v1.1.0", "v1.0.0", false},
		{"dev", "v1.0.0", false},
		{"abc123-dirty", "v1.0.0", false},
		{"v1.0.0-rc.1", "v1.0.0", true},
	}
	for _, c := range cases {
		if got := shouldApplyDaemonUpdate(c.current, c.latest); got != c.want {
			t.Errorf("shouldApplyDaemonUpdate(%q, %q) = %v, want %v", c.current, c.latest, got, c.want)
		}
	}
}

func TestApplyDaemonUpdateSkipsWhenCurrent(t *testing.T) {
	c := latestReleaseClient(t, "v1.0.0")
	called := false
	path, err := applyDaemonUpdate(context.Background(), c, "v1.0.0", func(context.Context, *selfupdate.Client, string) (string, error) {
		called = true
		return "/tmp/akari", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if path != "" {
		t.Fatalf("path = %q, want empty when already current", path)
	}
	if called {
		t.Fatal("replace ran for an already-current release")
	}
}

func TestApplyDaemonUpdateSkipsDevelopmentBuild(t *testing.T) {
	c := latestReleaseClient(t, "v1.0.0")
	called := false
	path, err := applyDaemonUpdate(context.Background(), c, "abc123-dirty", func(context.Context, *selfupdate.Client, string) (string, error) {
		called = true
		return "/tmp/akari", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if path != "" {
		t.Fatalf("path = %q, want empty for a development build", path)
	}
	if called {
		t.Fatal("replace ran for a development build")
	}
}

func TestApplyDaemonUpdateReplacesWhenBehind(t *testing.T) {
	c := latestReleaseClient(t, "v1.1.0")
	var gotLatest string
	path, err := applyDaemonUpdate(context.Background(), c, "v1.0.0", func(_ context.Context, _ *selfupdate.Client, latest string) (string, error) {
		gotLatest = latest
		return "/opt/akari", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if path != "/opt/akari" {
		t.Fatalf("path = %q, want /opt/akari", path)
	}
	if gotLatest != "v1.1.0" {
		t.Fatalf("latest = %q, want v1.1.0", gotLatest)
	}
}

func TestApplyDaemonUpdateRejectsEmptyReplacePath(t *testing.T) {
	c := latestReleaseClient(t, "v1.1.0")
	_, err := applyDaemonUpdate(context.Background(), c, "v1.0.0", func(context.Context, *selfupdate.Client, string) (string, error) {
		return "", nil
	})
	if err == nil {
		t.Fatal("applyDaemonUpdate accepted an empty replace path")
	}
}

func TestApplyDaemonUpdatePropagatesLatestTagError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)
	c := selfupdate.New()
	c.APIBaseURL = server.URL
	c.Repo = "grace/hopper"

	_, err := applyDaemonUpdate(context.Background(), c, "v1.0.0", func(context.Context, *selfupdate.Client, string) (string, error) {
		t.Fatal("replace ran after LatestTag failed")
		return "", nil
	})
	if err == nil {
		t.Fatal("applyDaemonUpdate succeeded when LatestTag failed")
	}
	if !strings.Contains(err.Error(), "resolve the latest release") {
		t.Fatalf("error = %v, want latest-release wrapping", err)
	}
}

func TestRunDaemonCycleSyncsAfterUpdateError(t *testing.T) {
	c := failingLatestClient(t)
	synced := false
	var logs []string
	err := runDaemonCycle(context.Background(), c, "v1.0.0", nil, func(context.Context) error {
		synced = true
		return nil
	}, func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf(format, args...))
	})
	if err != nil {
		t.Fatal(err)
	}
	if !synced {
		t.Fatal("sync did not run after an update error")
	}
	if len(logs) != 1 || !strings.Contains(logs[0], "akari daemon: update:") {
		t.Fatalf("logs = %v, want one update error", logs)
	}
}

func TestRunDaemonCycleRestartsInsteadOfSyncing(t *testing.T) {
	c := latestReleaseClient(t, "v1.1.0")
	err := runDaemonCycle(context.Background(), c, "v1.0.0", func(context.Context, *selfupdate.Client, string) (string, error) {
		return "/opt/akari", nil
	}, func(context.Context) error {
		t.Fatal("sync ran after a successful update")
		return nil
	}, func(string, ...any) {})
	path, ok := restartPath(err)
	if !ok {
		t.Fatalf("error = %v, want restartAfterUpdateError", err)
	}
	if path != "/opt/akari" {
		t.Fatalf("restart path = %q, want /opt/akari", path)
	}
}

func TestRunDaemonCycleStopsWhenUpdateIsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		cancel()
		<-r.Context().Done()
	}))
	t.Cleanup(server.Close)
	c := selfupdate.New()
	c.APIBaseURL = server.URL
	c.Repo = "grace/hopper"

	err := runDaemonCycle(ctx, c, "v1.0.0", nil, func(context.Context) error {
		t.Fatal("sync ran after a cancelled update")
		return nil
	}, func(string, ...any) {})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestRunDaemonCycleSyncsWhenCurrent(t *testing.T) {
	c := latestReleaseClient(t, "v1.0.0")
	synced := false
	err := runDaemonCycle(context.Background(), c, "v1.0.0", func(context.Context, *selfupdate.Client, string) (string, error) {
		t.Fatal("replace ran for an already-current release")
		return "", nil
	}, func(context.Context) error {
		synced = true
		return nil
	}, func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	if !synced {
		t.Fatal("sync did not run when already current")
	}
}

func latestReleaseClient(t *testing.T, tag string) *selfupdate.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/releases/latest") {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		fmt.Fprintf(w, `{"tag_name":%q}`, tag)
	}))
	t.Cleanup(server.Close)
	c := selfupdate.New()
	c.APIBaseURL = server.URL
	c.Repo = "grace/hopper"
	return c
}

func failingLatestClient(t *testing.T) *selfupdate.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusBadGateway)
	}))
	t.Cleanup(server.Close)
	c := selfupdate.New()
	c.APIBaseURL = server.URL
	c.Repo = "grace/hopper"
	return c
}
