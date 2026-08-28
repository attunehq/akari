package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"time"

	"github.com/jssblck/akari/internal/client/daemon"
	"github.com/jssblck/akari/internal/config"
)

// daemonSyncInterval is the pause after each completed sync pass. A pass that
// overruns this bound delays the next start rather than overlapping.
const daemonSyncInterval = 10 * time.Minute

// runDaemonLoop is the detached worker behind `akari daemon start` and the
// macOS login agent. It holds the single-instance lock, runs `akari sync` with
// the same time limit as a one-shot sync, then waits daemonSyncInterval and
// repeats until cancelled.
func runDaemonLoop(ctx context.Context, args []string) (runErr error) {
	fs := flag.NewFlagSet("daemon run", flag.ContinueOnError)
	configPath := fs.String("config", "", "config file path (default: platform config dir)")
	daemonLogPath := fs.String("daemon-log", "", "internal: log file path used when run as a detached daemon process")
	if err := fs.Parse(args); err != nil {
		return err
	}

	paths, err := daemon.DefaultPaths()
	if err != nil {
		return err
	}
	lock, err := daemon.Acquire(paths.Pidfile)
	if err != nil {
		return err
	}
	defer lock.Release()

	logf := log.Printf
	if *daemonLogPath != "" {
		output, err := daemon.OpenLog(*daemonLogPath)
		if err != nil {
			return err
		}
		logger := log.New(output, "", log.LstdFlags)
		logf = logger.Printf
		restore, err := redirectStdio(output)
		if err != nil {
			_ = output.Close()
			return err
		}
		defer func() {
			restore()
			if runErr != nil {
				if _, err := fmt.Fprintf(output, "akari: %v\n", runErr); err != nil {
					runErr = errors.Join(runErr, fmt.Errorf("write daemon startup error: %w", err))
				}
			}
			if err := output.Close(); err != nil {
				runErr = errors.Join(runErr, err)
			}
		}()
	}

	ctx, stopControl, err := lock.ShutdownContext(ctx)
	if err != nil {
		return err
	}
	defer stopControl()

	if _, err := config.LoadClient(*configPath); err != nil {
		return err
	}

	syncArgs := []string{"--time-limit", defaultTimeLimit.String()}
	if *configPath != "" {
		syncArgs = append(syncArgs, "--config", *configPath)
	}
	logf("akari daemon: syncing every %s; press Ctrl-C to stop", daemonSyncInterval)
	return runSyncInterval(ctx, daemonSyncInterval, func(ctx context.Context) error {
		return runSync(ctx, syncArgs)
	}, logf)
}

func runSyncInterval(ctx context.Context, interval time.Duration, pass func(context.Context) error, logf func(string, ...any)) error {
	if interval <= 0 {
		return fmt.Errorf("sync interval must be positive")
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	for {
		if ctx.Err() != nil {
			logf("akari daemon: stopped")
			return nil
		}
		logf("akari daemon: starting sync")
		if err := pass(ctx); err != nil && !errors.Is(err, context.Canceled) {
			logf("akari daemon: sync: %v", err)
		} else if ctx.Err() == nil {
			logf("akari daemon: sync finished")
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			logf("akari daemon: stopped")
			return nil
		case <-timer.C:
		}
	}
}

// redirectStdio sends process stdout and stderr to w so an in-process sync pass
// writes its summary into the daemon log. restore closes the pipe and puts the
// original files back.
func redirectStdio(w io.Writer) (func(), error) {
	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	origOut, origErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = pw, pw
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(w, pr)
		close(done)
	}()
	return func() {
		os.Stdout, os.Stderr = origOut, origErr
		_ = pw.Close()
		<-done
		_ = pr.Close()
	}, nil
}
