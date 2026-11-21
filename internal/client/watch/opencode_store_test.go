package watch

import (
	"bytes"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/jssblck/akari/internal/client/discover"
	_ "modernc.org/sqlite"
)

func TestOpenCodeReadOnlySQLiteSidecarDoesNotRefresh(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "opencode.db")
	createClosedWALStore(t, path)

	// Observe shared-memory writes explicitly so this regression does not depend
	// on whether the OS directory watch also reports writes to existing files.
	fsw, err := fsnotify.NewBufferedWatcher(128)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fsw.Close() })
	for _, watched := range []string{root, path + "-shm"} {
		if err := fsw.Add(watched); err != nil {
			t.Fatal(err)
		}
	}

	w := New([]discover.Root{{Agent: "opencode", Dir: root}}, nil, Options{})
	known := map[discover.File]fileMeta{}
	pending := map[discover.File]time.Time{}
	sr := &storeRefresh{}
	before := readStoreFiles(t, path)
	metadata := map[string]fileMeta{}
	for name := range before {
		meta, ok := statMeta(name)
		if !ok {
			t.Fatalf("stat %s before read-only query", filepath.Base(name))
		}
		metadata[name] = meta
	}

	// Match discovery's real driver and mode=ro DSN. Each query has its own
	// connection lifecycle, with no persistent writer keeping the WAL mapped.
	for range 3 {
		db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro&_pragma=busy_timeout(5000)")
		if err != nil {
			t.Fatal(err)
		}
		var value string
		err = db.QueryRow("SELECT value FROM fixture").Scan(&value)
		closeErr := db.Close()
		if err != nil {
			t.Fatal(err)
		}
		if closeErr != nil {
			t.Fatal(closeErr)
		}
		if value != "synthetic" {
			t.Fatalf("read-only query = %q, want synthetic", value)
		}
	}

	// Fail rather than skip if the fixture stops reproducing real -shm activity.
	// Feed the observed OS event through the same handler as the watch loop.
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	observed := false
	for !observed {
		select {
		case ev := <-fsw.Events:
			w.handleEvent(fsw, ev, known, pending, sr)
			if ev.Name == path+"-shm" && ev.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename|fsnotify.Remove) != 0 {
				observed = true
				t.Logf("read-only SQLite activity: %s on %s", ev.Op, filepath.Base(ev.Name))
			}
		case err := <-fsw.Errors:
			t.Fatalf("observe read-only SQLite: %v", err)
		case <-timer.C:
			t.Fatal("read-only SQLite reopen produced no observed -shm activity")
		}
	}
	for name, want := range before {
		got, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("read-only SQLite changed %s", filepath.Base(name))
		}
		if meta, ok := statMeta(name); !ok || meta != metadata[name] {
			t.Fatalf("read-only SQLite changed metadata for %s", filepath.Base(name))
		}
	}
	if sr.pending {
		t.Fatal("read-only SQLite -shm activity scheduled a store refresh")
	}
	if len(pending) != 0 {
		t.Fatalf("read-only SQLite queued %d session files", len(pending))
	}
}

func TestOpenCodeRecreatedStoreFilesKeepWriteWatch(t *testing.T) {
	for _, suffix := range []string{"", "-wal"} {
		t.Run("opencode.db"+suffix, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "opencode.db")
			createClosedWALStore(t, path)
			recreated := path + suffix
			data, err := os.ReadFile(recreated)
			if err != nil {
				t.Fatal(err)
			}
			fsw, err := fsnotify.NewBufferedWatcher(128)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = fsw.Close() })
			w := New([]discover.Root{{Agent: "opencode", Dir: root}}, nil, Options{})
			if err := w.addRecursive(fsw, w.roots[0]); err != nil {
				t.Fatal(err)
			}
			known := map[discover.File]fileMeta{}
			pending := map[discover.File]time.Time{}
			sr := &storeRefresh{}
			handle := func(ev fsnotify.Event) { w.handleEvent(fsw, ev, known, pending, sr) }
			if err := os.Remove(recreated); err != nil {
				t.Fatal(err)
			}
			waitForStoreEvent(t, fsw, recreated, fsnotify.Remove, handle)
			if !sr.pending {
				t.Fatal("store removal did not schedule a refresh")
			}
			*sr = storeRefresh{}
			if err := os.WriteFile(recreated, data, 0o600); err != nil {
				t.Fatal(err)
			}
			waitForStoreEvent(t, fsw, recreated, fsnotify.Create, handle)
			if !sr.pending {
				t.Fatal("store recreation did not schedule a refresh")
			}

			// Finish consuming recreation's Write events before testing a later
			// write, which needs the watch on the new inode under kqueue.
			quiet := time.NewTimer(25 * time.Millisecond)
			defer quiet.Stop()
			draining := true
			for draining {
				select {
				case ev := <-fsw.Events:
					handle(ev)
					quiet.Reset(25 * time.Millisecond)
				case err := <-fsw.Errors:
					t.Fatalf("observe recreated store: %v", err)
				case <-quiet.C:
					draining = false
				}
			}
			*sr = storeRefresh{}
			// Rewrite identical fixture bytes: the contents stay valid, but the
			// OS must emit a fresh write for the recreated store file.
			if err := os.WriteFile(recreated, data, 0o600); err != nil {
				t.Fatal(err)
			}
			waitForStoreEvent(t, fsw, recreated, fsnotify.Write, handle)
			if !sr.pending {
				t.Fatal("later write to recreated store did not schedule a refresh")
			}
		})
	}
}

