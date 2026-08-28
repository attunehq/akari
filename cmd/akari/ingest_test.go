package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jssblck/akari/internal/config"
	"github.com/jssblck/akari/internal/parser"
)

func TestParseIngestArgs(t *testing.T) {
	t.Parallel()

	t.Run("root flag", func(t *testing.T) {
		got, err := parseIngestArgs([]string{"--root", "/sessions"})
		if err != nil {
			t.Fatal(err)
		}
		if got.root != "/sessions" {
			t.Fatalf("root = %q, want /sessions", got.root)
		}
		if !got.finalize {
			t.Fatal("finalize defaults to true")
		}
	})
	t.Run("positional root", func(t *testing.T) {
		got, err := parseIngestArgs([]string{"/sessions"})
		if err != nil {
			t.Fatal(err)
		}
		if got.root != "/sessions" {
			t.Fatalf("root = %q, want /sessions", got.root)
		}
	})
	t.Run("finalize false", func(t *testing.T) {
		got, err := parseIngestArgs([]string{"--root", "/sessions", "--finalize=false"})
		if err != nil {
			t.Fatal(err)
		}
		if got.finalize {
			t.Fatal("finalize = true, want false")
		}
	})
	t.Run("missing root", func(t *testing.T) {
		if _, err := parseIngestArgs(nil); err == nil || !strings.Contains(err.Error(), "--root") {
			t.Fatalf("err = %v, want --root", err)
		}
	})
	t.Run("flag and positional together", func(t *testing.T) {
		if _, err := parseIngestArgs([]string{"--root", "/a", "/b"}); err == nil || !strings.Contains(err.Error(), "not both") {
			t.Fatalf("err = %v, want not both", err)
		}
	})
	t.Run("unexpected args", func(t *testing.T) {
		if _, err := parseIngestArgs([]string{"/a", "/b"}); err == nil || !strings.Contains(err.Error(), "unexpected") {
			t.Fatalf("err = %v, want unexpected arguments", err)
		}
	})
}

func TestIngestRootsCoverEveryAgent(t *testing.T) {
	t.Parallel()
	roots := ingestRoots("/sessions")
	if len(roots) != len(parser.Agents) {
		t.Fatalf("got %d roots, want %d", len(roots), len(parser.Agents))
	}
	seen := map[string]bool{}
	for _, r := range roots {
		if r.Dir != "/sessions" || !r.Optional {
			t.Errorf("root %+v, want dir /sessions optional", r)
		}
		seen[r.Agent] = true
	}
	for _, a := range parser.Agents {
		if !seen[string(a)] {
			t.Errorf("missing agent %s", a)
		}
	}
}

func TestIngestRejectsMissingRoot(t *testing.T) {
	t.Parallel()
	err := ingest(context.Background(), []string{"--root", filepath.Join(t.TempDir(), "missing")}, http.DefaultClient)
	if err == nil || !strings.Contains(err.Error(), "stat --root") {
		t.Fatalf("err = %v, want stat --root", err)
	}
}

func TestIngestRejectsAFileAsRoot(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "sess.jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := ingest(context.Background(), []string{"--root", path}, http.DefaultClient)
	if err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("err = %v, want not a directory", err)
	}
}

