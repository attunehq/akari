package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/jssblck/akari/internal/client/daemon"
	"github.com/jssblck/akari/internal/config"
)

func TestDaemonInstallRequiresConfig(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("AppData", configHome)
	t.Setenv("HOME", configHome)
	t.Setenv(config.URLEnvVar, "https://from-env")
	t.Setenv(config.TokenEnvVar, "env-token")

	err := runDaemon(context.Background(), []string{"install"})
	if err == nil {
		t.Fatal("install succeeded without a client config")
	}
	if !strings.Contains(err.Error(), "akari login") && !strings.Contains(err.Error(), "no config") {
		t.Fatalf("install error = %v, want a missing-config hint", err)
	}
}

func TestDaemonInstallRequiresCredentialsInFile(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("AppData", configHome)
	t.Setenv("HOME", configHome)
	path := filepath.Join(configHome, "config.toml")
	if err := os.WriteFile(path, []byte("server_url = \"https://a\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.TokenEnvVar, "env-token")

	err := runDaemon(context.Background(), []string{"install", "--config", path})
	if err == nil {
		t.Fatal("install succeeded with token only in the environment")
	}
	if !strings.Contains(err.Error(), "must be in the file") {
		t.Fatalf("install error = %v, want a file-credentials hint", err)
	}
}

func TestDaemonInstallRejectedOffDarwin(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("this process is running on macOS")
	}
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("AppData", configHome)
	t.Setenv("HOME", configHome)
	path := filepath.Join(configHome, "config.toml")
	if err := os.WriteFile(path, []byte("server_url = \"https://a\"\ntoken = \"t\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := runDaemon(context.Background(), []string{"install", "--config", path})
	if !errors.Is(err, daemon.ErrInstallUnsupported) {
		t.Fatalf("install error = %v, want ErrInstallUnsupported", err)
	}
}

func TestDaemonInstallRejectsStopOnlyOptions(t *testing.T) {
	if err := runDaemon(context.Background(), []string{"install", "--force"}); err == nil {
		t.Fatal("daemon install accepted --force")
	}
}

func TestDaemonUninstallRejectedOffDarwin(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("this process is running on macOS")
	}
	err := runDaemon(context.Background(), []string{"uninstall"})
	if !errors.Is(err, daemon.ErrInstallUnsupported) {
		t.Fatalf("uninstall error = %v, want ErrInstallUnsupported", err)
	}
}
