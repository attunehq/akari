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

func TestDiscoverOpenCodeAcceptsDualWrittenProjection(t *testing.T) {
	dir := t.TempDir()
	cache := t.TempDir()
	prev := openCodeCacheDir
	openCodeCacheDir = func(string) (string, error) { return cache, nil }
	t.Cleanup(func() { openCodeCacheDir = prev })

	dbPath := filepath.Join(dir, "opencode.db")
	writeOpenCodeFixtureDB(t, dbPath, false)
	addSessionMessageTable(t, dbPath)
	execOpenCodeFixture(t, dbPath, `
		INSERT INTO session_message (id, session_id, type, seq, time_created, time_updated, data) VALUES
			('msg_u', 'ses_1', 'user', 1, 1, 1, '{"text":"Fix login","files":[],"agents":[]}'),
			('msg_a', 'ses_1', 'assistant', 2, 2, 2, '{"content":[{"type":"text","id":"txt_1","text":"ok"}]}'),
			('switch_1', 'ses_1', 'model-switched', 3, 3, 3, '{}')`)

	files, _, err := Discover([]Root{{Agent: "opencode", Dir: dir}}, Excluder{})
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("files = %d, want 1", len(files))
	}
}

func TestDiscoverOpenCodeSkipsSessionMissingLegacyTurns(t *testing.T) {
	dir := t.TempDir()
	cache := t.TempDir()
	prev := openCodeCacheDir
	openCodeCacheDir = func(string) (string, error) { return cache, nil }
	t.Cleanup(func() { openCodeCacheDir = prev })

	dbPath := filepath.Join(dir, "opencode.db")
	writeOpenCodeFixtureDB(t, dbPath, false)
	addSessionMessageTable(t, dbPath)
	execOpenCodeFixture(t, dbPath, `
		INSERT INTO session (id, project_id, slug, directory, title, version, time_created, time_updated)
		VALUES ('ses_2', 'p1', 'kind-panda', '/home/grace/app', 'Add search', '1.18.25', 2, 2);
		INSERT INTO session_message (id, session_id, type, seq, time_created, time_updated, data)
		VALUES ('msg_u_2', 'ses_2', 'user', 1, 2, 2, '{"text":"Add search"}')`)

	files, _, err := Discover([]Root{{Agent: "opencode", Dir: dir}}, Excluder{})
	if err == nil {
		t.Fatal("discover succeeded, want an incomplete-session error")
	}
	if !strings.Contains(err.Error(), "ses_2") || !strings.Contains(err.Error(), "opencode export") {
		t.Errorf("error = %v, want the skipped session and export recovery", err)
	}
	if len(files) != 1 {
		t.Fatalf("files = %d, want the readable session", len(files))
	}
	if filepath.Base(files[0].Path) != "ses_1.jsonl" {
		t.Errorf("file = %s, want ses_1.jsonl", files[0].Path)
	}
	if _, statErr := os.Stat(filepath.Join(cache, "ses_2.jsonl")); !os.IsNotExist(statErr) {
		t.Errorf("unsupported session cache error = %v, want not found", statErr)
	}
}

func TestDiscoverOpenCodeSkipsSessionMissingLegacyParts(t *testing.T) {
	for _, partID := range []string{"prt_u", "prt_a"} {
		t.Run(partID, func(t *testing.T) {
			dir := t.TempDir()
			cache := t.TempDir()
			prev := openCodeCacheDir
			openCodeCacheDir = func(string) (string, error) { return cache, nil }
			t.Cleanup(func() { openCodeCacheDir = prev })

			dbPath := filepath.Join(dir, "opencode.db")
			writeOpenCodeFixtureDB(t, dbPath, false)
			addSessionMessageTable(t, dbPath)
			execOpenCodeFixture(t, dbPath, `
				INSERT INTO session_message (id, session_id, type, seq, time_created, time_updated, data) VALUES
					('msg_u', 'ses_1', 'user', 1, 1, 1, '{"text":"Fix login"}'),
					('msg_a', 'ses_1', 'assistant', 2, 2, 2, '{"content":[{"type":"text","id":"txt_1","text":"ok"}]}');
				DELETE FROM part WHERE id = '`+partID+`'`)

			files, _, err := Discover([]Root{{Agent: "opencode", Dir: dir}}, Excluder{})
			if err == nil {
				t.Fatal("discover succeeded, want an incomplete-session error")
			}
			if len(files) != 0 {
				t.Fatalf("files = %d, want 0", len(files))
			}
			if _, statErr := os.Stat(filepath.Join(cache, "ses_1.jsonl")); !os.IsNotExist(statErr) {
				t.Errorf("unsupported session cache error = %v, want not found", statErr)
			}
		})
	}
}

