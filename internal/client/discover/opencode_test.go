package discover

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jssblck/akari/internal/config"
	"github.com/tidwall/gjson"
	_ "modernc.org/sqlite"
)

func TestRootsOpenCode(t *testing.T) {
	home := filepath.Join("/home", "grace")
	roots := Roots(config.Client{}, func(string) string { return "" }, home)
	var got Root
	for _, r := range roots {
		if r.Agent == "opencode" {
			got = r
			break
		}
	}
	want := Root{Agent: "opencode", Dir: filepath.Join(home, ".local", "share", "opencode"), Optional: true}
	if got != want {
		t.Errorf("opencode root = %+v, want %+v", got, want)
	}

	env := map[string]string{"OPENCODE_DATA_DIR": "/custom/opencode"}
	roots = Roots(config.Client{}, func(k string) string { return env[k] }, home)
	got = Root{}
	for _, r := range roots {
		if r.Agent == "opencode" {
			got = r
			break
		}
	}
	if got != (Root{Agent: "opencode", Dir: "/custom/opencode"}) {
		t.Errorf("OPENCODE_DATA_DIR override = %+v", got)
	}

	env = map[string]string{"OPENCODE_DB": "/custom/data/foo.db"}
	roots = Roots(config.Client{}, func(k string) string { return env[k] }, home)
	got = Root{}
	for _, r := range roots {
		if r.Agent == "opencode" {
			got = r
			break
		}
	}
	if got != (Root{Agent: "opencode", Dir: "/custom/data", SessionDB: "foo.db"}) {
		t.Errorf("OPENCODE_DB override = %+v", got)
	}
}

