package daemon

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const launchAgentLabel = "com.jssblck.akari"

var (
	// ErrInstallUnsupported is returned when daemon install/uninstall is used
	// on an OS that has no login-agent implementation.
	ErrInstallUnsupported = errors.New("daemon install is only supported on macOS")
	// ErrNotInstalled is returned when uninstall finds no login agent.
	ErrNotInstalled = errors.New("login agent is not installed")

	currentHomeDir   = os.UserHomeDir
	currentUID       = os.Getuid
	getenv           = os.Getenv
	runLaunchctl     = defaultLaunchctl
	startConfirmWait = 3 * time.Second
)

// InstallResult is the outcome of a successful Install.
type InstallResult struct {
	PlistPath string
	Started   bool
}

// Install registers a macOS LaunchAgent that runs the periodic-sync daemon at
// Aqua login. launchd owns that process: the login equivalent of daemon start,
// without detaching out of the session (a Setsid child would survive logout and
// then fail the next login with ErrAlreadyRunning). Other OSes return
// ErrInstallUnsupported.
//
// The installer's PATH is snapshotted into the plist because login agents do
// not source shell rc files, and sync shells out to git by name.
func Install(self, configPath string, p Paths) (InstallResult, error) {
	return install(runtime.GOOS, self, configPath, p)
}

func install(goos, self, configPath string, p Paths) (InstallResult, error) {
	if goos != "darwin" {
		return InstallResult{}, fmt.Errorf("%w, not %s", ErrInstallUnsupported, goos)
	}
	if strings.TrimSpace(self) == "" {
		return InstallResult{}, fmt.Errorf("akari executable path is required")
	}
	self, err := filepath.Abs(self)
	if err != nil {
		return InstallResult{}, fmt.Errorf("resolve executable path: %w", err)
	}
	argv := []string{self, "daemon", "run", "--daemon-log", p.Logfile}
	if configPath != "" {
		absConfig, err := filepath.Abs(configPath)
		if err != nil {
			return InstallResult{}, fmt.Errorf("resolve config path: %w", err)
		}
		argv = append(argv, "--config", absConfig)
	}

	home, err := currentHomeDir()
	if err != nil {
		return InstallResult{}, fmt.Errorf("locate home directory: %w", err)
	}
	plistPath := launchAgentPlistPath(home)
	body, err := launchAgentPlist(launchAgentLabel, argv, getenv("PATH"))
	if err != nil {
		return InstallResult{}, err
	}

	// Unload first so replacing the plist is not racing a loaded job.
	if err := bootoutAgent(); err != nil {
		return InstallResult{}, err
	}
	if err := os.MkdirAll(filepath.Dir(plistPath), 0o755); err != nil {
		return InstallResult{}, fmt.Errorf("create LaunchAgents directory: %w", err)
	}
	if err := os.WriteFile(plistPath, body, 0o600); err != nil {
		return InstallResult{}, fmt.Errorf("write login agent %s: %w", plistPath, err)
	}
	if err := enableAgent(); err != nil {
		return InstallResult{}, err
	}

	result := InstallResult{PlistPath: plistPath}
	running, err := IsRunning(p.Pidfile)
	if err != nil {
		return InstallResult{}, fmt.Errorf("check daemon status: %w", err)
	}
	if running {
		return result, nil
	}
	if err := bootstrapAgent(plistPath); err != nil {
		return InstallResult{}, err
	}
	if err := waitUntilRunning(p, startConfirmWait); err != nil {
		return InstallResult{}, err
	}
	result.Started = true
	return result, nil
}

// Uninstall removes the macOS login LaunchAgent. A launchd-managed daemon is
// stopped by the bootout; a process started by daemon start is left running.
func Uninstall() error {
	return uninstall(runtime.GOOS)
}

func uninstall(goos string) error {
	if goos != "darwin" {
		return fmt.Errorf("%w, not %s", ErrInstallUnsupported, goos)
	}
	home, err := currentHomeDir()
	if err != nil {
		return fmt.Errorf("locate home directory: %w", err)
	}
	plistPath := launchAgentPlistPath(home)
	loaded := agentIsLoaded()
	if err := bootoutAgent(); err != nil {
		return err
	}
	err = os.Remove(plistPath)
	missing := os.IsNotExist(err)
	if err != nil && !missing {
		return fmt.Errorf("remove login agent %s: %w", plistPath, err)
	}
	if missing && !loaded {
		return ErrNotInstalled
	}
	return nil
}

func launchAgentPlistPath(home string) string {
	return filepath.Join(home, "Library", "LaunchAgents", launchAgentLabel+".plist")
}

func launchAgentPlist(label string, argv []string, pathEnv string) ([]byte, error) {
	if label == "" {
		return nil, fmt.Errorf("launch agent label is required")
	}
	if len(argv) == 0 {
		return nil, fmt.Errorf("launch agent program is required")
	}
	var b bytes.Buffer
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>`)
	if err := xml.EscapeText(&b, []byte(label)); err != nil {
		return nil, err
	}
	b.WriteString(`</string>
	<key>ProgramArguments</key>
	<array>
`)
	for _, arg := range argv {
		b.WriteString("\t\t<string>")
		if err := xml.EscapeText(&b, []byte(arg)); err != nil {
			return nil, err
		}
		b.WriteString("</string>\n")
	}
	b.WriteString(`	</array>
	<key>RunAtLoad</key>
	<true/>
`)
	if pathEnv != "" {
		b.WriteString(`	<key>EnvironmentVariables</key>
	<dict>
		<key>PATH</key>
		<string>`)
		if err := xml.EscapeText(&b, []byte(pathEnv)); err != nil {
			return nil, err
		}
		b.WriteString(`</string>
	</dict>
`)
	}
	b.WriteString(`</dict>
</plist>
`)
	return b.Bytes(), nil
}

func serviceTarget() string {
	return "gui/" + strconv.Itoa(currentUID()) + "/" + launchAgentLabel
}

func agentIsLoaded() bool {
	return runLaunchctl("print", serviceTarget()) == nil
}

func enableAgent() error {
	if err := runLaunchctl("enable", serviceTarget()); err != nil {
		return fmt.Errorf("enable login agent: %w", err)
	}
	return nil
}

func bootstrapAgent(plistPath string) error {
	domain := "gui/" + strconv.Itoa(currentUID())
	if err := runLaunchctl("bootstrap", domain, plistPath); err != nil {
		return fmt.Errorf("load login agent: %w", err)
	}
	return nil
}

func bootoutAgent() error {
	err := runLaunchctl("bootout", serviceTarget())
	if err == nil || launchctlNotLoaded(err) {
		return nil
	}
	return fmt.Errorf("unload login agent: %w", err)
}

func launchctlNotLoaded(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "No such process") || strings.Contains(msg, "Could not find")
}

func waitUntilRunning(p Paths, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		running, err := IsRunning(p.Pidfile)
		if err != nil {
			return fmt.Errorf("confirm daemon started: %w", err)
		}
		if running {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("login agent did not start the daemon in time; check %s", p.Logfile)
}

func defaultLaunchctl(args ...string) error {
	cmd := exec.Command("launchctl", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			return fmt.Errorf("launchctl %s: %w", strings.Join(args, " "), err)
		}
		return fmt.Errorf("launchctl %s: %w (%s)", strings.Join(args, " "), err, msg)
	}
	return nil
}
