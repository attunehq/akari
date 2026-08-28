package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestLaunchAgentPlistEscapesAndSnapshotsPath(t *testing.T) {
	got, err := launchAgentPlist("com.jssblck.akari", []string{
		`/tmp/akari & bin/akari`,
		"daemon",
		"run",
		"--daemon-log",
		"/tmp/akari.log",
		"--config",
		`/tmp/cfg <file>.toml`,
	}, `/opt/homebrew/bin:/usr/bin:/bin`)
	if err != nil {
		t.Fatal(err)
	}
	body := string(got)
	for _, want := range []string{
		"<key>Label</key>\n\t<string>com.jssblck.akari</string>",
		"<string>/tmp/akari &amp; bin/akari</string>",
		"<string>/tmp/cfg &lt;file&gt;.toml</string>",
		"<string>daemon</string>",
		"<string>run</string>",
		"<string>--daemon-log</string>",
		"<key>RunAtLoad</key>\n\t<true/>",
		"<key>PATH</key>\n\t\t<string>/opt/homebrew/bin:/usr/bin:/bin</string>",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("plist missing %q\n%s", want, body)
		}
	}
}

func TestLaunchAgentPlistOmitsEmptyPath(t *testing.T) {
	got, err := launchAgentPlist("com.jssblck.akari", []string{"/bin/akari"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "EnvironmentVariables") {
		t.Fatalf("empty PATH still wrote EnvironmentVariables:\n%s", got)
	}
}

func TestInstallRejectedOffDarwin(t *testing.T) {
	_, err := install("linux", "/usr/local/bin/akari", "", Paths{})
	if !errors.Is(err, ErrInstallUnsupported) {
		t.Fatalf("install linux: %v", err)
	}
	if err := uninstall("windows"); !errors.Is(err, ErrInstallUnsupported) {
		t.Fatalf("uninstall windows: %v", err)
	}
}

func TestInstallWritesLaunchAgentAndBootstraps(t *testing.T) {
	home := t.TempDir()
	paths := Paths{
		Pidfile: filepath.Join(t.TempDir(), "akari.pid"),
		Logfile: filepath.Join(t.TempDir(), "akari.log"),
	}
	self := filepath.Join(t.TempDir(), "akari")
	configPath := filepath.Join(t.TempDir(), "config.toml")
	var calls [][]string
	stubInstallEnv(t, home, 501, "/opt/homebrew/bin:/bin", func(args ...string) error {
		calls = append(calls, append([]string(nil), args...))
		switch args[0] {
		case "bootout":
			return errors.New("launchctl bootout gui/501/com.jssblck.akari: exit status 3 (No such process)")
		case "bootstrap":
			lock, err := Acquire(paths.Pidfile)
			if err != nil {
				return err
			}
			t.Cleanup(func() { _ = lock.Release() })
			return nil
		default:
			return nil
		}
	})

	result, err := install("darwin", self, configPath, paths)
	if err != nil {
		t.Fatal(err)
	}
	wantPlist := filepath.Join(home, "Library", "LaunchAgents", "com.jssblck.akari.plist")
	if result.PlistPath != wantPlist {
		t.Fatalf("plist path = %q, want %q", result.PlistPath, wantPlist)
	}
	if !result.Started {
		t.Fatal("expected install to start the daemon")
	}
	body, err := os.ReadFile(wantPlist)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	absSelf, err := filepath.Abs(self)
	if err != nil {
		t.Fatal(err)
	}
	absConfig, err := filepath.Abs(configPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"<string>" + absSelf + "</string>",
		"<string>daemon</string>",
		"<string>run</string>",
		"<string>--daemon-log</string>",
		"<string>" + paths.Logfile + "</string>",
		"<string>--config</string>",
		"<string>" + absConfig + "</string>",
		"<string>/opt/homebrew/bin:/bin</string>",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("plist missing %q\n%s", want, text)
		}
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(wantPlist)
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Fatalf("plist mode = %o, want 0600", perm)
		}
	}
	if got := launchctlOps(calls); strings.Join(got, ",") != "bootout,enable,bootstrap" {
		t.Fatalf("launchctl ops = %v, want bootout, enable, bootstrap", got)
	}
}

