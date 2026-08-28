package main

import (
	"fmt"
	"os"

	"github.com/jssblck/akari/internal/client/daemon"
	"github.com/jssblck/akari/internal/config"
)

func runDaemonInstall(configPath string, paths daemon.Paths) error {
	if _, err := config.LoadClient(configPath); err != nil {
		return err
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	result, err := daemon.Install(self, configPath, paths)
	if err != nil {
		return err
	}
	fmt.Printf("akari daemon will start at login (%s)\n", result.PlistPath)
	running, pid, err := daemon.Status(paths)
	if err != nil {
		return err
	}
	if !running {
		return fmt.Errorf("login agent installed but daemon is not running; check %s", paths.Logfile)
	}
	if result.Started {
		fmt.Printf("akari daemon started (pid %d); logging to %s\n", pid, paths.Logfile)
	} else {
		fmt.Printf("akari daemon already running (pid %d); logging to %s\n", pid, paths.Logfile)
	}
	return nil
}

func runDaemonUninstall() error {
	if err := daemon.Uninstall(); err != nil {
		return err
	}
	fmt.Println("akari login agent removed")
	return nil
}
