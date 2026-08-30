package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/jssblck/akari/internal/config"
)

// isFlagSet reports whether a flag was actually present on the command line, as
// opposed to sitting at its zero-value default. login uses it to tell "clear the
// machine name" (--machine="" passed explicitly) apart from "leave it untouched"
// (--machine omitted entirely).
func isFlagSet(fs *flag.FlagSet, name string) bool {
	set := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			set = true
		}
	})
	return set
}

// resolveToken determines the token login writes. A token on the command line
// outlives the command: it stays in shell history, is readable in the process
// table while login runs, and lands in CI logs under `set -x`. So --token is the
// explicit non-interactive path rather than the only one, and with it absent the
// token comes from stdin: prompted without echo when stdin is a terminal, read
// straight through when it is not, which is what lets a secret manager pipe it
// in (`pass show akari/token | akari login --server URL`).
func resolveToken(flagToken string, stdin *os.File) (string, error) {
	if flagToken != "" {
		return flagToken, nil
	}
	if term.IsTerminal(int(stdin.Fd())) {
		fmt.Fprint(os.Stderr, "API token: ")
		typed, err := term.ReadPassword(int(stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", fmt.Errorf("read token: %w", err)
		}
		if token := strings.TrimSpace(string(typed)); token != "" {
			return token, nil
		}
		return "", errors.New("no token entered")
	}
	piped, err := io.ReadAll(stdin)
	if err != nil {
		return "", fmt.Errorf("read token from stdin: %w", err)
	}
	// Trailing newlines come with every `echo $TOKEN |` and every token file.
	if token := strings.TrimSpace(string(piped)); token != "" {
		return token, nil
	}
	return "", errors.New("no token on stdin: pipe the token in, or pass --token")
}

// runLogin writes the client config (server URL and API token) to the config
// file. The token is created out of band (web UI or `akari-server` admin) and
// reaches login through resolveToken; akari stores it with owner-only
// permissions.
func runLogin(args []string, stdin *os.File) error {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	server := fs.String("server", "", "akari server base URL, e.g. https://akari.example")
	token := fs.String("token", "", "API token (ingest or full scope); omit it to be prompted, or to read the token from stdin, which keeps the secret out of shell history and the process table")
	machine := fs.String("machine", "", "logical machine name to report for this client's sessions (default: OS hostname). Give an ephemeral fleet a stable identity, e.g. ci or sandbox-pool; AKARI_MACHINE overrides it per run")
	configPath := fs.String("config", "", "config file path (default: platform config dir)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *server == "" {
		return errors.New("--server is required")
	}
	resolved, err := resolveToken(*token, stdin)
	if err != nil {
		return err
	}

	// Preserve any existing extra roots and excludes rather than clobbering them.
	// ReadClient distinguishes a missing file (fine, start blank) from a corrupt
	// one (refuse to overwrite and lose recoverable content).
	cfg, _, err := config.ReadClient(*configPath)
	if err != nil {
		return err
	}
	cfg.ServerURL = *server
	cfg.Token = resolved
	// Only overwrite the machine when the flag is given, so re-running login to
	// rotate a token leaves an existing machine identity in place. Passing
	// --machine with an empty value is the way to clear it back to the hostname.
	if isFlagSet(fs, "machine") {
		cfg.Machine = strings.TrimSpace(*machine)
	}

	if err := config.SaveClient(*configPath, cfg); err != nil {
		return err
	}
	path := *configPath
	if path == "" {
		path, _ = config.DefaultClientPath()
	}
	fmt.Fprintf(os.Stderr, "wrote config to %s\n", path)
	if cfg.Machine != "" {
		fmt.Fprintf(os.Stderr, "reporting sessions as machine %q\n", cfg.Machine)
	}
	return nil
}
