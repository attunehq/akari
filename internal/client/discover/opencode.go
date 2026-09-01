package discover

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/tidwall/gjson"
	_ "modernc.org/sqlite"
)

const openCodeDBName = "opencode.db"

// openCodeSettleWindow is how long an OpenCode session must be idle before an
// in-progress assistant turn is materialized. OpenCode updates the last
// assistant message in place while the model streams, so emitting that turn
// before it completes would rewrite a JSONL line the ingest prefix-hash already
// covered. The window matches the client's Codex trailing-turn settle.
const openCodeSettleWindow = 60 * time.Second

// openCodeCacheDir returns the directory that holds materialized OpenCode JSONL
// files for one data root. Tests replace it so they do not touch the user cache.
var openCodeCacheDir = func(dataDir string) (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("locate user cache dir: %w", err)
	}
	sum := sha256.Sum256([]byte(dataDir))
	return filepath.Join(base, "akari", "opencode", hex.EncodeToString(sum[:8])), nil
}

func openCodeDBPath(root Root, walkDir string) string {
	name := root.SessionDB
	if name == "" {
		name = openCodeDBName
	}
	return filepath.Join(walkDir, name)
}

// discoverOpenCode lists sessions in the OpenCode SQLite database at walkDir and
// materializes each into a JSONL file the rest of the client treats like any
// other session transcript.
func discoverOpenCode(root Root, walkDir string, ex Excluder) (files []File, err error) {
	dbPath := openCodeDBPath(root, walkDir)
	if ex.Excluded(dbPath) {
		return nil, nil
	}
	info, err := os.Lstat(dbPath)
	if err != nil {
		if os.IsNotExist(err) {
			if root.Optional {
				return nil, nil
			}
			return nil, fmt.Errorf("open opencode database %s: %w", dbPath, err)
		}
		return nil, fmt.Errorf("inspect opencode database %s: %w", dbPath, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("inspect opencode database %s: symlinks are not allowed", dbPath)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("inspect opencode database %s: not a regular file", dbPath)
	}

	cacheDir, err := openCodeCacheDir(walkDir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		return nil, fmt.Errorf("create opencode cache %s: %w", cacheDir, err)
	}

	db, err := openOpenCodeDB(dbPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	// Read the schema and session list from one short snapshot.
	tx, err := db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("read opencode database %s: %w", dbPath, err)
	}
	defer tx.Rollback()

	if err := probeOpenCodeSchema(tx); err != nil {
		return nil, fmt.Errorf("opencode database %s: %w", dbPath, err)
	}
	sessions, err := listOpenCodeSessions(tx)
	if err != nil {
		return nil, fmt.Errorf("list opencode sessions in %s: %w", dbPath, err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("read opencode database %s: %w", dbPath, err)
	}
	out := make([]File, 0, len(sessions))
	skipped := 0
	firstSkipped := ""
	for _, sess := range sessions {
		path := filepath.Join(cacheDir, sess.ID+".jsonl")
		if ex.Excluded(path) {
			continue
		}
		supported, err := materializeOpenCodeSession(db, sess, path)
		if err != nil {
			return out, fmt.Errorf("materialize opencode session %s: %w", sess.ID, err)
		}
		if !supported {
			skipped++
			if firstSkipped == "" {
				firstSkipped = sess.ID
			}
			continue
		}
		out = append(out, File{Agent: "opencode", Root: walkDir, Path: path})
	}
	if skipped > 0 {
		return out, fmt.Errorf(
			"skip %d OpenCode session(s), including %s: session_message has turns or content missing from the legacy tables, so this akari build cannot read them without truncation; export them with `opencode export <session-id>`",
			skipped, firstSkipped)
	}
	return out, nil
}

var openCodeColumns = map[string][]string{
	"session":   {"id", "parent_id", "workspace_id", "directory", "title", "slug", "agent", "model", "time_created", "time_updated"},
	"workspace": {"id", "branch"},
	"message":   {"id", "session_id", "time_created", "data"},
	"part":      {"id", "message_id", "session_id", "time_created", "data"},
}

type openCodeQuerier interface {
	Query(query string, args ...any) (*sql.Rows, error)
}

func probeOpenCodeSchema(db openCodeQuerier) error {
	hasSessionMessage, err := probeOpenCodeSessionMessage(db)
	if err != nil {
		return err
	}

	for _, table := range []string{"session", "workspace", "message", "part"} {
		cols, err := openCodeTableColumns(db, table)
		if err != nil {
			return fmt.Errorf("inspect table %q: %w", table, err)
		}
		if len(cols) == 0 {
			if hasSessionMessage {
				return fmt.Errorf("table %q is missing, so this akari build cannot read the OpenCode database without truncation; export sessions with `opencode export <session-id>`", table)
			}
			return fmt.Errorf("table %q is missing, so this is not a schema akari can read", table)
		}
		for _, want := range openCodeColumns[table] {
			if !cols[want] {
				return fmt.Errorf("table %q has no column %q, so the schema has changed", table, want)
			}
		}
	}
	return nil
}

func probeOpenCodeSessionMessage(db openCodeQuerier) (bool, error) {
	sessionMessageCols, err := openCodeTableColumns(db, "session_message")
	if err != nil {
		return false, fmt.Errorf("inspect table %q: %w", "session_message", err)
	}
	for _, want := range []string{"id", "session_id", "type", "data"} {
		if len(sessionMessageCols) > 0 && !sessionMessageCols[want] {
			return false, fmt.Errorf("table %q has no column %q, so the schema has changed", "session_message", want)
		}
	}
	return len(sessionMessageCols) > 0, nil
}

func openCodeTableColumns(db openCodeQuerier, table string) (map[string]bool, error) {
	rows, err := db.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		cols[name] = true
	}
	return cols, rows.Err()
}

func openCodeSessionSupported(db openCodeQuerier, sessionID string) (bool, error) {
	hasSessionMessage, err := probeOpenCodeSessionMessage(db)
	if err != nil {
		return false, err
	}
	if !hasSessionMessage {
		return true, nil
	}

	// Projection-only events such as model switches have no legacy message.
	// User and assistant projections must retain both the turn and its content.
	rows, err := db.Query(`
		SELECT sm.type,
		       sm.data,
		       EXISTS (SELECT 1 FROM message m WHERE m.id = sm.id AND m.session_id = sm.session_id),
		       (SELECT count(*) FROM part p WHERE p.message_id = sm.id AND p.session_id = sm.session_id AND json_extract(p.data, '$.type') = 'text'),
		       (SELECT count(*) FROM part p WHERE p.message_id = sm.id AND p.session_id = sm.session_id AND json_extract(p.data, '$.type') = 'file'),
		       (SELECT count(*) FROM part p WHERE p.message_id = sm.id AND p.session_id = sm.session_id AND json_extract(p.data, '$.type') = 'agent'),
		       (SELECT count(*) FROM part p WHERE p.message_id = sm.id AND p.session_id = sm.session_id AND json_extract(p.data, '$.type') = 'reasoning'),
		       (SELECT count(*) FROM part p WHERE p.message_id = sm.id AND p.session_id = sm.session_id AND json_extract(p.data, '$.type') = 'tool'),
		       (SELECT count(*) FROM part p
		         WHERE p.message_id = sm.id
		           AND p.session_id = sm.session_id
		           AND json_extract(p.data, '$.type') = 'text'
		           AND json_extract(p.data, '$.synthetic') = 1
		           AND (json_extract(p.data, '$.text') LIKE 'Reading MCP resource:%'
		             OR json_extract(p.data, '$.text') LIKE 'Read tool failed to read %'))
		  FROM session_message sm
		 WHERE sm.session_id = ?
		   AND sm.type IN ('user', 'assistant')
		 ORDER BY sm.id`, sessionID)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var projectedType string
		var data []byte
		var hasMessage bool
		var got openCodePartCounts
		if err := rows.Scan(&projectedType, &data, &hasMessage, &got.text, &got.file, &got.agent, &got.reasoning, &got.tool, &got.fileAsText); err != nil {
			return false, err
		}
		want, known := projectedOpenCodePartCounts(projectedType, data)
		if !hasMessage || !known || want.exceeds(got) {
			return false, nil
		}
	}
	return true, rows.Err()
}

