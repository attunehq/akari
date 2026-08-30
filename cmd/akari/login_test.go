package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jssblck/akari/internal/config"
)

// TestLoginMachineFlag pins the three states of `--machine`: setting a name,
// leaving an existing name untouched on a re-run that omits the flag, and
// clearing it back to the hostname with an explicit empty value.
func TestLoginMachineFlag(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")

	// First login sets a machine name.
	if err := runLogin([]string{"--server", "https://a", "--token", "t1", "--machine", "ci", "--config", path}, os.Stdin); err != nil {
		t.Fatal(err)
	}
	if got := readMachine(t, path); got != "ci" {
		t.Fatalf("after set, machine = %q, want ci", got)
	}

	// Re-running without --machine rotates the token but preserves the machine.
	if err := runLogin([]string{"--server", "https://a", "--token", "t2", "--config", path}, os.Stdin); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadClient(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Machine != "ci" {
		t.Errorf("machine not preserved across re-login: %q", cfg.Machine)
	}
	if cfg.Token != "t2" {
		t.Errorf("token not rotated: %q", cfg.Token)
	}

	// Passing --machine with an empty value explicitly clears it.
	if err := runLogin([]string{"--server", "https://a", "--token", "t2", "--machine", "", "--config", path}, os.Stdin); err != nil {
		t.Fatal(err)
	}
	if got := readMachine(t, path); got != "" {
		t.Errorf("after clear, machine = %q, want empty", got)
	}
}

func readMachine(t *testing.T, path string) string {
	t.Helper()
	cfg, err := config.LoadClient(path)
	if err != nil {
		t.Fatal(err)
	}
	return cfg.Machine
}

// TestLoginTokenFromStdin covers the non-argv token paths: a piped token is read
// and trimmed, --token still wins when both are present, and empty stdin fails
// loudly instead of writing a blank credential.
func TestLoginTokenFromStdin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")

	if err := runLogin([]string{"--server", "https://a", "--config", path}, pipe(t, "akr_piped\n")); err != nil {
		t.Fatal(err)
	}
	if got := readToken(t, path); got != "akr_piped" {
		t.Errorf("token from stdin = %q, want akr_piped", got)
	}

	if err := runLogin([]string{"--server", "https://a", "--token", "akr_flag", "--config", path}, pipe(t, "akr_piped\n")); err != nil {
		t.Fatal(err)
	}
	if got := readToken(t, path); got != "akr_flag" {
		t.Errorf("with both given, token = %q, want the flag's akr_flag", got)
	}

	err := runLogin([]string{"--server", "https://a", "--config", path}, pipe(t, "  \n"))
	if err == nil || !strings.Contains(err.Error(), "no token") {
		t.Fatalf("empty stdin error = %v, want a no-token error", err)
	}
	if got := readToken(t, path); got != "akr_flag" {
		t.Errorf("failed login rewrote the config: token = %q", got)
	}
}

// pipe hands runLogin a non-terminal stdin holding content.
func pipe(t *testing.T, content string) *os.File {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	go func() {
		defer w.Close()
		w.WriteString(content)
	}()
	return r
}

func readToken(t *testing.T, path string) string {
	t.Helper()
	cfg, err := config.LoadClient(path)
	if err != nil {
		t.Fatal(err)
	}
	return cfg.Token
}
