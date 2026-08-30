package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// errAny stands in for any hostname lookup failure in ResolveMachine's tests.
var errAny = errors.New("hostname lookup failed")

func TestMain(m *testing.M) {
	// Cloud and eph shells export AKARI_URL for the running server. Clear it so
	// LoadClient tests see only the file (and the env they set themselves).
	_ = os.Unsetenv(URLEnvVar)
	_ = os.Unsetenv(TokenEnvVar)
	os.Exit(m.Run())
}

func TestSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	in := Client{
		ServerURL:  "https://akari.example",
		Token:      "secret-token",
		ExtraRoots: []ExtraRoot{{Agent: "pi", Path: "/extra/pi"}},
		Excludes:   []string{"**/tmp/**"},
	}
	if err := SaveClient(path, in); err != nil {
		t.Fatal(err)
	}
	got, err := LoadClient(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.ServerURL != in.ServerURL || got.Token != in.Token {
		t.Errorf("round trip = %+v", got)
	}
	if len(got.ExtraRoots) != 1 || got.ExtraRoots[0] != in.ExtraRoots[0] {
		t.Errorf("extra roots not preserved: %+v", got.ExtraRoots)
	}
	if len(got.Excludes) != 1 || got.Excludes[0] != "**/tmp/**" {
		t.Errorf("excludes not preserved: %+v", got.Excludes)
	}
}

func TestSaveLoadPreservesFollowRootLink(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	in := Client{
		ServerURL:  "https://akari.example",
		Token:      "secret-token",
		ExtraRoots: []ExtraRoot{{Agent: "claude", Path: "/mnt/linked-claude", FollowRootLink: true}},
	}
	if err := SaveClient(path, in); err != nil {
		t.Fatal(err)
	}
	got, err := LoadClient(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.ExtraRoots) != 1 || got.ExtraRoots[0] != in.ExtraRoots[0] {
		t.Fatalf("extra root with follow_root_link not preserved: %+v", got.ExtraRoots)
	}
	if !got.ExtraRoots[0].FollowRootLink {
		t.Error("follow_root_link round-tripped as false")
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "follow_root_link = true") {
		t.Errorf("config file does not spell out follow_root_link: %s", raw)
	}
}

