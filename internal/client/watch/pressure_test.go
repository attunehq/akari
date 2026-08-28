package watch

import (
	"context"
	"errors"
	"fmt"
	"net"
	"syscall"
	"testing"
	"time"

	"github.com/jssblck/akari/internal/client/discover"
	"github.com/jssblck/akari/internal/client/syncer"
	"github.com/jssblck/akari/internal/client/upload"
)

func TestPressureFailureClassification(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "dns", err: &net.DNSError{Err: "no such host", Name: "akari.example"}, want: true},
		{name: "process capacity", err: fmt.Errorf("start git: %w", syscall.EAGAIN), want: true},
		{name: "server status", err: fmt.Errorf("announce: %w", upload.ErrRetryableStatus), want: true},
		{name: "canceled", err: context.Canceled, want: false},
		{name: "file", err: errors.New("read session header"), want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := pressureFailure(test.err); got != test.want {
				t.Fatalf("pressureFailure(%v) = %v, want %v", test.err, got, test.want)
			}
		})
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