func TestInstallSkipsBootstrapWhenAlreadyRunning(t *testing.T) {
	home := t.TempDir()
	paths := Paths{
		Pidfile: filepath.Join(t.TempDir(), "akari.pid"),
		Logfile: filepath.Join(t.TempDir(), "akari.log"),
	}
	lock, err := Acquire(paths.Pidfile)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lock.Release() })

	var calls [][]string
	stubInstallEnv(t, home, 501, "/usr/bin", func(args ...string) error {
		calls = append(calls, append([]string(nil), args...))
		if args[0] == "bootout" {
			return errors.New("launchctl bootout gui/501/com.jssblck.akari: exit status 3 (No such process)")
		}
		if args[0] == "bootstrap" {
			t.Fatal("bootstrap started a second daemon while the lock was held")
		}
		return nil
	})

	result, err := install("darwin", filepath.Join(t.TempDir(), "akari"), "", paths)
	if err != nil {
		t.Fatal(err)
	}
	if result.Started {
		t.Fatal("install reported a start while the daemon was already running")
	}
	if got := launchctlOps(calls); strings.Join(got, ",") != "bootout,enable" {
		t.Fatalf("launchctl ops = %v, want bootout, enable", got)
	}
}

func TestInstallTimesOutWhenWatchDoesNotStart(t *testing.T) {
	home := t.TempDir()
	paths := Paths{
		Pidfile: filepath.Join(t.TempDir(), "akari.pid"),
		Logfile: filepath.Join(t.TempDir(), "akari.log"),
	}
	origWait := startConfirmWait
	startConfirmWait = 50 * time.Millisecond
	t.Cleanup(func() { startConfirmWait = origWait })
	stubInstallEnv(t, home, 501, "/usr/bin", func(args ...string) error {
		if args[0] == "bootout" {
			return errors.New("launchctl bootout gui/501/com.jssblck.akari: exit status 3 (No such process)")
		}
		return nil
	})

	_, err := install("darwin", filepath.Join(t.TempDir(), "akari"), "", paths)
	if err == nil || !strings.Contains(err.Error(), "did not start the daemon in time") {
		t.Fatalf("error = %v, want start timeout", err)
	}
}

func TestUninstallRemovesPlistAndBootout(t *testing.T) {
	home := t.TempDir()
	plistPath := launchAgentPlistPath(home)
	if err := os.MkdirAll(filepath.Dir(plistPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plistPath, []byte("plist"), 0o600); err != nil {
		t.Fatal(err)
	}
	var calls [][]string
	stubInstallEnv(t, home, 501, "", func(args ...string) error {
		calls = append(calls, append([]string(nil), args...))
		if args[0] == "print" {
			return nil
		}
		return nil
	})

	if err := uninstall("darwin"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(plistPath); !os.IsNotExist(err) {
		t.Fatalf("plist still present: %v", err)
	}
	if got := launchctlOps(calls); strings.Join(got, ",") != "print,bootout" {
		t.Fatalf("launchctl ops = %v, want print, bootout", got)
	}
}

func TestUninstallReportsNotInstalled(t *testing.T) {
	home := t.TempDir()
	stubInstallEnv(t, home, 501, "", func(args ...string) error {
		if args[0] == "print" {
			return errors.New("launchctl print gui/501/com.jssblck.akari: exit status 113 (Could not find service)")
		}
		if args[0] == "bootout" {
			return errors.New("launchctl bootout gui/501/com.jssblck.akari: exit status 3 (No such process)")
		}
		return nil
	})
	if err := uninstall("darwin"); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("uninstall: %v, want ErrNotInstalled", err)
	}
}

func stubInstallEnv(t *testing.T, home string, uid int, path string, launchctl func(args ...string) error) {
	t.Helper()
	origHome, origUID, origGetenv, origLaunchctl := currentHomeDir, currentUID, getenv, runLaunchctl
	currentHomeDir = func() (string, error) { return home, nil }
	currentUID = func() int { return uid }
	getenv = func(key string) string {
		if key == "PATH" {
			return path
		}
		return ""
	}
	runLaunchctl = launchctl
	t.Cleanup(func() {
		currentHomeDir = origHome
		currentUID = origUID
		getenv = origGetenv
		runLaunchctl = origLaunchctl
	})
}

func launchctlOps(calls [][]string) []string {
	ops := make([]string, 0, len(calls))
	for _, c := range calls {
		if len(c) > 0 {
			ops = append(ops, c[0])
		}
	}
	return ops
}