func TestResolveMachine(t *testing.T) {
	const hostname = "host-from-os"
	okHost := func() (string, error) { return hostname, nil }
	env := func(vals map[string]string) func(string) string {
		return func(k string) string { return vals[k] }
	}

	cases := []struct {
		name string
		cfg  Client
		env  map[string]string
		host func() (string, error)
		want string
	}{
		{
			name: "hostname is the default",
			host: okHost,
			want: hostname,
		},
		{
			name: "config overrides hostname",
			cfg:  Client{Machine: "sandbox-pool"},
			host: okHost,
			want: "sandbox-pool",
		},
		{
			name: "env overrides config and hostname",
			cfg:  Client{Machine: "sandbox-pool"},
			env:  map[string]string{MachineEnvVar: "ci"},
			host: okHost,
			want: "ci",
		},
		{
			name: "blank env falls through to config",
			cfg:  Client{Machine: "sandbox-pool"},
			env:  map[string]string{MachineEnvVar: "   "},
			host: okHost,
			want: "sandbox-pool",
		},
		{
			name: "blank config falls through to hostname",
			cfg:  Client{Machine: "  "},
			host: okHost,
			want: hostname,
		},
		{
			name: "values are trimmed",
			env:  map[string]string{MachineEnvVar: "  ci  "},
			host: okHost,
			want: "ci",
		},
		{
			name: "a hostname error yields an empty machine, as before",
			host: func() (string, error) { return "", errAny },
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ResolveMachine(tc.cfg, env(tc.env), tc.host)
			if got != tc.want {
				t.Errorf("ResolveMachine = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSaveLoadPreservesMachine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	in := Client{ServerURL: "https://akari.example", Token: "t", Machine: "ci"}
	if err := SaveClient(path, in); err != nil {
		t.Fatal(err)
	}
	got, err := LoadClient(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Machine != "ci" {
		t.Errorf("machine not round-tripped: %q", got.Machine)
	}
}

func TestReadClientMissing(t *testing.T) {
	_, exists, err := ReadClient(filepath.Join(t.TempDir(), "nope.toml"))
	if err != nil {
		t.Fatalf("missing config should not error: %v", err)
	}
	if exists {
		t.Error("missing config reported as existing")
	}
}

func TestReadClientCorrupt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.toml")
	if err := os.WriteFile(path, []byte("this is = not [valid toml"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadClient(path); err == nil {
		t.Fatal("corrupt config should error, not be treated as empty")
	}
}

func TestReadClientRejectsUnknownKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	raw := `server_url = "https://akari.example"
token = "secret"
exclude = ["**/private/**"]

[[extra_roots]]
agent = "claude"
path = "/sessions"
typo = true
`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadClient(path); err == nil ||
		!strings.Contains(err.Error(), "exclude") || !strings.Contains(err.Error(), "extra_roots.typo") {
		t.Fatalf("ReadClient unknown keys error = %v, want both key names", err)
	}
}

func TestLoadClientValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(`server_url = "https://x"`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := mustLoadClientError(t, path)
	if !strings.Contains(err.Error(), "token is required") || !strings.Contains(err.Error(), TokenEnvVar) {
		t.Fatalf("LoadClient missing token error = %v, want token required and %s", err, TokenEnvVar)
	}
}

func TestLoadClientMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nope.toml")
	err := mustLoadClientError(t, path)
	if !strings.Contains(err.Error(), "no config") || !strings.Contains(err.Error(), "akari login") ||
		!strings.Contains(err.Error(), URLEnvVar) || !strings.Contains(err.Error(), TokenEnvVar) {
		t.Fatalf("LoadClient missing file error = %v, want login and env-var hints", err)
	}
}

func TestLoadClientEnvOverridesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := SaveClient(path, Client{ServerURL: "https://from-file", Token: "file-token"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv(URLEnvVar, "https://from-env")
	t.Setenv(TokenEnvVar, "env-token")
	got, err := LoadClient(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.ServerURL != "https://from-env" || got.Token != "env-token" {
		t.Errorf("LoadClient env overlay = %+v, want url/token from env", got)
	}
}

func TestLoadClientEnvWithoutFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nope.toml")
	t.Setenv(URLEnvVar, "https://ci.example")
	t.Setenv(TokenEnvVar, "ci-token")
	got, err := LoadClient(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.ServerURL != "https://ci.example" || got.Token != "ci-token" {
		t.Errorf("LoadClient env-only = %+v", got)
	}
}

func TestLoadClientBlankEnvFallsThrough(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := SaveClient(path, Client{ServerURL: "https://from-file", Token: "file-token"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv(URLEnvVar, "  ")
	t.Setenv(TokenEnvVar, "")
	got, err := LoadClient(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.ServerURL != "https://from-file" || got.Token != "file-token" {
		t.Errorf("blank env should fall through, got %+v", got)
	}
}

func TestLoadClientEnvFillsMissingToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(`server_url = "https://from-file"`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(TokenEnvVar, "  env-token  ")
	got, err := LoadClient(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.ServerURL != "https://from-file" || got.Token != "env-token" {
		t.Errorf("LoadClient mixed file/env = %+v", got)
	}
}

func TestLoadClientPartialEnvWithoutFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nope.toml")
	t.Setenv(URLEnvVar, "https://ci.example")
	err := mustLoadClientError(t, path)
	if !strings.Contains(err.Error(), "token is required") || !strings.Contains(err.Error(), TokenEnvVar) {
		t.Fatalf("LoadClient URL-only env error = %v, want token required", err)
	}

	t.Setenv(URLEnvVar, "")
	t.Setenv(TokenEnvVar, "ci-token")
	err = mustLoadClientError(t, path)
	if !strings.Contains(err.Error(), "server_url is required") || !strings.Contains(err.Error(), URLEnvVar) {
		t.Fatalf("LoadClient token-only env error = %v, want server_url required", err)
	}
}

func TestLoadClientEnvKeepsExtraRoots(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	in := Client{
		ServerURL:  "https://from-file",
		Token:      "file-token",
		ExtraRoots: []ExtraRoot{{Agent: "pi", Path: "/extra/pi"}},
	}
	if err := SaveClient(path, in); err != nil {
		t.Fatal(err)
	}
	t.Setenv(URLEnvVar, "https://from-env")
	got, err := LoadClient(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.ServerURL != "https://from-env" || got.Token != "file-token" {
		t.Errorf("url overlay = %+v", got)
	}
	if len(got.ExtraRoots) != 1 || got.ExtraRoots[0] != in.ExtraRoots[0] {
		t.Errorf("extra roots dropped under env overlay: %+v", got.ExtraRoots)
	}
}

func mustLoadClientError(t *testing.T, path string) error {
	t.Helper()
	_, err := LoadClient(path)
	if err == nil {
		t.Fatal("LoadClient succeeded, want an error")
	}
	return err
}

func TestLoadClientRejectsUnknownAgent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	raw := `server_url = "https://akari.example"
token = "secret"

[[extra_roots]]
agent = "copilot"
path = "/sessions"
`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadClient(path); err == nil || !strings.Contains(err.Error(), "must be claude, codex, pi, cursor, grok, or opencode") {
		t.Fatalf("LoadClient invalid agent error = %v", err)
	}
}

func TestSaveDoesNotDestroyOnRewrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	first := Client{ServerURL: "https://a", Token: "t1", ExtraRoots: []ExtraRoot{{Agent: "pi", Path: "/p"}}}
	if err := SaveClient(path, first); err != nil {
		t.Fatal(err)
	}
	// A second save replaces the file atomically and leaves no stray temp files.
	if err := SaveClient(path, Client{ServerURL: "https://b", Token: "t2"}); err != nil {
		t.Fatal(err)
	}
	got, err := LoadClient(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.ServerURL != "https://b" || got.Token != "t2" {
		t.Errorf("second save not applied: %+v", got)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	for _, e := range entries {
		if e.Name() != "config.toml" {
			t.Errorf("stray file left behind: %s", e.Name())
		}
	}
}
