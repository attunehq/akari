package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/jssblck/akari/internal/client/discover"
	"github.com/jssblck/akari/internal/client/resolve"
	"github.com/jssblck/akari/internal/client/syncer"
	"github.com/jssblck/akari/internal/client/upload"
	"github.com/jssblck/akari/internal/config"
	"github.com/jssblck/akari/internal/parser"
)

// ingestOptions is the parsed, validated form of `akari ingest`'s flags.
type ingestOptions struct {
	configPath string
	root       string
	finalize   bool
}

// parseIngestArgs parses the ingest flag set. The root path is not opened here:
// existence and type are checked in ingest so a missing --root fails before any
// stat.
func parseIngestArgs(args []string) (ingestOptions, error) {
	fs := flag.NewFlagSet("ingest", flag.ContinueOnError)
	configPath := fs.String("config", "", "config file path (default: platform config dir)")
	root := fs.String("root", "", "directory to discover session files from")
	finalize := fs.Bool("finalize", true, "treat discovered sessions as terminal and flush trailing turns now (default true; pass --finalize=false to wait for the idle settle window)")
	if err := fs.Parse(args); err != nil {
		return ingestOptions{}, err
	}
	dir := strings.TrimSpace(*root)
	switch fs.NArg() {
	case 0:
	case 1:
		if dir != "" {
			return ingestOptions{}, fmt.Errorf("pass the directory as --root or as a positional argument, not both")
		}
		dir = fs.Arg(0)
	default:
		return ingestOptions{}, fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	if dir == "" {
		return ingestOptions{}, fmt.Errorf("--root is required")
	}
	return ingestOptions{
		configPath: *configPath,
		root:       dir,
		finalize:   *finalize,
	}, nil
}

// ingestRoots treats dir as an extra root for every agent. Optional is set so a
// layout one agent does not use (no OpenCode database, for example) is skipped
// rather than failing the whole ingest; the caller has already required that
// dir itself exists.
func ingestRoots(dir string) []discover.Root {
	roots := make([]discover.Root, 0, len(parser.Agents))
	for _, a := range parser.Agents {
		roots = append(roots, discover.Root{Agent: string(a), Dir: dir, Optional: true})
	}
	return roots
}

// discoverIngestRoot walks dir once per agent and keeps a file under the first
// agent whose header signature matches. Path-level de-dup inside Discover would
// otherwise tag every *.jsonl as claude (the first agent that Matches any
// jsonl).
func discoverIngestRoot(dir string) (files []discover.File, notices []string, err error) {
	seen := map[string]bool{}
	var problems []error
	for _, root := range ingestRoots(dir) {
		found, n, derr := discover.Discover([]discover.Root{root}, discover.Excluder{})
		notices = append(notices, n...)
		if derr != nil {
			problems = append(problems, derr)
		}
		for _, f := range found {
			if seen[f.Path] {
				continue
			}
			if _, herr := resolve.PeekHeader(f); herr != nil {
				continue
			}
			seen[f.Path] = true
			files = append(files, f)
		}
	}
	return files, notices, errors.Join(problems...)
}

// runIngest discovers session files under one directory and uploads them through
// the logged-in client config.
func runIngest(ctx context.Context, args []string) error {
	return ingest(ctx, args, upload.NewHTTPClient())
}

func ingest(ctx context.Context, args []string, httpClient *http.Client) error {
	opts, err := parseIngestArgs(args)
	if err != nil {
		return err
	}

	abs, err := filepath.Abs(opts.root)
	if err != nil {
		return fmt.Errorf("resolve --root: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return fmt.Errorf("stat --root: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("--root %s is not a directory", abs)
	}

	cfg, err := config.LoadClient(opts.configPath)
	if err != nil {
		return err
	}
	machine := config.ResolveMachine(cfg, os.Getenv, os.Hostname)

	files, notices, discoveryErr := discoverIngestRoot(abs)
	for _, n := range notices {
		fmt.Fprintln(os.Stderr, "notice: "+n)
	}

	client := upload.New(httpClient, cfg.ServerURL, cfg.Token)
	sync := syncer.New(resolve.New(), client, machine, opts.finalize)
	run := func(c context.Context, f discover.File) outcome {
		r := sync.SyncOne(c, f)
		return outcome{sync: &r}
	}
	sum, interrupted := syncAll(ctx, ctx, files, 1, run)
	sum.discoveryFailed = discover.ErrorCount(discoveryErr)
	printSummary(len(files), sum, false)
	if interrupted && ctx.Err() != nil {
		fmt.Fprintf(os.Stderr, "interrupted: stopped before processing every file\n")
	}
	var uploadErr error
	if sum.failed > 0 {
		uploadErr = fmt.Errorf("%d file(s) failed to upload", sum.failed)
	}
	if discoveryErr != nil {
		discoveryErr = fmt.Errorf("discover sessions: %w", discoveryErr)
	}
	return errors.Join(discoveryErr, uploadErr)
}