type openCodePartCounts struct {
	text       int
	file       int
	agent      int
	reasoning  int
	tool       int
	fileAsText int
}

func projectedOpenCodePartCounts(projectedType string, data []byte) (openCodePartCounts, bool) {
	var counts openCodePartCounts
	switch projectedType {
	case "user":
		if gjson.GetBytes(data, "text").String() != "" {
			counts.text = 1
		}
		counts.file = len(gjson.GetBytes(data, "files").Array())
		counts.agent = len(gjson.GetBytes(data, "agents").Array())
	case "assistant":
		for _, content := range gjson.GetBytes(data, "content").Array() {
			switch content.Get("type").String() {
			case "text":
				counts.text++
			case "reasoning":
				counts.reasoning++
			case "tool":
				counts.tool++
			default:
				return openCodePartCounts{}, false
			}
		}
	default:
		return openCodePartCounts{}, false
	}
	return counts, true
}

func (c openCodePartCounts) exceeds(other openCodePartCounts) bool {
	missingFiles := max(c.file-other.file, 0)
	return missingFiles > other.fileAsText ||
		c.text+missingFiles > other.text ||
		c.agent > other.agent ||
		c.reasoning > other.reasoning ||
		c.tool > other.tool
}

func openOpenCodeDB(path string) (*sql.DB, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve opencode database %s: %w", path, err)
	}
	dsn := "file:" + filepath.ToSlash(abs) + "?mode=ro&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open opencode database %s: %w", path, err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("open opencode database %s: %w", path, err)
	}
	return db, nil
}

