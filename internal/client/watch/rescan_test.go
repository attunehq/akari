package watch

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jssblck/akari/internal/client/discover"
)

func TestMarkDiscoveredChangesQueuesOnlyNewOrChangedFiles(t *testing.T) {
	dir := t.TempDir()
	existing := discover.File{Agent: "claude", Root: dir, Path: filepath.Join(dir, "existing.jsonl")}
	writeSession(t, existing.Path)
	initial, ok := statMeta(existing.Path)
	if !ok {
		t.Fatal("stat existing session")
	}
	known := map[discover.File]fileMeta{existing: initial}
	var marked []discover.File
	mark := func(f discover.File) { marked = append(marked, f) }

	markDiscoveredChanges(known, []discover.File{existing}, mark)
	if len(marked) != 0 {
		t.Fatalf("unchanged rescan marked %v", marked)
	}

	f, err := os.OpenFile(existing.Path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := f.WriteString("changed\n")
	closeErr := f.Close()
	if writeErr != nil {
		t.Fatal(writeErr)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	markDiscoveredChanges(known, []discover.File{existing}, mark)
	if len(marked) != 1 || marked[0] != existing {
		t.Fatalf("changed rescan marked %v, want %v", marked, existing)
	}

	marked = nil
	newFile := discover.File{Agent: "claude", Root: dir, Path: filepath.Join(dir, "new.jsonl")}
	writeSession(t, newFile.Path)
	markDiscoveredChanges(known, []discover.File{existing, newFile}, mark)
	if len(marked) != 1 || marked[0] != newFile {
		t.Fatalf("new-file rescan marked %v, want %v", marked, newFile)
	}
}
