package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jssblck/akari/internal/config"
)

func TestParseAssignProjectArgs(t *testing.T) {
	t.Parallel()

	t.Run("both ids", func(t *testing.T) {
		got, err := parseAssignProjectArgs([]string{"--session", "12", "--project", "4"})
		if err != nil {
			t.Fatal(err)
		}
		if got.sessionID != 12 || got.projectID != 4 {
			t.Fatalf("got %+v, want session 12 project 4", got)
		}
	})
	t.Run("missing session", func(t *testing.T) {
		if _, err := parseAssignProjectArgs([]string{"--project", "4"}); err == nil || !strings.Contains(err.Error(), "--session") {
			t.Fatalf("err = %v, want --session", err)
		}
	})
	t.Run("missing project", func(t *testing.T) {
		if _, err := parseAssignProjectArgs([]string{"--session", "12"}); err == nil || !strings.Contains(err.Error(), "--project") {
			t.Fatalf("err = %v, want --project", err)
		}
	})
	t.Run("unexpected args", func(t *testing.T) {
		if _, err := parseAssignProjectArgs([]string{"--session", "12", "--project", "4", "extra"}); err == nil || !strings.Contains(err.Error(), "unexpected") {
			t.Fatalf("err = %v, want unexpected arguments", err)
		}
	})
}

func TestAssignProjectPinsSession(t *testing.T) {
	t.Parallel()
	var gotAuth, gotPath, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		gotBody = string(raw)
		if r.Method != http.MethodPut {
			t.Errorf("method = %s, want PUT", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"session_id": 12, "project_id": 4, "pinned": true,
		})
	}))
	t.Cleanup(srv.Close)

	path := filepath.Join(t.TempDir(), "config.toml")
	if err := config.SaveClient(path, config.Client{ServerURL: srv.URL, Token: "full-tok"}); err != nil {
		t.Fatal(err)
	}
	if err := assignProject(context.Background(), []string{
		"--config", path, "--session", "12", "--project", "4",
	}, srv.Client()); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer full-tok" {
		t.Errorf("Authorization = %q, want Bearer full-tok", gotAuth)
	}
	if gotPath != "/api/v1/app/sessions/12/project" {
		t.Errorf("path = %q", gotPath)
	}
	if !strings.Contains(gotBody, `"project_id":4`) {
		t.Errorf("body = %q, want project_id 4", gotBody)
	}
}

func TestAssignProjectSurfacesServerError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"session is not orphaned"}`))
	}))
	t.Cleanup(srv.Close)

	path := filepath.Join(t.TempDir(), "config.toml")
	if err := config.SaveClient(path, config.Client{ServerURL: srv.URL, Token: "full-tok"}); err != nil {
		t.Fatal(err)
	}
	err := assignProject(context.Background(), []string{
		"--config", path, "--session", "12", "--project", "4",
	}, srv.Client())
	if err == nil || err.Error() != "session is not orphaned" {
		t.Fatalf("err = %v, want session is not orphaned", err)
	}
}

func TestAssignProjectRequiresConfig(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "missing.toml")
	err := assignProject(context.Background(), []string{
		"--config", path, "--session", "12", "--project", "4",
	}, http.DefaultClient)
	if err == nil || !strings.Contains(err.Error(), "akari login") {
		t.Fatalf("err = %v, want a login hint", err)
	}
}
