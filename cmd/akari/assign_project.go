package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/jssblck/akari/internal/client/upload"
	"github.com/jssblck/akari/internal/config"
)

// assignProjectOptions is the parsed form of `akari assign-project`.
type assignProjectOptions struct {
	configPath string
	sessionID  int64
	projectID  int64
}

// parseAssignProjectArgs parses and validates the assign-project flag set.
func parseAssignProjectArgs(args []string) (assignProjectOptions, error) {
	fs := flag.NewFlagSet("assign-project", flag.ContinueOnError)
	configPath := fs.String("config", "", "config file path (default: platform config dir)")
	sessionID := fs.Int64("session", 0, "orphaned session id to re-home")
	projectID := fs.Int64("project", 0, "project id to pin the session onto")
	if err := fs.Parse(args); err != nil {
		return assignProjectOptions{}, err
	}
	if fs.NArg() != 0 {
		return assignProjectOptions{}, fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	if *sessionID < 1 {
		return assignProjectOptions{}, fmt.Errorf("--session is required")
	}
	if *projectID < 1 {
		return assignProjectOptions{}, fmt.Errorf("--project is required")
	}
	return assignProjectOptions{configPath: *configPath, sessionID: *sessionID, projectID: *projectID}, nil
}

// runAssignProject pins an orphaned session onto a project using the logged-in
// client's full-scope token. An ingest-scope token is refused by the server.
func runAssignProject(ctx context.Context, args []string) error {
	return assignProject(ctx, args, upload.NewHTTPClient())
}

func assignProject(ctx context.Context, args []string, httpClient *http.Client) error {
	opts, err := parseAssignProjectArgs(args)
	if err != nil {
		return err
	}
	cfg, err := config.LoadClient(opts.configPath)
	if err != nil {
		return err
	}
	got, err := putSessionProject(ctx, httpClient, cfg.ServerURL, cfg.Token, opts.sessionID, opts.projectID)
	if err != nil {
		return err
	}
	fmt.Printf("pinned session %d to project %d\n", got.SessionID, got.ProjectID)
	return nil
}

type sessionProjectResponse struct {
	SessionID int64 `json:"session_id"`
	ProjectID int64 `json:"project_id"`
	Pinned    bool  `json:"pinned"`
}

func putSessionProject(ctx context.Context, httpClient *http.Client, baseURL, token string, sessionID, projectID int64) (sessionProjectResponse, error) {
	body, err := json.Marshal(map[string]int64{"project_id": projectID})
	if err != nil {
		return sessionProjectResponse{}, err
	}
	url := strings.TrimRight(baseURL, "/") + fmt.Sprintf("/api/v1/app/sessions/%d/project", sessionID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader(body))
	if err != nil {
		return sessionProjectResponse{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return sessionProjectResponse{}, err
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return sessionProjectResponse{}, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return sessionProjectResponse{}, assignProjectAPIError(resp.StatusCode, payload)
	}
	var got sessionProjectResponse
	if err := json.Unmarshal(payload, &got); err != nil {
		return sessionProjectResponse{}, fmt.Errorf("decode response: %w", err)
	}
	return got, nil
}

func assignProjectAPIError(status int, payload []byte) error {
	var env struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(payload, &env) == nil && env.Error != "" {
		return fmt.Errorf("%s", env.Error)
	}
	return fmt.Errorf("server returned HTTP %d", status)
}
