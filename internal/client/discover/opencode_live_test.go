package discover

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jssblck/akari/internal/parser"
)

// TestLiveOpenCodeSessionsParse materializes this machine's OpenCode database
// and parses every session through the reducer. It is the coverage net for
// shapes the synthetic fixture does not invent: subagents, compaction, errors,
// encrypted reasoning, MCP tools. Skips when the default database is absent.
func TestLiveOpenCodeSessionsParse(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	dataDir := filepath.Join(home, ".local", "share", "opencode")
	if _, err := os.Stat(filepath.Join(dataDir, "opencode.db")); err != nil {
		t.Skip("no local OpenCode database")
	}
	cache := t.TempDir()
	prev := openCodeCacheDir
	openCodeCacheDir = func(string) (string, error) { return cache, nil }
	t.Cleanup(func() { openCodeCacheDir = prev })

	files, _, err := Discover([]Root{{Agent: "opencode", Dir: dataDir, Optional: true}}, Excluder{})
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(files) == 0 {
		t.Skip("OpenCode database has no sessions")
	}
	parsed := 0
	var withTools, withThinking, withParent, withUsage int
	for _, f := range files {
		raw, err := os.ReadFile(f.Path)
		if err != nil {
			t.Fatalf("read %s: %v", f.Path, err)
		}
		s, err := parser.Parse(parser.AgentOpenCode, raw)
		if err != nil {
			t.Fatalf("parse %s: %v", f.Path, err)
		}
		parsed++
		if len(s.ToolCalls) > 0 {
			withTools++
		}
		for _, m := range s.Messages {
			if m.HasThinking {
				withThinking++
				break
			}
		}
		if s.Identity.ParentSourceID != "" {
			withParent++
		}
		if len(s.UsageEvent) > 0 {
			withUsage++
		}
	}
	t.Logf("parsed %d OpenCode sessions (tools=%d thinking=%d parent=%d usage=%d)",
		parsed, withTools, withThinking, withParent, withUsage)
	if parsed == 0 {
		t.Fatal("no sessions parsed")
	}
}
