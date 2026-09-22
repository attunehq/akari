package watch

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/jssblck/akari/internal/client/discover"
	"github.com/jssblck/akari/internal/client/syncer"
	"github.com/jssblck/akari/internal/client/upload"
)

// vanishedHeaderError is what the worker sees for a transcript deleted between
// discovery and upload: resolve wraps the *fs.PathError that os.Lstat returned.
func vanishedHeaderError(path string) error {
	return fmt.Errorf("read session header: %w", &fs.PathError{
		Op:   "lstat",
		Path: path,
		Err:  syscall.ENOENT,
	})
}

func TestPressureFailureClassification(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "dns", err: &net.DNSError{Err: "no such host", Name: "akari.example"}, want: true},
		{name: "dial", err: &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}, want: true},
		{
			name: "upload transport",
			err: &url.Error{
				Op:  "Post",
				URL: "https://akari.example/api/v1/ingest/session",
				Err: &net.OpError{Op: "read", Err: syscall.ECONNRESET},
			},
			want: true,
		},
		{name: "process capacity", err: fmt.Errorf("start git: %w", syscall.EAGAIN), want: true},
		{name: "descriptor capacity", err: fmt.Errorf("open: %w", syscall.EMFILE), want: true},
		{name: "server status", err: fmt.Errorf("announce: %w", upload.ErrRetryableStatus), want: true},
		{name: "canceled", err: context.Canceled, want: false},
		{name: "vanished session", err: vanishedHeaderError("/gone/session.jsonl"), want: false},
		{
			name: "unreadable session",
			err:  fmt.Errorf("read session header: %w", &fs.PathError{Op: "open", Path: "/x", Err: syscall.EACCES}),
			want: false,
		},
		{name: "opaque", err: errors.New("read session header"), want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := pressureFailure(test.err); got != test.want {
				t.Fatalf("pressureFailure(%v) = %v, want %v", test.err, got, test.want)
			}
		})
	}
}

// The synthetic *fs.PathError above only reproduces the bug for as long as the
// standard library keeps its method set as it is. syscall.Errno satisfying
// net.Error is what made a missing file look like a dropped connection, so take
// the error from a real syscall too and let the kernel supply the shape.
func TestPressureFailureOnRealMissingFile(t *testing.T) {
	_, err := os.Lstat(filepath.Join(t.TempDir(), "never-written.jsonl"))
	if err == nil {
		t.Fatal("lstat of a missing path returned no error")
	}
	if pressureFailure(fmt.Errorf("read session header: %w", err)) {
		t.Fatalf("a missing transcript was classified as resource pressure: %v", err)
	}
}

func TestPressureBackoffDefault(t *testing.T) {
	if got := (Options{}).withDefaults().PressureBackoff; got != 30*time.Second {
		t.Fatalf("pressure backoff = %s, want 30s", got)
	}
}

func TestWorkerBacksOffAndRetriesAfterPressure(t *testing.T) {
	const delay = 50 * time.Millisecond
	attempted := make(chan time.Time, 2)
	attempt := 0
	file := discover.File{Agent: "claude", Path: "session.jsonl"}
	w := &Watcher{
		sync: func(context.Context, discover.File) syncer.Result {
			attempt++
			attempted <- time.Now()
			if attempt == 1 {
				return syncer.Result{File: file, Err: fmt.Errorf("start git: %w", syscall.EAGAIN)}
			}
			return syncer.Result{File: file}
		},
		opt: Options{PressureBackoff: delay, Logf: func(string, ...any) {}},
	}
	rs := &runState{w: w, dirty: map[discover.File]struct{}{}, wake: make(chan struct{}, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		rs.worker(ctx)
		close(done)
	}()
	rs.mark(file)

	var first time.Time
	select {
	case first = <-attempted:
	case <-time.After(time.Second):
		t.Fatal("worker did not start the pressured file")
	}
	select {
	case second := <-attempted:
		if elapsed := second.Sub(first); elapsed < delay {
			t.Fatalf("pressure retry started after %s, want at least %s", elapsed, delay)
		}
	case <-time.After(time.Second):
		t.Fatal("worker did not retry the pressured file")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("worker did not stop")
	}
}

// A deleted transcript is never coming back, so the worker must drop it. Keeping
// it costs more than one wasted attempt: a re-marked file is retried forever, and
// each retry pauses every other file behind the pressure backoff.
func TestWorkerDropsVanishedFile(t *testing.T) {
	const backoff = 10 * time.Second // long enough that a pause would fail the test
	attempted := make(chan struct{}, 4)
	file := discover.File{Agent: "claude", Path: "/gone/session.jsonl"}
	w := &Watcher{
		sync: func(context.Context, discover.File) syncer.Result {
			attempted <- struct{}{}
			return syncer.Result{File: file, Err: vanishedHeaderError(file.Path)}
		},
		opt: Options{PressureBackoff: backoff, Logf: func(string, ...any) {}},
	}
	rs := &runState{w: w, dirty: map[discover.File]struct{}{}, wake: make(chan struct{}, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		rs.worker(ctx)
		close(done)
	}()
	rs.mark(file)

	select {
	case <-attempted:
	case <-time.After(time.Second):
		t.Fatal("worker did not attempt the vanished file")
	}
	select {
	case <-attempted:
		t.Fatal("worker retried a vanished file instead of dropping it")
	case <-time.After(100 * time.Millisecond):
	}

	rs.mu.Lock()
	remaining := len(rs.dirty)
	rs.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("vanished file left %d entries in the dirty set, want 0", remaining)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("worker did not stop")
	}
}
