package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/jssblck/akari/internal/server/auth"
	"github.com/jssblck/akari/internal/server/store"
)

func TestAssignSessionProjectAPI(t *testing.T) {
	t.Parallel()
	srv, st := newTestServer(t)
	ctx := context.Background()

	admin, err := st.Register(ctx, "grace", mustHash(t, "hopper-1906"), "")
	if err != nil {
		t.Fatalf("register admin: %v", err)
	}
	if _, err := st.CreateInvite(ctx, auth.HashToken("inv-ada"), admin.ID, "", nil); err != nil {
		t.Fatalf("invite ada: %v", err)
	}
	ada, err := st.Register(ctx, "ada", mustHash(t, "lovelace-1843"), auth.HashToken("inv-ada"))
	if err != nil {
		t.Fatalf("register ada: %v", err)
	}
	if _, err := st.CreateInvite(ctx, auth.HashToken("inv-anna"), admin.ID, "", nil); err != nil {
		t.Fatalf("invite anna: %v", err)
	}
	if _, err := st.Register(ctx, "anna", mustHash(t, "winlock-1857"), auth.HashToken("inv-anna")); err != nil {
		t.Fatalf("register anna: %v", err)
	}

	remoteID, err := st.UpsertProject(ctx, "github.com/ada/akari", "github.com", "ada", "akari", "akari", "remote")
	if err != nil {
		t.Fatal(err)
	}
	orphanID, err := st.UpsertProject(ctx, "local:box:/tmp/wt", "box", "", "wt", "wt", "orphaned")
	if err != nil {
		t.Fatal(err)
	}
	ann, err := st.Announce(ctx, store.AnnounceParams{
		UserID: ada.ID, Agent: "claude", SourceSessionID: "ada-orphan",
		ProjectID: orphanID, Kind: "orphaned", Cwd: "/tmp/wt", Machine: "box",
	})
	if err != nil {
		t.Fatal(err)
	}

	login := func(username, password string) *http.Client {
		c := newClient(t)
		status, body := postJSON(t, c, srv.URL+"/api/v1/auth/login", fmt.Sprintf(`{"username":%q,"password":%q}`, username, password))
		if status != http.StatusOK {
			t.Fatalf("login %s: status=%d body=%v", username, status, body)
		}
		return c
	}
	assign := func(c *http.Client, sessionID, projectID int64) (*http.Response, map[string]any) {
		return doJSON(t, c, http.MethodPut, srv.URL+fmt.Sprintf("/api/v1/app/sessions/%d/project", sessionID),
			map[string]any{"project_id": projectID})
	}

	adaClient := login("ada", "lovelace-1843")
	annaClient := login("anna", "winlock-1857")
	graceClient := login("grace", "hopper-1906")

	resp, body := assign(annaClient, ann.SessionID, remoteID)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("non-owner assign status = %d body=%v, want 403", resp.StatusCode, body)
	}

	resp, body = assign(adaClient, ann.SessionID, remoteID)
	if resp.StatusCode != http.StatusOK || body["project_id"] != float64(remoteID) || body["pinned"] != true {
		t.Fatalf("owner assign: status=%d body=%v", resp.StatusCode, body)
	}

	otherRemote, err := st.UpsertProject(ctx, "github.com/ada/other", "github.com", "ada", "other", "other", "remote")
	if err != nil {
		t.Fatal(err)
	}
	resp, body = assign(graceClient, ann.SessionID, otherRemote)
	if resp.StatusCode != http.StatusOK || body["project_id"] != float64(otherRemote) {
		t.Fatalf("admin re-assign: status=%d body=%v", resp.StatusCode, body)
	}

	resp, body = assign(adaClient, ann.SessionID, 0)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("project_id 0: status=%d body=%v, want 400", resp.StatusCode, body)
	}
	resp, body = doJSON(t, adaClient, http.MethodPut, srv.URL+fmt.Sprintf("/api/v1/app/sessions/%d/project", ann.SessionID), map[string]any{})
	if resp.StatusCode != http.StatusBadRequest || body["error"] != "project_id is required" {
		t.Fatalf("missing project_id: status=%d body=%v", resp.StatusCode, body)
	}

	resolved, err := st.Announce(ctx, store.AnnounceParams{
		UserID: ada.ID, Agent: "claude", SourceSessionID: "ada-remote",
		ProjectID: remoteID, Kind: "remote", Cwd: "/home/ada/akari", Machine: "box",
	})
	if err != nil {
		t.Fatal(err)
	}
	resp, body = assign(adaClient, resolved.SessionID, otherRemote)
	if resp.StatusCode != http.StatusBadRequest || body["error"] != "session is not orphaned" {
		t.Fatalf("remote session assign: status=%d body=%v", resp.StatusCode, body)
	}

	resp, body = assign(adaClient, ann.SessionID, 999999)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("missing project: status=%d body=%v, want 404", resp.StatusCode, body)
	}
	resp, body = assign(adaClient, 999999, remoteID)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("missing session: status=%d body=%v, want 404", resp.StatusCode, body)
	}
}

func TestAssignSessionProjectMCP(t *testing.T) {
	t.Parallel()
	srv, st := newTestServer(t)
	ctx := context.Background()

	ada, err := st.Register(ctx, "ada", mustHash(t, "lovelace-1843"), "")
	if err != nil {
		t.Fatalf("register ada: %v", err)
	}
	secret, err := auth.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateAPIToken(ctx, ada.ID, "read", "read", auth.HashToken(secret)); err != nil {
		t.Fatalf("create read token: %v", err)
	}

	remoteID, err := st.UpsertProject(ctx, "github.com/ada/akari", "github.com", "ada", "akari", "akari", "remote")
	if err != nil {
		t.Fatal(err)
	}
	orphanID, err := st.UpsertProject(ctx, "local:box:/tmp/wt", "box", "", "wt", "wt", "orphaned")
	if err != nil {
		t.Fatal(err)
	}
	ann, err := st.Announce(ctx, store.AnnounceParams{
		UserID: ada.ID, Agent: "claude", SourceSessionID: "mcp-orphan",
		ProjectID: orphanID, Kind: "orphaned", Cwd: "/tmp/wt", Machine: "box",
	})
	if err != nil {
		t.Fatal(err)
	}

	sess := mcpSession(t, srv.URL, secret)
	var out struct {
		SessionID int64 `json:"session_id"`
		ProjectID int64 `json:"project_id"`
		Pinned    bool  `json:"pinned"`
	}
	callToolJSON(t, sess, "assign_session_project", map[string]any{
		"session_id": ann.SessionID, "project_id": remoteID,
	}, &out)
	if out.SessionID != ann.SessionID || out.ProjectID != remoteID || !out.Pinned {
		t.Fatalf("mcp assign = %+v, want session %d pinned to %d", out, ann.SessionID, remoteID)
	}

	var projectID int64
	var pinned bool
	if err := st.Pool.QueryRow(ctx,
		`SELECT project_id, project_pinned FROM sessions WHERE id = $1`, ann.SessionID).
		Scan(&projectID, &pinned); err != nil {
		t.Fatal(err)
	}
	if projectID != remoteID || !pinned {
		t.Fatalf("stored pin = project %d pinned=%v, want %d true", projectID, pinned, remoteID)
	}
}