type openCodeSession struct {
	ID          string
	ParentID    string
	Directory   string
	Title       string
	Slug        string
	Branch      string
	Agent       string
	Model       []byte
	TimeCreated int64
	TimeUpdated int64
}

func listOpenCodeSessions(db openCodeQuerier) ([]openCodeSession, error) {
	rows, err := db.Query(`
		SELECT s.id,
		       coalesce(s.parent_id, ''),
		       coalesce(s.directory, ''),
		       coalesce(s.title, ''),
		       coalesce(s.slug, ''),
		       coalesce(w.branch, ''),
		       coalesce(s.agent, ''),
		       s.model,
		       s.time_created,
		       s.time_updated
		  FROM session s
		  LEFT JOIN workspace w ON w.id = s.workspace_id
		 ORDER BY s.time_created, s.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []openCodeSession
	for rows.Next() {
		var s openCodeSession
		var model sql.NullString
		if err := rows.Scan(&s.ID, &s.ParentID, &s.Directory, &s.Title, &s.Slug, &s.Branch, &s.Agent, &model, &s.TimeCreated, &s.TimeUpdated); err != nil {
			return nil, err
		}
		if model.Valid && model.String != "" && model.String != "null" {
			s.Model = []byte(model.String)
		}
		if s.ID == "" {
			continue
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

type openCodeMessage struct {
	ID          string
	TimeCreated int64
	Data        []byte
}

type openCodePart struct {
	ID          string
	MessageID   string
	TimeCreated int64
	Data        []byte
}

func materializeOpenCodeSession(db *sql.DB, sess openCodeSession, path string) (bool, error) {
	tx, err := db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	supported, err := openCodeSessionSupported(tx, sess.ID)
	if err != nil || !supported {
		return supported, err
	}
	if !openCodeMaterializeStale(path, sess.TimeUpdated) {
		return true, tx.Commit()
	}
	msgs, err := listOpenCodeMessages(tx, sess.ID)
	if err != nil {
		return false, err
	}
	parts, err := listOpenCodeParts(tx, sess.ID)
	if err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	byMsg := make(map[string][]openCodePart, len(msgs))
	for _, p := range parts {
		byMsg[p.MessageID] = append(byMsg[p.MessageID], p)
	}
	stale := time.Since(time.UnixMilli(sess.TimeUpdated)) >= openCodeSettleWindow
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return false, err
	}
	ok := false
	defer func() {
		f.Close()
		if !ok {
			os.Remove(tmp)
		}
	}()
	if err := writeOpenCodeJSONL(f, sess, msgs, byMsg, stale); err != nil {
		return false, err
	}
	if err := f.Sync(); err != nil {
		return false, err
	}
	if err := f.Close(); err != nil {
		return false, err
	}
	if err := os.Rename(tmp, path); err != nil {
		return false, err
	}
	ok = true
	return true, nil
}

func openCodeMaterializeStale(path string, updated int64) bool {
	f, err := os.Open(path)
	if err != nil {
		return true
	}
	defer f.Close()
	buf := make([]byte, 4096)
	n, err := f.Read(buf)
	if n == 0 || (err != nil && err != io.EOF) {
		return true
	}
	line := buf[:n]
	for i, b := range line {
		if b == '\n' {
			line = line[:i]
			break
		}
	}
	got := gjson.GetBytes(line, "time_updated").Int()
	return got != updated
}

func listOpenCodeMessages(db openCodeQuerier, sessionID string) ([]openCodeMessage, error) {
	rows, err := db.Query(`
		SELECT id, time_created, data
		  FROM message
		 WHERE session_id = ?
		 ORDER BY time_created, id`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []openCodeMessage
	for rows.Next() {
		var m openCodeMessage
		if err := rows.Scan(&m.ID, &m.TimeCreated, &m.Data); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func listOpenCodeParts(db openCodeQuerier, sessionID string) ([]openCodePart, error) {
	rows, err := db.Query(`
		SELECT id, message_id, time_created, data
		  FROM part
		 WHERE session_id = ?
		 ORDER BY time_created, id`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []openCodePart
	for rows.Next() {
		var p openCodePart
		if err := rows.Scan(&p.ID, &p.MessageID, &p.TimeCreated, &p.Data); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

type openCodeSessionLine struct {
	Type        string          `json:"type"`
	ID          string          `json:"id"`
	Directory   string          `json:"directory"`
	ParentID    string          `json:"parent_id,omitempty"`
	Title       string          `json:"title,omitempty"`
	Slug        string          `json:"slug,omitempty"`
	Branch      string          `json:"branch,omitempty"`
	Agent       string          `json:"agent,omitempty"`
	Model       json.RawMessage `json:"model,omitempty"`
	TimeCreated int64           `json:"time_created"`
	TimeUpdated int64           `json:"time_updated"`
}

type openCodeMessageLine struct {
	Type        string             `json:"type"`
	ID          string             `json:"id"`
	TimeCreated int64              `json:"time_created"`
	Data        json.RawMessage    `json:"data"`
	Parts       []openCodePartLine `json:"parts"`
}

type openCodePartLine struct {
	ID          string          `json:"id"`
	TimeCreated int64           `json:"time_created"`
	Data        json.RawMessage `json:"data"`
}

func writeOpenCodeJSONL(w io.Writer, sess openCodeSession, msgs []openCodeMessage, byMsg map[string][]openCodePart, sessionStale bool) error {
	enc := json.NewEncoder(w)
	header := openCodeSessionLine{
		Type:        "session",
		ID:          sess.ID,
		Directory:   sess.Directory,
		ParentID:    sess.ParentID,
		Title:       sess.Title,
		Slug:        sess.Slug,
		Branch:      sess.Branch,
		Agent:       sess.Agent,
		Model:       json.RawMessage(sess.Model),
		TimeCreated: sess.TimeCreated,
		TimeUpdated: sess.TimeUpdated,
	}
	if err := enc.Encode(header); err != nil {
		return err
	}
	for _, m := range msgs {
		if !openCodeMessageReady(m.Data, sessionStale) {
			continue
		}
		line := openCodeMessageLine{
			Type:        "message",
			ID:          m.ID,
			TimeCreated: m.TimeCreated,
			Data:        json.RawMessage(m.Data),
			Parts:       make([]openCodePartLine, 0, len(byMsg[m.ID])),
		}
		for _, p := range byMsg[m.ID] {
			line.Parts = append(line.Parts, openCodePartLine{
				ID:          p.ID,
				TimeCreated: p.TimeCreated,
				Data:        json.RawMessage(p.Data),
			})
		}
		if err := enc.Encode(line); err != nil {
			return err
		}
	}
	return nil
}

func openCodeMessageReady(data []byte, sessionStale bool) bool {
	if gjson.GetBytes(data, "role").String() != "assistant" {
		return true
	}
	if gjson.GetBytes(data, "time.completed").Int() > 0 {
		return true
	}
	if gjson.GetBytes(data, "error").Exists() {
		return true
	}
	return sessionStale
}
