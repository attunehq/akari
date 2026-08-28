package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/jssblck/akari/internal/selfupdate"
)

// restartAfterUpdateError tells the daemon loop to re-exec onto path after it
// has released the lock and closed the control socket. path is the directory
// entry Replace just wrote; os.Executable after a replace can still name the
// old inode.
type restartAfterUpdateError struct{ path string }

func (e restartAfterUpdateError) Error() string {
	return "akari binary updated"
}

func restartPath(err error) (string, bool) {
	var r restartAfterUpdateError
	if errors.As(err, &r) && r.path != "" {
		return r.path, true
	}
	return "", false
}

// shouldApplyDaemonUpdate is the unattended-update policy: only a comparable
// release that is behind latest is replaced. Development builds (commit SHAs,
// "dev") are left alone so a local build is not overwritten by a release.
func shouldApplyDaemonUpdate(current, latest string) bool {
	upToDate, comparable := selfupdate.UpToDate(current, latest)
	return comparable && !upToDate
}

type binaryReplacer func(ctx context.Context, c *selfupdate.Client, latest string) (string, error)

func applyDaemonUpdate(ctx context.Context, c *selfupdate.Client, current string, replace binaryReplacer) (path string, err error) {
	latest, err := c.LatestTag(ctx)
	if err != nil {
		return "", fmt.Errorf("resolve the latest release: %w", err)
	}
	if !shouldApplyDaemonUpdate(current, latest) {
		return "", nil
	}
	path, err = replace(ctx, c, latest)
	if err != nil {
		return "", err
	}
	if path == "" {
		return "", fmt.Errorf("updated binary path is empty")
	}
	return path, nil
}

// runDaemonCycle runs akari update, then akari sync. A failed update is logged
// and the sync still runs; a cancelled update stops the pass. A successful
// replace returns restartAfterUpdateError so the process can re-exec before
// syncing on the old image.
func runDaemonCycle(ctx context.Context, c *selfupdate.Client, current string, replace binaryReplacer, sync func(context.Context) error, logf func(string, ...any)) error {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	path, err := applyDaemonUpdate(ctx, c, current, replace)
	if err != nil {
		if ctx.Err() != nil {
			return err
		}
		logf("akari daemon: update: %v", err)
	} else if path != "" {
		logf("akari daemon: updated from %s; restarting", current)
		return restartAfterUpdateError{path: path}
	}
	return sync(ctx)
}