func waitForStoreEvent(t *testing.T, fsw *fsnotify.Watcher, path string, op fsnotify.Op, handle func(fsnotify.Event)) {
	t.Helper()
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	for {
		select {
		case ev := <-fsw.Events:
			handle(ev)
			if ev.Name == path && ev.Op&op != 0 {
				t.Logf("store activity: %s on %s", ev.Op, filepath.Base(ev.Name))
				return
			}
		case err := <-fsw.Errors:
			t.Fatalf("observe store: %v", err)
		case <-timer.C:
			t.Fatalf("no observed %s event for %s", op, filepath.Base(path))
		}
	}
}

func TestOpenCodeSQLiteChangesScheduleRefresh(t *testing.T) {
	for _, tc := range []struct {
		name   string
		suffix string
		op     fsnotify.Op
		change func(*testing.T, *sql.DB, string)
	}{
		{
			name: "WAL transaction", suffix: "-wal", op: fsnotify.Write,
			change: func(t *testing.T, db *sql.DB, _ string) {
				if _, err := db.Exec("INSERT INTO fixture VALUES ('updated')"); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "database checkpoint", op: fsnotify.Write,
			change: func(t *testing.T, db *sql.DB, _ string) {
				var busy, frames, checkpointed int
				if err := db.QueryRow("PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &frames, &checkpointed); err != nil {
					t.Fatal(err)
				}
				if busy != 0 {
					t.Fatalf("checkpoint remained busy: %d", busy)
				}
			},
		},
		{
			name: "database replacement", op: fsnotify.Remove | fsnotify.Rename | fsnotify.Create,
			change: func(t *testing.T, _ *sql.DB, path string) {
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				replacement := path + ".replacement"
				if err := os.WriteFile(replacement, data, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(replacement, path); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "WAL removal", suffix: "-wal", op: fsnotify.Remove,
			change: func(t *testing.T, _ *sql.DB, path string) {
				if err := os.Remove(path + "-wal"); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "opencode.db")
			createClosedWALStore(t, path)
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			db.SetMaxOpenConns(1)
			t.Cleanup(func() { _ = db.Close() })

			fsw, err := fsnotify.NewBufferedWatcher(128)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = fsw.Close() })
			w := New([]discover.Root{{Agent: "opencode", Dir: root}}, nil, Options{})
			if err := w.addRecursive(fsw, w.roots[0]); err != nil {
				t.Fatal(err)
			}
			known := map[discover.File]fileMeta{}
			pending := map[discover.File]time.Time{}
			sr := &storeRefresh{}
			tc.change(t, db, path)

			timer := time.NewTimer(3 * time.Second)
			defer timer.Stop()
			for {
				select {
				case ev := <-fsw.Events:
					if ev.Name != path+tc.suffix || ev.Op&tc.op == 0 {
						continue
					}
					t.Logf("store change: %s on %s", ev.Op, filepath.Base(ev.Name))
					w.handleEvent(fsw, ev, known, pending, sr)
					if !sr.pending {
						t.Fatalf("real store event %s on %s did not schedule a refresh", ev.Op, filepath.Base(ev.Name))
					}
					return
				case err := <-fsw.Errors:
					t.Fatalf("observe store change: %v", err)
				case <-timer.C:
					t.Fatalf("store change produced no observed %s event for %s", tc.op, filepath.Base(path+tc.suffix))
				}
			}
		})
	}
}

// createClosedWALStore snapshots a synthetic WAL database before the final
// writer closes, then restores that snapshot after SQLite cleans its sidecars.
// This reproduces a dormant WAL store without holding a writer connection open.
func createClosedWALStore(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	for _, stmt := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA wal_autocheckpoint=0",
		"CREATE TABLE fixture (value TEXT NOT NULL)",
		"INSERT INTO fixture VALUES ('synthetic')",
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	files := readStoreFiles(t, path)
	shm, err := os.ReadFile(path + "-shm")
	if err != nil {
		t.Fatal(err)
	}
	files[path+"-shm"] = shm
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	for name, data := range files {
		if err := os.WriteFile(name, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func readStoreFiles(t *testing.T, path string) map[string][]byte {
	t.Helper()
	files := map[string][]byte{}
	for _, name := range []string{path, path + "-wal"} {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		files[name] = data
	}
	return files
}
