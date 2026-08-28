package discover

import (
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

	sessions, err := listOpenCodeSessions(db)
	if err != nil {
		return nil, fmt.Errorf("list opencode sessions in %s: %w", dbPath, err)
	}
	out := make([]File, 0, len(sessions))
	for _, sess := range sessions {
		path := filepath.Join(cacheDir, sess.ID+".jsonl")
		if ex.Excluded(path) {
			continue
		}
		if err := materializeOpenCodeSession(db, sess, path); err != nil {
			return out, fmt.Errorf("materialize opencode session %s: %w", sess.ID, err)
		}
		out = append(out, File{Agent: "opencode", Root: walkDir, Path: path})
	}
	return out, nil
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

func listOpenCodeSessions(db *sql.DB) ([]openCodeSession, error) {
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

func materializeOpenCodeSession(db *sql.DB, sess openCodeSession, path string) error {
	if !openCodeMaterializeStale(path, sess.TimeUpdated) {
		return nil
	}
	msgs, err := listOpenCodeMessages(db, sess.ID)
	if err != nil {
		return err
	}
	parts, err := listOpenCodeParts(db, sess.ID)
	if err != nil {
		return err
	}
	byMsg := make(map[string][]openCodePart, len(msgs))
	for _, p := range parts {
		byMsg[p.MessageID] = append(byMsg[p.MessageID], p)
	}
	stale := time.Since(time.UnixMilli(sess.TimeUpdated)) >= openCodeSettleWindow
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		f.Close()
		if !ok {
			os.Remove(tmp)
		}
	}()
	if err := writeOpenCodeJSONL(f, sess, msgs, byMsg, stale); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	ok = true
	return nil
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

func listOpenCodeMessages(db *sql.DB, sessionID string) ([]openCodeMessage, error) {
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

func listOpenCodeParts(db *sql.DB, sessionID string) ([]openCodePart, error) {
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