func TestDiscoverIngestRootPicksPiNotClaude(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "p-7.jsonl"), []byte(`{"type":"session","id":"p-7","cwd":"/home/grace/proj"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	files, _, err := discoverIngestRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("got %d files, want 1: %+v", len(files), files)
	}
	if files[0].Agent != "pi" {
		t.Errorf("agent = %q, want pi", files[0].Agent)
	}
}

func TestIngestUploadsAndFinalizes(t *testing.T) {
	t.Parallel()
	fake := newIngestFake()
	srv := httptest.NewServer(fake.handler())
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "p-7.jsonl"), []byte(`{"type":"session","id":"p-7","cwd":"/home/grace/proj"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "config.toml")
	if err := config.SaveClient(cfgPath, config.Client{ServerURL: srv.URL, Token: "ingest-tok"}); err != nil {
		t.Fatal(err)
	}

	if err := ingest(context.Background(), []string{"--config", cfgPath, "--root", dir}, srv.Client()); err != nil {
		t.Fatal(err)
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.auth != "Bearer ingest-tok" {
		t.Errorf("Authorization = %q, want Bearer ingest-tok", fake.auth)
	}
	if fake.announce["agent"] != "pi" {
		t.Errorf("announce agent = %v, want pi", fake.announce["agent"])
	}
	if fake.announce["source_session_id"] != "p-7" {
		t.Errorf("announce source_session_id = %v, want p-7", fake.announce["source_session_id"])
	}
	if fake.announce["terminal"] != true {
		t.Errorf("announce terminal = %v, want true", fake.announce["terminal"])
	}
	if fake.finalizes != 1 {
		t.Errorf("finalize calls = %d, want 1", fake.finalizes)
	}
	if len(fake.buf) == 0 {
		t.Fatal("uploaded no transcript bytes")
	}
}

func TestIngestEmptyRootIsSuccess(t *testing.T) {
	t.Parallel()
	fake := newIngestFake()
	srv := httptest.NewServer(fake.handler())
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	if err := config.SaveClient(cfgPath, config.Client{ServerURL: srv.URL, Token: "t"}); err != nil {
		t.Fatal(err)
	}
	if err := ingest(context.Background(), []string{"--config", cfgPath, "--root", dir}, srv.Client()); err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.announce != nil {
		t.Errorf("announce = %v, want none for an empty root", fake.announce)
	}
}

func TestIngestFinalizeFalseOmitsTerminal(t *testing.T) {
	t.Parallel()
	fake := newIngestFake()
	srv := httptest.NewServer(fake.handler())
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "p-8.jsonl"), []byte(`{"type":"session","id":"p-8","cwd":"/home/grace/proj"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "config.toml")
	if err := config.SaveClient(cfgPath, config.Client{ServerURL: srv.URL, Token: "t"}); err != nil {
		t.Fatal(err)
	}

	if err := ingest(context.Background(), []string{
		"--config", cfgPath, "--root", dir, "--finalize=false",
	}, srv.Client()); err != nil {
		t.Fatal(err)
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.announce["terminal"] != false {
		t.Errorf("announce terminal = %v, want false", fake.announce["terminal"])
	}
	if fake.finalizes != 0 {
		t.Errorf("finalize calls = %d, want 0", fake.finalizes)
	}
}

// ingestFake is a minimal ingest-protocol stand-in for the command tests.
type ingestFake struct {
	mu        sync.Mutex
	auth      string
	announce  map[string]any
	buf       []byte
	finalizes int
}

func newIngestFake() *ingestFake { return &ingestFake{} }

func (s *ingestFake) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/ingest/session", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.auth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&s.announce)
		sum := sha256.Sum256(s.buf)
		n := len(s.buf)
		s.mu.Unlock()
		writeIngestJSON(w, map[string]any{
			"session_id":    1,
			"stored_bytes":  n,
			"prefix_sha256": hex.EncodeToString(sum[:]),
		})
	})
	mux.HandleFunc("POST /api/v1/ingest/session/{id}/chunk", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.buf = append(s.buf, body...)
		n := len(s.buf)
		s.mu.Unlock()
		writeIngestJSON(w, map[string]any{"stored_bytes": n})
	})
	mux.HandleFunc("POST /api/v1/ingest/session/{id}/finalize", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.finalizes++
		s.mu.Unlock()
		writeIngestJSON(w, map[string]any{"finalized": true})
	})
	mux.HandleFunc("POST /api/v1/ingest/blobs/check", func(w http.ResponseWriter, r *http.Request) {
		writeIngestJSON(w, map[string]any{"missing": []string{}})
	})
	return mux
}

func writeIngestJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
