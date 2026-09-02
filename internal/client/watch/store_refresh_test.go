package watch

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/jssblck/akari/internal/client/discover"
)

func TestStoreEventsCoalesceIntoOneDiscoveryPass(t *testing.T) {
	root := t.TempDir()
	storePaths := []string{
		filepath.Join(root, "opencode.db"),
		filepath.Join(root, "opencode.db-wal"),
		filepath.Join(root, "opencode.db-shm"),
	}
	for _, path := range storePaths {
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fsw.Close() })

	const debounce = 50 * time.Millisecond
	w := New(
		[]discover.Root{{Agent: "opencode", Dir: root}},
		nil,
		Options{Debounce: debounce, StoreRefresh: time.Second},
	)
	known := map[discover.File]fileMeta{}
	pending := map[discover.File]time.Time{}
	sr := &storeRefresh{}
	discoveries := 0
	w.discoverFunc = func([]discover.Root, discover.Excluder) ([]discover.File, []string, error) {
		discoveries++
		return nil, nil, nil
	}

	const events = 12
	for i := range events {
		w.handleEvent(fsw, fsnotify.Event{
			Name: storePaths[i%len(storePaths)],
			Op:   fsnotify.Write,
		}, known, pending, sr)
	}
	if discoveries != 0 {
		t.Fatalf("store burst ran %d inline discovery pass(es), want 0", discoveries)
	}
	if !sr.pending {
		t.Fatal("store burst did not schedule a refresh")
	}
	if len(pending) != 0 {
		t.Fatalf("store burst populated %d pending file(s) before discovery", len(pending))
	}

	mark := func(discover.File) {}
	flushTick(sr.settleAt.Add(-time.Nanosecond), debounce, sr, known, pending, w.discover, mark)
	flushTick(sr.settleAt, debounce, sr, known, pending, w.discover, mark)
	flushTick(sr.settleAt.Add(debounce), debounce, sr, known, pending, w.discover, mark)

	if discoveries != 1 {
		t.Fatalf("store burst caused %d discovery passes, want 1", discoveries)
	}
}

func TestStoreRefreshForcesContinuousWritesAtDeadline(t *testing.T) {
	t0 := time.Unix(1_800_000_000, 0)
	const (
		debounce = 2 * time.Second
		maxWait  = 5 * time.Second
	)
	sr := &storeRefresh{}
	for i := range 5 {
		sr.schedule(t0.Add(time.Duration(i)*time.Second), debounce, maxWait)
	}

	wantForceAt := t0.Add(maxWait)
	if !sr.forceAt.Equal(wantForceAt) {
		t.Fatalf("force deadline = %s, want %s", sr.forceAt, wantForceAt)
	}
	if !sr.settleAt.After(sr.forceAt) {
		t.Fatalf("settle deadline = %s, want it after force deadline %s", sr.settleAt, sr.forceAt)
	}

	discoveries := 0
	discoverFiles := func() []discover.File {
		discoveries++
		return nil
	}
	known := map[discover.File]fileMeta{}
	pending := map[discover.File]time.Time{}
	mark := func(discover.File) {}
	flushTick(sr.forceAt.Add(-time.Nanosecond), debounce, sr, known, pending, discoverFiles, mark)
	if discoveries != 0 {
		t.Fatalf("discovery ran before force deadline: %d pass(es)", discoveries)
	}
	flushTick(sr.forceAt, debounce, sr, known, pending, discoverFiles, mark)
	if discoveries != 1 {
		t.Fatalf("discovery passes at force deadline = %d, want 1", discoveries)
	}
}

func TestCoalescedDiscoveryQueuesFreshPendingDeadline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	writeSession(t, path)
	file := discover.File{Agent: "opencode", Root: filepath.Dir(path), Path: path}

	const debounce = 500 * time.Millisecond
	t0 := time.Unix(1_800_000_000, 0)
	sr := &storeRefresh{}
	sr.schedule(t0, debounce, 5*time.Second)
	refreshTick := sr.settleAt
	known := map[discover.File]fileMeta{}
	pending := map[discover.File]time.Time{}
	var marked []discover.File

	flushTick(
		refreshTick,
		debounce,
		sr,
		known,
		pending,
		func() []discover.File { return []discover.File{file} },
		func(f discover.File) { marked = append(marked, f) },
	)

	wantDeadline := refreshTick.Add(debounce)
	if got := pending[file]; !got.Equal(wantDeadline) {
		t.Fatalf("pending deadline = %s, want fresh deadline %s", got, wantDeadline)
	}
	if _, ok := known[file]; !ok {
		t.Fatal("coalesced discovery did not add the session to the poll baseline")
	}
	if len(marked) != 0 {
		t.Fatalf("coalesced discovery marked %d file(s) on its discovery tick, want 0", len(marked))
	}

	flushTick(
		wantDeadline,
		debounce,
		sr,
		known,
		pending,
		func() []discover.File { t.Fatal("discovery repeated without another store event"); return nil },
		func(f discover.File) { marked = append(marked, f) },
	)
	if len(marked) != 1 || marked[0] != file {
		t.Fatalf("marked files = %v, want [%v]", marked, file)
	}
	if len(pending) != 0 {
		t.Fatalf("pending still contains %d file(s) after its later flush tick", len(pending))
	}
}

func TestNonStoreEventStillQueuesFileDirectly(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "project", "session.jsonl")
	writeSession(t, path)

	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fsw.Close() })

	const debounce = 250 * time.Millisecond
	w := New(
		[]discover.Root{{Agent: "claude", Dir: root}},
		nil,
		Options{Debounce: debounce, StoreRefresh: 5 * time.Second},
	)
	known := map[discover.File]fileMeta{}
	pending := map[discover.File]time.Time{}
	sr := &storeRefresh{}
	before := time.Now()
	w.handleEvent(fsw, fsnotify.Event{Name: path, Op: fsnotify.Write}, known, pending, sr)
	after := time.Now()

	file := discover.File{Agent: "claude", Root: root, Path: path}
	deadline, ok := pending[file]
	if !ok {
		t.Fatal("non-store event was not queued directly")
	}
	if deadline.Before(before.Add(debounce)) || deadline.After(after.Add(debounce)) {
		t.Fatalf("pending deadline = %s, want between %s and %s", deadline, before.Add(debounce), after.Add(debounce))
	}
	if _, ok := known[file]; !ok {
		t.Fatal("non-store event did not add the file to the poll baseline")
	}
	if sr.pending {
		t.Fatal("non-store event scheduled a store refresh")
	}
}