func TestDiscoverOpenCodeMaterializesJSONL(t *testing.T) {
	dir := t.TempDir()
	cache := t.TempDir()
	prev := openCodeCacheDir
	openCodeCacheDir = func(string) (string, error) { return cache, nil }
	t.Cleanup(func() { openCodeCacheDir = prev })

	dbPath := filepath.Join(dir, "opencode.db")
	writeOpenCodeFixtureDB(t, dbPath, false)

	files, notices, err := Discover([]Root{{Agent: "opencode", Dir: dir}}, Excluder{})
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(notices) != 0 {
		t.Fatalf("notices = %v", notices)
	}
	if len(files) != 1 {
		t.Fatalf("files = %d, want 1: %+v", len(files), files)
	}
	if files[0].Agent != "opencode" || files[0].Root != dir {
		t.Errorf("file = %+v", files[0])
	}
	raw, err := os.ReadFile(files[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 3 {
		t.Fatalf("jsonl lines = %d, want 3 (session + user + completed assistant)\n%s", len(lines), raw)
	}
	if gjson.Get(lines[0], "type").String() != "session" || gjson.Get(lines[0], "id").String() != "ses_1" {
		t.Errorf("header = %s", lines[0])
	}
	if gjson.Get(lines[0], "directory").String() != "/home/grace/app" {
		t.Errorf("directory = %s", gjson.Get(lines[0], "directory").String())
	}
	if gjson.Get(lines[1], "data.role").String() != "user" {
		t.Errorf("message 1 = %s", lines[1])
	}
	if gjson.Get(lines[2], "data.role").String() != "assistant" {
		t.Errorf("message 2 = %s", lines[2])
	}
}

func TestDiscoverOpenCodeWithholdsInProgressAssistant(t *testing.T) {
	dir := t.TempDir()
	cache := t.TempDir()
	prev := openCodeCacheDir
	openCodeCacheDir = func(string) (string, error) { return cache, nil }
	t.Cleanup(func() { openCodeCacheDir = prev })

	writeOpenCodeFixtureDB(t, filepath.Join(dir, "opencode.db"), true)

	files, _, err := Discover([]Root{{Agent: "opencode", Dir: dir}}, Excluder{})
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	raw, err := os.ReadFile(files[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 2 {
		t.Fatalf("jsonl lines = %d, want 2 (session + user; in-progress assistant withheld)\n%s", len(lines), raw)
	}
}

func TestDiscoverOpenCodeSkipsMissingOptionalDB(t *testing.T) {
	files, _, err := Discover([]Root{{
		Agent: "opencode", Dir: filepath.Join(t.TempDir(), "missing"), Optional: true,
	}}, Excluder{})
	if err != nil {
		t.Fatalf("optional missing db: %v", err)
	}
	if len(files) != 0 {
		t.Fatalf("files = %+v", files)
	}
}

func writeOpenCodeFixtureDB(t *testing.T, path string, inProgress bool) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now().UnixMilli()
	if _, err := db.Exec(`
		CREATE TABLE project (id TEXT PRIMARY KEY);
		CREATE TABLE workspace (
			id TEXT PRIMARY KEY,
			type TEXT NOT NULL DEFAULT '',
			name TEXT NOT NULL DEFAULT '',
			branch TEXT,
			directory TEXT,
			extra TEXT,
			project_id TEXT NOT NULL,
			time_used INTEGER NOT NULL DEFAULT 0
		);
		CREATE TABLE session (
			id TEXT PRIMARY KEY,
			project_id TEXT NOT NULL,
			workspace_id TEXT,
			parent_id TEXT,
			slug TEXT NOT NULL DEFAULT '',
			directory TEXT NOT NULL DEFAULT '',
			title TEXT NOT NULL DEFAULT '',
			version TEXT NOT NULL DEFAULT '',
			agent TEXT,
			model TEXT,
			time_created INTEGER NOT NULL,
			time_updated INTEGER NOT NULL
		);
		CREATE TABLE message (
			id TEXT PRIMARY KEY,
			session_id TEXT NOT NULL,
			time_created INTEGER NOT NULL,
			time_updated INTEGER NOT NULL,
			data TEXT NOT NULL
		);
		CREATE TABLE part (
			id TEXT PRIMARY KEY,
			message_id TEXT NOT NULL,
			session_id TEXT NOT NULL,
			time_created INTEGER NOT NULL,
			time_updated INTEGER NOT NULL,
			data TEXT NOT NULL
		);
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO project (id) VALUES ('p1')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO session (id, project_id, slug, directory, title, version, agent, model, time_created, time_updated)
		VALUES ('ses_1', 'p1', 'silent-tiger', '/home/grace/app', 'Fix login', '1.18.25', 'build', '{"id":"grok-4.6","providerID":"opencode"}', ?, ?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO message (id, session_id, time_created, time_updated, data)
		VALUES ('msg_u', 'ses_1', ?, ?, '{"role":"user","time":{"created":1},"agent":"build"}')`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO part (id, message_id, session_id, time_created, time_updated, data)
		VALUES ('prt_u', 'msg_u', 'ses_1', ?, ?, '{"type":"text","text":"Fix login"}')`, now, now); err != nil {
		t.Fatal(err)
	}
	asst := `{"role":"assistant","modelID":"grok-4.6","time":{"created":2,"completed":3},"tokens":{"input":1,"output":1,"reasoning":0,"cache":{"read":0,"write":0}}}`
	if inProgress {
		asst = `{"role":"assistant","modelID":"grok-4.6","time":{"created":2},"tokens":{"input":0,"output":0,"reasoning":0,"cache":{"read":0,"write":0}}}`
	}
	if _, err := db.Exec(`INSERT INTO message (id, session_id, time_created, time_updated, data)
		VALUES ('msg_a', 'ses_1', ?, ?, ?)`, now+1, now+1, asst); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO part (id, message_id, session_id, time_created, time_updated, data)
		VALUES ('prt_a', 'msg_a', 'ses_1', ?, ?, '{"type":"text","text":"ok"}')`, now+1, now+1); err != nil {
		t.Fatal(err)
	}
}
