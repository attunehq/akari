package watch

import (
	"context"
	"errors"
	"net"
	"syscall"
	"time"

	"github.com/jssblck/akari/internal/client/upload"
)

// exhaustionErrnos are the kernel limits a later attempt can plausibly clear.
// EAGAIN is a process table with no room to fork Git; the descriptor and memory
// limits are the same story for a watcher tracking thousands of files. Backing
// off gives the machine room, so these stay pressure.
//
// They are listed rather than inferred because the net.Error match below used to
// classify them by accident, and an accident is not a contract.
var exhaustionErrnos = [...]syscall.Errno{
	syscall.EAGAIN,
	syscall.EMFILE,
	syscall.ENFILE,
	syscall.ENOMEM,
}

// pressureFailure reports whether err is worth pausing the whole watcher for.
// The worker re-marks a pressured file, so answering yes to a condition that
// cannot change puts the watcher in a loop it never leaves.
func pressureFailure(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, upload.ErrRetryableStatus) {
		return true
	}
	for _, errno := range exhaustionErrnos {
		if errors.Is(err, errno) {
			return true
		}
	}
	return networkFailure(err)
}

// networkFailure reports whether err came from the transport rather than from
// the filesystem.
//
// Matching the net.Error interface alone does not answer that. syscall.Errno
// implements both Timeout() and Temporary(), so every errno satisfies net.Error
// on its own, and errors.As walks straight past *fs.PathError — which has
// Timeout() but no Temporary() — to land on the errno underneath. An ENOENT from
// a transcript that was deleted between discovery and upload therefore matched
// as firmly as a dropped connection.
//
// That is not a theoretical mismatch. On one macOS host it held the watcher in a
// 30-second backoff loop over 938 session files from removed worktrees: each
// attempt failed with ENOENT, was classified as pressure, was re-marked into the
// dirty set, and slept. The set could never drain, and the loop wrote ~2.3MB a
// day of identical errors into an unrotated log for eleven days.
//
// A genuine transport failure always presents a net package type ahead of any
// errno — *url.Error from the upload client, with *net.OpError or *net.DNSError
// beneath it — so a match that lands on a bare errno is the filesystem talking,
// not the network.
func networkFailure(err error) bool {
	var networkError net.Error
	if !errors.As(err, &networkError) {
		return false
	}
	_, bareErrno := networkError.(syscall.Errno)
	return !bareErrno
}

func waitForPressureBackoff(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}
