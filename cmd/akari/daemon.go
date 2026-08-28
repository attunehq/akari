package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/jssblck/akari/internal/client/daemon"
)

// runDaemon manages the periodic-sync background process: start, stop, status,
// and on macOS a login LaunchAgent. `run` is the detached worker start launches.
func runDaemon(ctx context.Context, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: akari daemon {start|status|install} [--config PATH] | akari daemon stop [--timeout DUR] [--force] | akari daemon uninstall")
	}
	sub := args[0]
	if sub == "run" {
		return runDaemonLoop(ctx, args[1:])
	}

	fs := flag.NewFlagSet("daemon", flag.ContinueOnError)
	configPath := fs.String("config", "", "config file path (passed to the daemon process)")
	force := fs.Bool("force", false, "terminate the daemon if graceful shutdown does not complete")
	timeout := fs.Duration("timeout", daemon.DefaultStopTimeout, "maximum wait for each shutdown phase")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	stopOptionSet := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "force" || f.Name == "timeout" {
			stopOptionSet = true
		}
	})
	if sub != "stop" && stopOptionSet {
		return fmt.Errorf("--force and --timeout are valid only for daemon stop")
	}
	if sub == "stop" && *timeout <= 0 {
		return fmt.Errorf("--timeout must be positive")
	}

	paths, err := daemon.DefaultPaths()
	if err != nil {
		return err
	}

	switch sub {
	case "start":
		self, err := os.Executable()
		if err != nil {
			return err
		}
		childArgs := []string{"daemon", "run"}
		if *configPath != "" {
			childArgs = append(childArgs, "--config", *configPath)
		}
		if err := daemon.Start(self, childArgs, paths); err != nil {
			return err
		}
		running, pid, err := daemon.Status(paths)
		if err != nil {
			return err
		}
		if running {
			fmt.Printf("akari daemon started (pid %d); logging to %s\n", pid, paths.Logfile)
			return nil
		}
		return fmt.Errorf("background daemon did not acquire its lock")

	case "stop":
		result, err := daemon.Stop(paths, daemon.StopOptions{Timeout: *timeout, Force: *force})
		if err != nil {
			return err
		}
		if result == daemon.StoppedForcefully {
			fmt.Println("akari daemon force-stopped")
		} else {
			fmt.Println("akari daemon stopped")
		}
		return nil

	case "status":
		running, pid, err := daemon.Status(paths)
		if err != nil {
			return err
		}
		if running {
			fmt.Printf("running (pid %d)\n", pid)
		} else {
			fmt.Println("not running")
		}
		return nil

	case "install":
		return runDaemonInstall(*configPath, paths)
	case "uninstall":
		return runDaemonUninstall()

	default:
		return fmt.Errorf("unknown daemon command %q (want start, stop, status, install, or uninstall)", sub)
	}
}