func TestDiscoverOpenCodeAcceptsProjectedResourceAsLegacyText(t *testing.T) {
	dir := t.TempDir()
	cache := t.TempDir()
	prev := openCodeCacheDir
	openCodeCacheDir = func(string) (string, error) { return cache, nil }
	t.Cleanup(func() { openCodeCacheDir = prev })

	dbPath := filepath.Join(dir, "opencode.db")
	writeOpenCodeFixtureDB(t, dbPath, false)
	addSessionMessageTable(t, dbPath)
	execOpenCodeFixture(t, dbPath, `
		INSERT INTO session_message (id, session_id, type, seq, time_created, time_updated, data) VALUES
			('msg_u', 'ses_1', 'user', 1, 1, 1, '{"text":"Fix login","files":[{"uri":"mcp://docs/login","mime":"text/plain"}]}'),
			('msg_a', 'ses_1', 'assistant', 2, 2, 2, '{"content":[{"type":"text","id":"txt_1","text":"ok"}]}');
		INSERT INTO part (id, message_id, session_id, time_created, time_updated, data)
		VALUES ('prt_resource', 'msg_u', 'ses_1', 1, 1, '{"type":"text","synthetic":true,"text":"Reading MCP resource: login (mcp://docs/login)"}')`)

	files, _, err := Discover([]Root{{Agent: "opencode", Dir: dir}}, Excluder{})
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("files = %d, want 1", len(files))
	}
}

func TestDiscoverOpenCodeSkipsProjectedFileWithoutLegacyContent(t *testing.T) {
	dir := t.TempDir()
	cache := t.TempDir()
	prev := openCodeCacheDir
	openCodeCacheDir = func(string) (string, error) { return cache, nil }
	t.Cleanup(func() { openCodeCacheDir = prev })

	dbPath := filepath.Join(dir, "opencode.db")
	writeOpenCodeFixtureDB(t, dbPath, false)
	addSessionMessageTable(t, dbPath)
	execOpenCodeFixture(t, dbPath, `
		INSERT INTO session_message (id, session_id, type, seq, time_created, time_updated, data) VALUES
			('msg_u', 'ses_1', 'user', 1, 1, 1, '{"text":"Fix login","files":[{"uri":"file:///home/grace/login.png","mime":"image/png"}]}'),
			('msg_a', 'ses_1', 'assistant', 2, 2, 2, '{"content":[{"type":"text","id":"txt_1","text":"ok"}]}')`)

	files, _, err := Discover([]Root{{Agent: "opencode", Dir: dir}}, Excluder{})
	if err == nil {
		t.Fatal("discover succeeded, want an incomplete-session error")
	}
	if len(files) != 0 {
		t.Fatalf("files = %d, want 0", len(files))
	}
}

func TestDiscoverOpenCodeAcceptsEmptySessionMessage(t *testing.T) {
	dir := t.TempDir()
	cache := t.TempDir()
	prev := openCodeCacheDir
	openCodeCacheDir = func(string) (string, error) { return cache, nil }
	t.Cleanup(func() { openCodeCacheDir = prev })

	dbPath := filepath.Join(dir, "opencode.db")
	writeOpenCodeFixtureDB(t, dbPath, false)
	addSessionMessageTable(t, dbPath)

	files, _, err := Discover([]Root{{Agent: "opencode", Dir: dir}}, Excluder{})
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("files = %d, want 1", len(files))
	}
}

func TestDiscoverOpenCodeRefusesChangedColumns(t *testing.T) {
	dir := t.TempDir()
	cache := t.TempDir()
	prev := openCodeCacheDir
	openCodeCacheDir = func(string) (string, error) { return cache, nil }
	t.Cleanup(func() { openCodeCacheDir = prev })

	dbPath := filepath.Join(dir, "opencode.db")
	writeOpenCodeFixtureDB(t, dbPath, false)
	execOpenCodeFixture(t, dbPath, `ALTER TABLE session DROP COLUMN workspace_id`)

	_, _, err := Discover([]Root{{Agent: "opencode", Dir: dir}}, Excluder{})
	if err == nil {
		t.Fatal("discover succeeded, want a refusal")
	}
	if !strings.Contains(err.Error(), "workspace_id") {
		t.Errorf("error = %v, want it to name the missing column", err)
	}
}

func TestDiscoverOpenCodeExplainsMissingLegacyTable(t *testing.T) {
	dir := t.TempDir()
	cache := t.TempDir()
	prev := openCodeCacheDir
	openCodeCacheDir = func(string) (string, error) { return cache, nil }
	t.Cleanup(func() { openCodeCacheDir = prev })

	dbPath := filepath.Join(dir, "opencode.db")
	writeOpenCodeFixtureDB(t, dbPath, false)
	addSessionMessageTable(t, dbPath)
	execOpenCodeFixture(t, dbPath, `DROP TABLE part`)

	_, _, err := Discover([]Root{{Agent: "opencode", Dir: dir}}, Excluder{})
	if err == nil {
		t.Fatal("discover succeeded, want a missing-table error")
	}
	if !strings.Contains(err.Error(), `table "part" is missing`) || !strings.Contains(err.Error(), "opencode export") {
		t.Errorf("error = %v, want the missing table and export recovery", err)
	}
}

func addSessionMessageTable(t *testing.T, path string) {
	t.Helper()
	execOpenCodeFixture(t, path, `CREATE TABLE session_message (
		id TEXT PRIMARY KEY,
		session_id TEXT NOT NULL,
		type TEXT NOT NULL,
		seq INTEGER NOT NULL,
		time_created INTEGER NOT NULL,
		time_updated INTEGER NOT NULL,
		data TEXT NOT NULL
	)`)
}

func execOpenCodeFixture(t *testing.T, path, stmt string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(stmt); err != nil {
		t.Fatal(err)
	}
}
